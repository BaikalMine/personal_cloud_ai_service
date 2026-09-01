package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

var (
	ErrGenerationBatchConflict       = errors.New("generation batch conflict")
	ErrGenerationBatchWinnerConflict = errors.New("generation batch winner conflict")
)

const generationBatchClaimLockID int64 = 0x47454e4241544348

const generationBatchColumns = `
	b.id,b.public_id,b.user_id,b.username_snapshot,b.request_id,b.parent_batch_id,
	COALESCE(parent.public_id,''),b.source_job_id,COALESCE(source.public_id,''),
	b.winner_job_id,COALESCE(winner.public_id,''),b.template_id,b.workflow_id,b.model_name,
	b.experiment_mode,b.parameter_name,b.parameter_values,b.seed_locked,b.total_count,b.max_parallel,
	b.cancellation_requested_at,b.created_at,
	GREATEST(b.updated_at,COALESCE(MAX(job.updated_at),b.updated_at)),
	COUNT(job.id) FILTER (WHERE job.state='draft'),
	COUNT(job.id) FILTER (WHERE job.state IN ('preparing','uploading','waiting_for_resources','queued','running','postprocessing','archiving')),
	COUNT(job.id) FILTER (WHERE job.state='completed'),
	COUNT(job.id) FILTER (WHERE job.state='failed'),
	COUNT(job.id) FILTER (WHERE job.state='cancelled'),
	COUNT(job.id) FILTER (WHERE job.state='expired')`

const generationBatchJoins = `
	LEFT JOIN generation_batches parent ON parent.id=b.parent_batch_id
	LEFT JOIN generation_jobs source ON source.id=b.source_job_id
	LEFT JOIN generation_jobs winner ON winner.id=b.winner_job_id
	LEFT JOIN generation_jobs job ON job.batch_id=b.id`

const generationBatchGroupBy = `
	GROUP BY b.id,parent.public_id,source.public_id,winner.public_id`

type generationBatchScanner interface {
	Scan(dest ...any) error
}

func scanGenerationBatch(scanner generationBatchScanner) (domain.GenerationBatch, error) {
	var batch domain.GenerationBatch
	var userID, parentBatchID, sourceJobID, winnerJobID sql.NullInt64
	var cancellationRequestedAt sql.NullTime
	var mode string
	var parameterValues []byte
	err := scanner.Scan(
		&batch.ID, &batch.PublicID, &userID, &batch.UsernameSnapshot, &batch.RequestID, &parentBatchID,
		&batch.ParentBatchPublicID, &sourceJobID, &batch.SourceJobPublicID,
		&winnerJobID, &batch.WinnerJobPublicID, &batch.TemplateID, &batch.WorkflowID, &batch.ModelName,
		&mode, &batch.ParameterName, &parameterValues, &batch.SeedLocked, &batch.TotalCount, &batch.MaxParallel,
		&cancellationRequestedAt, &batch.CreatedAt, &batch.UpdatedAt,
		&batch.DraftCount, &batch.ActiveCount, &batch.CompletedCount, &batch.FailedCount,
		&batch.CancelledCount, &batch.ExpiredCount,
	)
	if err != nil {
		return domain.GenerationBatch{}, err
	}
	batch.Mode = domain.GenerationBatchMode(mode)
	if !batch.Mode.Valid() {
		return domain.GenerationBatch{}, fmt.Errorf("unknown generation batch mode %q", mode)
	}
	if err := json.Unmarshal(parameterValues, &batch.ParameterValues); err != nil {
		return domain.GenerationBatch{}, fmt.Errorf("decode generation batch values: %w", err)
	}
	if userID.Valid {
		value := userID.Int64
		batch.UserID = &value
	}
	if parentBatchID.Valid {
		value := parentBatchID.Int64
		batch.ParentBatchID = &value
	}
	if sourceJobID.Valid {
		value := sourceJobID.Int64
		batch.SourceJobID = &value
	}
	if winnerJobID.Valid {
		value := winnerJobID.Int64
		batch.WinnerJobID = &value
	}
	if cancellationRequestedAt.Valid {
		value := cancellationRequestedAt.Time
		batch.CancellationRequestedAt = &value
	}
	return batch, nil
}

func validateGenerationBatchParams(params domain.CreateGenerationBatchParams) error {
	params.PublicID = strings.TrimSpace(params.PublicID)
	params.RequestID = strings.TrimSpace(params.RequestID)
	if params.UserID <= 0 || params.PublicID == "" || params.RequestID == "" {
		return errors.New("generation batch identity is required")
	}
	if !params.Mode.Valid() || len(params.Jobs) < 2 || len(params.Jobs) > 20 {
		return errors.New("generation batch must contain 2 to 20 jobs")
	}
	if params.Mode == domain.GenerationBatchParameter && strings.TrimSpace(params.ParameterName) == "" {
		return errors.New("generation batch parameter is required")
	}
	if params.Mode == domain.GenerationBatchParameter && len(params.ParameterValues) != len(params.Jobs) {
		return errors.New("generation batch parameter values must match jobs")
	}
	if params.Mode == domain.GenerationBatchSeeds && strings.TrimSpace(params.ParameterName) != "" {
		return errors.New("seed batch cannot declare a parameter")
	}
	if params.Mode == domain.GenerationBatchSeeds && len(params.ParameterValues) != 0 {
		return errors.New("seed batch cannot declare parameter values")
	}
	if params.MaxParallel < 1 || params.MaxParallel > 4 {
		return errors.New("generation batch parallel limit is invalid")
	}
	for index, job := range params.Jobs {
		if strings.TrimSpace(job.PublicID) == "" || strings.TrimSpace(job.RequestID) == "" || job.Position != index+1 {
			return errors.New("generation batch job identity is invalid")
		}
		if len(job.Prepared.PayloadCipher) == 0 || strings.TrimSpace(job.Prepared.TemplateID) == "" || strings.TrimSpace(job.Prepared.WorkflowID) == "" {
			return errors.New("generation batch job is not prepared")
		}
	}
	return nil
}

func (s *Store) CreateGenerationBatch(ctx context.Context, params domain.CreateGenerationBatchParams) (domain.GenerationBatch, bool, error) {
	if err := validateGenerationBatchParams(params); err != nil {
		return domain.GenerationBatch{}, false, err
	}
	values := params.ParameterValues
	if values == nil {
		values = []string{}
	}
	parameterValues, err := json.Marshal(values)
	if err != nil {
		return domain.GenerationBatch{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.GenerationBatch{}, false, err
	}
	defer tx.Rollback()

	var existingID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM generation_batches WHERE user_id=$1 AND request_id=$2 FOR UPDATE`, params.UserID, strings.TrimSpace(params.RequestID)).Scan(&existingID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return domain.GenerationBatch{}, false, err
		}
		batch, loadErr := s.generationBatchByID(ctx, params.UserID, existingID)
		return batch, false, loadErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationBatch{}, false, err
	}
	if params.ParentBatchID != nil {
		var owned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM generation_batches WHERE id=$1 AND user_id=$2)`, *params.ParentBatchID, params.UserID).Scan(&owned); err != nil {
			return domain.GenerationBatch{}, false, err
		}
		if !owned {
			return domain.GenerationBatch{}, false, ErrGenerationBatchConflict
		}
	}
	if params.SourceJobID != nil {
		var owned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM generation_jobs WHERE id=$1 AND user_id=$2 AND state='completed')`, *params.SourceJobID, params.UserID).Scan(&owned); err != nil {
			return domain.GenerationBatch{}, false, err
		}
		if !owned {
			return domain.GenerationBatch{}, false, ErrGenerationBatchConflict
		}
	}

	var allowed bool
	var dailyLimit int
	var totalLimit, totalUsed int64
	if err := tx.QueryRowContext(ctx, `SELECT can_use_quick_generation,generation_daily_limit,generation_total_limit,generation_total_used
		FROM users WHERE id=$1 FOR UPDATE`, params.UserID).Scan(&allowed, &dailyLimit, &totalLimit, &totalUsed); err != nil {
		return domain.GenerationBatch{}, false, err
	}
	if !allowed {
		return domain.GenerationBatch{}, false, ErrQuickGenerationForbidden
	}
	count := len(params.Jobs)
	if totalLimit > 0 && (int64(count) > totalLimit-totalUsed) {
		return domain.GenerationBatch{}, false, ErrQuickGenerationTotalLimit
	}
	var usageDate time.Time
	if err := tx.QueryRowContext(ctx, `SELECT timezone('Europe/Moscow',now())::date`).Scan(&usageDate); err != nil {
		return domain.GenerationBatch{}, false, err
	}
	var usedToday int
	err = tx.QueryRowContext(ctx, `SELECT used_count FROM quick_generation_daily_usage WHERE user_id=$1 AND usage_date=$2`, params.UserID, usageDate).Scan(&usedToday)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationBatch{}, false, err
	}
	if dailyLimit > 0 && count > dailyLimit-usedToday {
		return domain.GenerationBatch{}, false, ErrQuickGenerationDailyLimit
	}

	var batchID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO generation_batches
		(public_id,user_id,username_snapshot,request_id,parent_batch_id,source_job_id,template_id,workflow_id,model_name,
		experiment_mode,parameter_name,parameter_values,seed_locked,total_count,max_parallel)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id`,
		strings.TrimSpace(params.PublicID), params.UserID, strings.TrimSpace(params.UsernameSnapshot), strings.TrimSpace(params.RequestID),
		params.ParentBatchID, params.SourceJobID, strings.TrimSpace(params.TemplateID), strings.TrimSpace(params.WorkflowID), strings.TrimSpace(params.ModelName),
		params.Mode, strings.TrimSpace(params.ParameterName), parameterValues, params.SeedLocked, count, params.MaxParallel,
	).Scan(&batchID); err != nil {
		return domain.GenerationBatch{}, false, err
	}
	for _, child := range params.Jobs {
		dependencies, marshalErr := json.Marshal(uniqueStrings(child.Prepared.Dependencies))
		if marshalErr != nil {
			return domain.GenerationBatch{}, false, marshalErr
		}
		var jobID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO generation_jobs
			(public_id,correlation_id,user_id,username_snapshot,request_id,parent_job_id,batch_id,batch_position,experiment_value,
			template_id,workflow_id,model_name,seed,payload_cipher,state,status_message,dependencies,input_count,quota_reserved_on)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'draft','Ожидает запуска в пакете',$15,$16,$17)
			RETURNING id`, strings.TrimSpace(child.PublicID), strings.TrimSpace(child.CorrelationID), params.UserID,
			strings.TrimSpace(params.UsernameSnapshot), strings.TrimSpace(child.RequestID), child.ParentJobID, batchID, child.Position,
			strings.TrimSpace(child.ExperimentValue), child.Prepared.TemplateID, child.Prepared.WorkflowID, child.Prepared.ModelName,
			child.Prepared.Seed, child.Prepared.PayloadCipher, dependencies, child.Prepared.InputCount, usageDate).Scan(&jobID); err != nil {
			return domain.GenerationBatch{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO generation_job_transitions(job_id,correlation_id,from_state,to_state,message,attempt)
			VALUES($1,$2,'','draft','Ожидает запуска в пакете',1)`, jobID, strings.TrimSpace(child.CorrelationID)); err != nil {
			return domain.GenerationBatch{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO generation_requests(user_id,request_id,correlation_id,job_id)
			VALUES($1,$2,$3,$4)`, params.UserID, strings.TrimSpace(child.RequestID), strings.TrimSpace(child.CorrelationID), jobID); err != nil {
			return domain.GenerationBatch{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quick_generation_daily_usage(user_id,usage_date,used_count)
		VALUES($1,$2,$3) ON CONFLICT(user_id,usage_date) DO UPDATE
		SET used_count=quick_generation_daily_usage.used_count+EXCLUDED.used_count`, params.UserID, usageDate, count); err != nil {
		return domain.GenerationBatch{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET generation_total_used=generation_total_used+$2 WHERE id=$1`, params.UserID, count); err != nil {
		return domain.GenerationBatch{}, false, err
	}
	if err := incrementUserNotificationRevision(ctx, tx, params.UserID); err != nil {
		return domain.GenerationBatch{}, false, err
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return domain.GenerationBatch{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationBatch{}, false, err
	}
	batch, err := s.generationBatchByID(ctx, params.UserID, batchID)
	return batch, true, err
}

func (s *Store) generationBatchByID(ctx context.Context, userID, batchID int64) (domain.GenerationBatch, error) {
	query := `SELECT ` + generationBatchColumns + ` FROM generation_batches b ` + generationBatchJoins + `
		WHERE b.id=$1 AND b.user_id=$2 ` + generationBatchGroupBy
	return scanGenerationBatch(s.db.QueryRowContext(ctx, query, batchID, userID))
}

func (s *Store) GenerationBatchByPublicID(ctx context.Context, userID int64, publicID string) (domain.GenerationBatch, error) {
	query := `SELECT ` + generationBatchColumns + ` FROM generation_batches b ` + generationBatchJoins + `
		WHERE b.public_id=$1 AND b.user_id=$2 ` + generationBatchGroupBy
	return scanGenerationBatch(s.db.QueryRowContext(ctx, query, strings.TrimSpace(publicID), userID))
}

func (s *Store) ListGenerationBatches(ctx context.Context, userID int64, limit int, finishedAfter time.Time) ([]domain.GenerationBatch, error) {
	if finishedAfter.IsZero() {
		return nil, errors.New("generation batch history boundary is required")
	}
	limit = boundedLimit(limit, 1, 40)
	query := `SELECT ` + generationBatchColumns + ` FROM generation_batches b ` + generationBatchJoins + `
		WHERE b.user_id=$1 AND (b.created_at>$3 OR EXISTS(
			SELECT 1 FROM generation_jobs active WHERE active.batch_id=b.id
			AND active.state NOT IN ('completed','failed','cancelled','expired')
		)) ` + generationBatchGroupBy + ` ORDER BY b.created_at DESC,b.id DESC LIMIT $2`
	rows, err := s.db.QueryContext(ctx, query, userID, limit, finishedAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.GenerationBatch, 0)
	for rows.Next() {
		item, scanErr := scanGenerationBatch(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GenerationBatchJobs(ctx context.Context, userID, batchID int64) ([]domain.GenerationJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs
		WHERE user_id=$1 AND batch_id=$2 ORDER BY batch_position,id`, userID, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]domain.GenerationJob, 0, 20)
	for rows.Next() {
		job, scanErr := scanGenerationJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// ClaimNextGenerationBatchJob starts one waiting child only when the same
// batch has no active sibling and the global batch window still has room.
func (s *Store) ClaimNextGenerationBatchJob(ctx context.Context, maxActive int) (domain.GenerationJob, error) {
	if maxActive < 1 {
		maxActive = 1
	}
	if maxActive > 8 {
		maxActive = 8
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generationBatchClaimLockID); err != nil {
		return domain.GenerationJob{}, err
	}
	var jobID int64
	var previousStateChangedAt time.Time
	err = tx.QueryRowContext(ctx, `SELECT candidate.id FROM generation_jobs candidate
		JOIN generation_batches batch ON batch.id=candidate.batch_id
		WHERE candidate.state='draft' AND candidate.prompt_id IS NULL
		  AND candidate.cancellation_requested_at IS NULL AND batch.cancellation_requested_at IS NULL
		  AND candidate.batch_position=(SELECT MIN(waiting.batch_position) FROM generation_jobs waiting
			WHERE waiting.batch_id=candidate.batch_id AND waiting.state='draft' AND waiting.cancellation_requested_at IS NULL)
		  AND NOT EXISTS(SELECT 1 FROM generation_jobs sibling WHERE sibling.batch_id=candidate.batch_id
			AND sibling.id<>candidate.id AND sibling.state IN ('preparing','uploading','waiting_for_resources','queued','running','postprocessing','archiving'))
		  AND (SELECT COUNT(*) FROM generation_jobs active WHERE active.batch_id IS NOT NULL
			AND active.state IN ('preparing','uploading','waiting_for_resources','queued','running','postprocessing','archiving')) < $1
		ORDER BY batch.created_at,batch.id,candidate.batch_position,candidate.id
		FOR UPDATE OF candidate SKIP LOCKED LIMIT 1`, maxActive).Scan(&jobID)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT state_changed_at FROM generation_jobs WHERE id=$1`, jobID).Scan(&previousStateChangedAt); err != nil {
		return domain.GenerationJob{}, err
	}
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs
		SET state='preparing',status_message='Подготавливаем вариант из пакета',state_changed_at=now(),updated_at=now()
		WHERE id=$1 AND state='draft' RETURNING `+generationJobColumns, jobID))
	if err != nil {
		return domain.GenerationJob{}, err
	}
	durationMS := time.Since(previousStateChangedAt).Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_job_transitions
		(job_id,correlation_id,from_state,to_state,message,attempt,duration_ms)
		VALUES($1,$2,'draft','preparing',$3,$4,$5)`, job.ID, job.CorrelationID, job.StatusMessage, job.Attempt, durationMS); err != nil {
		return domain.GenerationJob{}, err
	}
	if job.BatchID == nil {
		return domain.GenerationJob{}, errors.New("claimed generation batch job has no batch")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_batches SET updated_at=now() WHERE id=$1`, *job.BatchID); err != nil {
		return domain.GenerationJob{}, err
	}
	if job.UserID != nil {
		if err := incrementUserNotificationRevision(ctx, tx, *job.UserID); err != nil {
			return domain.GenerationJob{}, err
		}
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return domain.GenerationJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationJob{}, err
	}
	return job, nil
}

func (s *Store) RequestGenerationBatchCancellation(ctx context.Context, userID int64, publicID string) (domain.GenerationBatch, []domain.GenerationJob, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationBatch{}, nil, false, err
	}
	defer tx.Rollback()
	var batchID int64
	var requestedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT id,cancellation_requested_at FROM generation_batches
		WHERE user_id=$1 AND public_id=$2 FOR UPDATE`, userID, strings.TrimSpace(publicID)).Scan(&batchID, &requestedAt); err != nil {
		return domain.GenerationBatch{}, nil, false, err
	}
	changed := !requestedAt.Valid
	if changed {
		if _, err := tx.ExecContext(ctx, `UPDATE generation_batches SET cancellation_requested_at=now(),updated_at=now() WHERE id=$1`, batchID); err != nil {
			return domain.GenerationBatch{}, nil, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE generation_jobs SET cancellation_requested_at=COALESCE(cancellation_requested_at,now()),updated_at=now()
			WHERE batch_id=$1 AND user_id=$2 AND state IN ('draft','preparing','uploading','waiting_for_resources','queued','running')`, batchID, userID); err != nil {
			return domain.GenerationBatch{}, nil, false, err
		}
		if err := incrementGenerationJobRevision(ctx, tx); err != nil {
			return domain.GenerationBatch{}, nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationBatch{}, nil, false, err
	}
	batch, err := s.generationBatchByID(ctx, userID, batchID)
	if err != nil {
		return domain.GenerationBatch{}, nil, false, err
	}
	jobs, err := s.GenerationBatchJobs(ctx, userID, batchID)
	return batch, jobs, changed, err
}

func (s *Store) SetGenerationBatchWinner(ctx context.Context, userID int64, batchPublicID, jobPublicID string) (domain.GenerationBatch, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationBatch{}, err
	}
	defer tx.Rollback()
	var batchID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM generation_batches WHERE user_id=$1 AND public_id=$2 FOR UPDATE`, userID, strings.TrimSpace(batchPublicID)).Scan(&batchID); err != nil {
		return domain.GenerationBatch{}, err
	}
	var jobID int64
	var jobBatchID sql.NullInt64
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT id,batch_id,state FROM generation_jobs
		WHERE user_id=$1 AND public_id=$2 FOR UPDATE`, userID, strings.TrimSpace(jobPublicID)).Scan(&jobID, &jobBatchID, &state); err != nil {
		return domain.GenerationBatch{}, err
	}
	if !jobBatchID.Valid || jobBatchID.Int64 != batchID || domain.GenerationJobState(state) != domain.GenerationJobCompleted {
		return domain.GenerationBatch{}, ErrGenerationBatchWinnerConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_batches SET winner_job_id=$2,updated_at=now() WHERE id=$1`, batchID, jobID); err != nil {
		return domain.GenerationBatch{}, err
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return domain.GenerationBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationBatch{}, err
	}
	return s.generationBatchByID(ctx, userID, batchID)
}
