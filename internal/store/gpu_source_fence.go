package store

import (
	"context"
	"database/sql"
	"errors"

	"ai-access-gateway/internal/domain"
)

func (s *Store) GenerationJobForGPUWork(ctx context.Context, workID string) (domain.GenerationJob, error) {
	return scanGenerationJob(s.db.QueryRowContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs
		WHERE public_id=(SELECT job_key FROM gpu_work WHERE id=$1 AND kind='generation')`, workID))
}

// A new source claim fences the old sender before its GPU reservation can be
// reused. Neither an expired GPU lease nor an empty executor queue is evidence.
func (s *Store) RequeueUnsubmittedGPUWork(ctx context.Context, id, token string) (domain.GPUWork, error) {
	if token == "" {
		return domain.GPUWork{}, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUWork{}, err
	}
	defer tx.Rollback()
	resource, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return work, err
	}
	if work.State != domain.GPUWorkHeld && work.State != domain.GPUWorkUncertain {
		return work, tx.Commit()
	}
	if work.LeaseToken == token {
		return work, tx.Commit()
	}
	if work.ExternalID != "" {
		return work, ErrGPUWorkConflict
	}
	var sourceID int64
	switch work.Kind {
	case domain.GPUWorkGeneration:
		err = tx.QueryRowContext(ctx, `SELECT id FROM generation_jobs WHERE public_id=$1
			AND dispatch_token=$2 AND dispatch_until>clock_timestamp() AND submission_started_at IS NULL
			AND prompt_id IS NULL AND dispatch_closed_at IS NULL AND resources_released_at IS NULL
			AND cancellation_requested_at IS NULL AND state='waiting_for_resources' FOR UPDATE`, work.JobKey, token).Scan(&sourceID)
	case domain.GPUWorkTraining:
		err = tx.QueryRowContext(ctx, `SELECT id FROM lora_training_jobs WHERE public_id=$1
			AND dispatch_token=$2 AND agent_submission_started_at IS NULL AND agent_job_id=''
			AND cancellation_requested_at IS NULL AND state='uploading' FOR UPDATE`, work.JobKey, token).Scan(&sourceID)
	default:
		return work, ErrGPUWorkInput
	}
	if errors.Is(err, sql.ErrNoRows) {
		return work, ErrGPUWorkConflict
	}
	if err != nil {
		return work, err
	}
	work, err = scanGPUWork(tx.QueryRowContext(ctx, `UPDATE gpu_work SET state='waiting',phase='reserved',
		lease_token='',lease_until=NULL,held_at=NULL,ready_until=now()+($2::bigint*interval '1 second'),updated_at=now()
		WHERE id=$1 RETURNING `+gpuWorkColumns, id, int64(domain.GPUIntentLifetime.Seconds())))
	if err != nil {
		return work, err
	}
	if err = gpuEvent(ctx, tx, id, work.State, "source_claim_recovered", "Previous sender fenced before submission"); err != nil {
		return work, err
	}
	if err = invalidateGPUObservation(ctx, tx, resource.ID); err != nil {
		return work, err
	}
	return work, tx.Commit()
}

func checkGPUSourceFence(ctx context.Context, tx *sql.Tx, work domain.GPUWork) error {
	var id int64
	var err error
	switch work.Kind {
	case domain.GPUWorkGeneration:
		err = tx.QueryRowContext(ctx, `SELECT id FROM generation_jobs WHERE public_id=$1
			AND dispatch_closed_at IS NOT NULL AND (submission_started_at IS NULL OR submission_rejected_at IS NOT NULL)
			FOR UPDATE`, work.JobKey).Scan(&id)
	case domain.GPUWorkTraining:
		// A queued source has already revoked its sender token. Lock it through
		// release so a new claim cannot race the evidence check.
		err = tx.QueryRowContext(ctx, `SELECT id FROM lora_training_jobs WHERE public_id=$1
			AND agent_submission_started_at IS NULL AND agent_job_id=''
			AND state IN ('queued','completed','failed','cancelled') FOR UPDATE`, work.JobKey).Scan(&id)
	default:
		return ErrGPUWorkInput
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGPUWorkConflict
	}
	return err
}

func (s *Store) CancelFencedWaitingGPUWork(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return err
	}
	if work.State != domain.GPUWorkWaiting {
		return ErrGPUWorkConflict
	}
	if err = checkGPUSourceFence(ctx, tx, work); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gpu_work SET state='cancelled',cancellation_requested=true,released_at=now(),updated_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	if err = gpuEvent(ctx, tx, id, domain.GPUWorkCancelled, "dispatch_fenced", "Source revoked before admission"); err != nil {
		return err
	}
	return tx.Commit()
}
