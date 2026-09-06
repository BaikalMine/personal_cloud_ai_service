package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
)

const GenerationDispatchLease = 2 * time.Minute

// Publication is separate from preparation: a partially received browser request
// must not become runnable before its payload and quota reservation are durable.
func (s *Store) QueueGenerationJobDispatch(ctx context.Context, jobID int64) (domain.GenerationJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	defer tx.Rollback()
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs
		SET dispatch_queued_at=COALESCE(dispatch_queued_at,now()),updated_at=now()
		WHERE id=$1 AND state='waiting_for_resources' AND batch_id IS NULL
		AND prompt_id IS NULL AND submission_started_at IS NULL AND cancellation_requested_at IS NULL
		AND dispatch_closed_at IS NULL AND resources_released_at IS NULL AND quota_reserved_on IS NOT NULL AND octet_length(payload_cipher)>0
		RETURNING `+generationJobColumns, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return job, ErrGenerationJobStateConflict
	}
	if err != nil {
		return job, err
	}
	if err = incrementGenerationJobRevision(ctx, tx); err != nil {
		return job, err
	}
	return job, tx.Commit()
}

// All generation launches share this claim. A lease may be reclaimed only before
// BeginGenerationJobSubmission; after that, lost receipts require reconciliation.
func (s *Store) ClaimNextGenerationDispatch(ctx context.Context, token string, maxActiveBatches int) (domain.GenerationJob, error) {
	if token == "" || !gpuTextValid(token, 96) {
		return domain.GenerationJob{}, ErrGPUWorkInput
	}
	if maxActiveBatches < 1 {
		maxActiveBatches = 1
	}
	if maxActiveBatches > 8 {
		maxActiveBatches = 8
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generationBatchClaimLockID); err != nil {
		return domain.GenerationJob{}, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT candidate.id FROM generation_jobs candidate
		LEFT JOIN generation_batches batch ON batch.id=candidate.batch_id
		LEFT JOIN users owner ON owner.id=candidate.user_id
		WHERE candidate.prompt_id IS NULL AND candidate.submission_started_at IS NULL
		AND candidate.dispatch_closed_at IS NULL AND candidate.resources_released_at IS NULL
		AND candidate.cancellation_requested_at IS NULL AND batch.cancellation_requested_at IS NULL
		AND (candidate.dispatch_until IS NULL OR candidate.dispatch_until<clock_timestamp())
		AND ((candidate.batch_id IS NULL AND candidate.dispatch_queued_at IS NOT NULL AND candidate.state='waiting_for_resources')
		 OR (candidate.batch_id IS NOT NULL
		  AND (candidate.state='draft' OR (candidate.dispatch_queued_at IS NOT NULL AND candidate.state IN ('preparing','uploading','waiting_for_resources')))
		  AND NOT EXISTS(SELECT 1 FROM generation_jobs sibling WHERE sibling.batch_id=candidate.batch_id AND sibling.id<>candidate.id
		   AND (sibling.state IN ('preparing','uploading','waiting_for_resources','queued','running','postprocessing','archiving')
		    OR (sibling.state='draft' AND sibling.batch_position<candidate.batch_position AND sibling.cancellation_requested_at IS NULL)))
		  AND (SELECT COUNT(*) FROM generation_jobs active WHERE active.batch_id IS NOT NULL AND active.id<>candidate.id
		   AND active.state IN ('preparing','uploading','waiting_for_resources','queued','running','postprocessing','archiving'))<$1))
		ORDER BY candidate.created_at-CASE WHEN owner.queue_priority THEN ($2::bigint*interval '1 second') ELSE interval '0 seconds' END,candidate.id
		FOR UPDATE OF candidate SKIP LOCKED LIMIT 1`, maxActiveBatches, int64(domain.GPUPriorityHeadStart.Seconds())).Scan(&id)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	previous, err := scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs WHERE id=$1`, id))
	if err != nil {
		return previous, err
	}
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs SET
		dispatch_queued_at=COALESCE(dispatch_queued_at,created_at),dispatch_token=$2,
		dispatch_until=clock_timestamp()+($3::bigint*interval '1 second'),
		state=CASE WHEN state='draft' THEN 'preparing' ELSE state END,
		status_message='Подготавливаем задание к запуску',
		state_changed_at=CASE WHEN state='draft' THEN now() ELSE state_changed_at END,updated_at=now()
		WHERE id=$1 RETURNING `+generationJobColumns, id, token, int64(GenerationDispatchLease.Seconds())))
	if err != nil {
		return job, err
	}
	if previous.State != job.State {
		if _, err = tx.ExecContext(ctx, `INSERT INTO generation_job_transitions
			(job_id,correlation_id,from_state,to_state,message,attempt,duration_ms)
			VALUES($1,$2,$3,$4,$5,$6,$7)`, id, job.CorrelationID, previous.State, job.State, job.StatusMessage, job.Attempt, max(time.Since(previous.StateChangedAt).Milliseconds(), 0)); err != nil {
			return job, err
		}
	}
	if job.BatchID != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE generation_batches SET updated_at=now() WHERE id=$1`, *job.BatchID); err != nil {
			return job, err
		}
	}
	if job.UserID != nil {
		if err = incrementUserNotificationRevision(ctx, tx, *job.UserID); err != nil {
			return job, err
		}
	}
	if err = incrementGenerationJobRevision(ctx, tx); err != nil {
		return job, err
	}
	return job, tx.Commit()
}

func (s *Store) BeginGenerationJobSubmission(ctx context.Context, jobID int64, token string) error {
	if token == "" {
		return ErrGenerationJobStateConflict
	}
	result, err := s.db.ExecContext(ctx, `UPDATE generation_jobs SET submission_started_at=clock_timestamp(),updated_at=now()
		WHERE id=$1 AND dispatch_token=$2 AND dispatch_until>clock_timestamp()
		AND state='waiting_for_resources' AND prompt_id IS NULL AND submission_started_at IS NULL
		AND cancellation_requested_at IS NULL AND dispatch_closed_at IS NULL AND resources_released_at IS NULL`, jobID, token)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		err = ErrGenerationJobStateConflict
	}
	return err
}

func (s *Store) PostponeGenerationDispatch(ctx context.Context, jobID int64, token string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE generation_jobs SET dispatch_token='',
		dispatch_until=clock_timestamp()+interval '5 seconds',status_message='Ожидаем свободное место в очереди ComfyUI',updated_at=now()
		WHERE id=$1 AND dispatch_token=$2 AND submission_started_at IS NULL AND prompt_id IS NULL
		AND state='waiting_for_resources' AND cancellation_requested_at IS NULL AND dispatch_closed_at IS NULL AND resources_released_at IS NULL`, jobID, token)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		err = ErrGenerationJobStateConflict
	}
	return err
}

func (s *Store) RejectGenerationJobSubmission(ctx context.Context, jobID int64, token string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE generation_jobs SET submission_rejected_at=now(),updated_at=now()
		WHERE id=$1 AND dispatch_token=$2 AND submission_started_at IS NOT NULL AND prompt_id IS NULL`, jobID, token)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		err = ErrGenerationJobStateConflict
	}
	return err
}

// Serialize resource release against the final pre-network dispatch marker.
func (s *Store) FenceGenerationJobResourceRelease(ctx context.Context, jobID int64, token string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE generation_jobs SET dispatch_closed_at=COALESCE(dispatch_closed_at,clock_timestamp()),dispatch_until=clock_timestamp(),dispatch_queued_at=NULL,dispatch_token=''
		WHERE id=$1 AND (dispatch_token=$2 OR dispatch_token='' OR prompt_id IS NOT NULL)
		AND (prompt_id IS NOT NULL OR submission_started_at IS NULL OR submission_rejected_at IS NOT NULL)`, jobID, token)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
