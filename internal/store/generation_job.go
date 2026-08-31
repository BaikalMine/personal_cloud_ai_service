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
	ErrGenerationJobStateConflict  = errors.New("generation job state conflict")
	ErrGenerationJobParentConflict = errors.New("generation job parent belongs to another user")
)

const generationJobColumns = `
	id,public_id,user_id,username_snapshot,request_id,parent_job_id,prompt_id,
	template_id,workflow_id,model_name,seed,payload_cipher,state,status_message,
	error_code,error_message,attempt,dependencies,input_count,state_changed_at,
	started_at,finished_at,resources_released_at,quota_reserved_on,quota_committed_at,
	cancellation_requested_at,cancellation_confirmed_at,created_at,updated_at`

type generationJobScanner interface {
	Scan(dest ...any) error
}

func scanGenerationJob(scanner generationJobScanner) (domain.GenerationJob, error) {
	var job domain.GenerationJob
	var userID, parentJobID sql.NullInt64
	var promptID sql.NullString
	var state string
	var dependencies []byte
	var startedAt, finishedAt, resourcesReleasedAt sql.NullTime
	var quotaReservedOn, quotaCommittedAt, cancellationRequestedAt, cancellationConfirmedAt sql.NullTime
	err := scanner.Scan(
		&job.ID, &job.PublicID, &userID, &job.UsernameSnapshot, &job.RequestID, &parentJobID, &promptID,
		&job.TemplateID, &job.WorkflowID, &job.ModelName, &job.Seed, &job.PayloadCipher, &state, &job.StatusMessage,
		&job.ErrorCode, &job.ErrorMessage, &job.Attempt, &dependencies, &job.InputCount, &job.StateChangedAt,
		&startedAt, &finishedAt, &resourcesReleasedAt, &quotaReservedOn, &quotaCommittedAt,
		&cancellationRequestedAt, &cancellationConfirmedAt, &job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	job.State = domain.GenerationJobState(state)
	if !job.State.Valid() {
		return domain.GenerationJob{}, fmt.Errorf("unknown generation job state %q", state)
	}
	if userID.Valid {
		value := userID.Int64
		job.UserID = &value
	}
	if parentJobID.Valid {
		value := parentJobID.Int64
		job.ParentJobID = &value
	}
	if promptID.Valid {
		job.PromptID = promptID.String
	}
	if startedAt.Valid {
		value := startedAt.Time
		job.StartedAt = &value
	}
	if finishedAt.Valid {
		value := finishedAt.Time
		job.FinishedAt = &value
	}
	if resourcesReleasedAt.Valid {
		value := resourcesReleasedAt.Time
		job.ResourcesReleasedAt = &value
	}
	if quotaReservedOn.Valid {
		value := quotaReservedOn.Time
		job.QuotaReservedOn = &value
	}
	if quotaCommittedAt.Valid {
		value := quotaCommittedAt.Time
		job.QuotaCommittedAt = &value
	}
	if cancellationRequestedAt.Valid {
		value := cancellationRequestedAt.Time
		job.CancellationRequestedAt = &value
	}
	if cancellationConfirmedAt.Valid {
		value := cancellationConfirmedAt.Time
		job.CancellationConfirmedAt = &value
	}
	if err := json.Unmarshal(dependencies, &job.Dependencies); err != nil {
		return domain.GenerationJob{}, fmt.Errorf("decode generation job dependencies: %w", err)
	}
	return job, nil
}

func (s *Store) CreateGenerationJob(ctx context.Context, params domain.CreateGenerationJobParams) (domain.GenerationJob, bool, error) {
	params.PublicID = strings.TrimSpace(params.PublicID)
	params.RequestID = strings.TrimSpace(params.RequestID)
	params.UsernameSnapshot = strings.TrimSpace(params.UsernameSnapshot)
	if params.UserID <= 0 || params.PublicID == "" || params.RequestID == "" {
		return domain.GenerationJob{}, false, errors.New("generation job identity is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	defer tx.Rollback()
	if params.ParentJobID != nil {
		var parentExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM generation_jobs WHERE id=$1 AND user_id=$2
		)`, *params.ParentJobID, params.UserID).Scan(&parentExists); err != nil {
			return domain.GenerationJob{}, false, err
		}
		if !parentExists {
			return domain.GenerationJob{}, false, ErrGenerationJobParentConflict
		}
	}

	query := `INSERT INTO generation_jobs(public_id,user_id,username_snapshot,request_id,parent_job_id,state,status_message)
		VALUES($1,$2,$3,$4,$5,'draft','Запуск создан') ON CONFLICT DO NOTHING RETURNING ` + generationJobColumns
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, query,
		params.PublicID, params.UserID, params.UsernameSnapshot, params.RequestID, params.ParentJobID,
	))
	created := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		job, err = scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
			FROM generation_jobs WHERE user_id=$1 AND request_id=$2 FOR UPDATE`, params.UserID, params.RequestID))
		created = false
	}
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	if created {
		if _, err := tx.ExecContext(ctx, `INSERT INTO generation_job_transitions(job_id,from_state,to_state,message,attempt)
			VALUES($1,'','draft',$2,1)`, job.ID, job.StatusMessage); err != nil {
			return domain.GenerationJob{}, false, err
		}
		if err := incrementGenerationJobRevision(ctx, tx); err != nil {
			return domain.GenerationJob{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationJob{}, false, err
	}
	return job, created, nil
}

// ClaimGenerationJob atomically reserves the browser idempotency key and its
// canonical job. A stale request row without a job is adopted by one caller,
// closing the crash window between the two legacy inserts.
func (s *Store) ClaimGenerationJob(ctx context.Context, params domain.CreateGenerationJobParams) (domain.GenerationJob, bool, error) {
	params.PublicID = strings.TrimSpace(params.PublicID)
	params.RequestID = strings.TrimSpace(params.RequestID)
	params.UsernameSnapshot = strings.TrimSpace(params.UsernameSnapshot)
	if params.UserID <= 0 || params.PublicID == "" || params.RequestID == "" {
		return domain.GenerationJob{}, false, errors.New("generation job identity is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_requests(user_id,request_id)
		VALUES($1,$2) ON CONFLICT(user_id,request_id) DO NOTHING`, params.UserID, params.RequestID); err != nil {
		return domain.GenerationJob{}, false, err
	}
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
		FROM generation_jobs WHERE user_id=$1 AND request_id=$2 FOR UPDATE`, params.UserID, params.RequestID))
	if err == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE generation_requests SET job_id=$1
			WHERE user_id=$2 AND request_id=$3 AND job_id IS NULL`, job.ID, params.UserID, params.RequestID); err != nil {
			return domain.GenerationJob{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.GenerationJob{}, false, err
		}
		return job, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationJob{}, false, err
	}
	if params.ParentJobID != nil {
		var parentExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM generation_jobs WHERE id=$1 AND user_id=$2
		)`, *params.ParentJobID, params.UserID).Scan(&parentExists); err != nil {
			return domain.GenerationJob{}, false, err
		}
		if !parentExists {
			return domain.GenerationJob{}, false, ErrGenerationJobParentConflict
		}
	}
	var promptID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT prompt_id FROM generation_requests
		WHERE user_id=$1 AND request_id=$2 FOR UPDATE`, params.UserID, params.RequestID).Scan(&promptID); err != nil {
		return domain.GenerationJob{}, false, err
	}
	initialState := domain.GenerationJobDraft
	message := "Запуск создан"
	dependencies := `[]`
	if promptID.Valid && strings.TrimSpace(promptID.String) != "" {
		initialState = domain.GenerationJobQueued
		message = "Восстановлен принятый ComfyUI prompt"
		dependencies = `["comfyui"]`
	}
	job, err = scanGenerationJob(tx.QueryRowContext(ctx, `INSERT INTO generation_jobs
		(public_id,user_id,username_snapshot,request_id,parent_job_id,prompt_id,state,status_message,dependencies)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb) ON CONFLICT DO NOTHING RETURNING `+generationJobColumns,
		params.PublicID, params.UserID, params.UsernameSnapshot, params.RequestID, params.ParentJobID,
		promptID, initialState, message, dependencies,
	))
	created := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		job, err = scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
			FROM generation_jobs WHERE user_id=$1 AND request_id=$2 FOR UPDATE`, params.UserID, params.RequestID))
		created = false
	}
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_requests SET job_id=$1
		WHERE user_id=$2 AND request_id=$3`, job.ID, params.UserID, params.RequestID); err != nil {
		return domain.GenerationJob{}, false, err
	}
	if created {
		if _, err := tx.ExecContext(ctx, `INSERT INTO generation_job_transitions(job_id,from_state,to_state,message,attempt)
			VALUES($1,'',$2,$3,1)`, job.ID, job.State, job.StatusMessage); err != nil {
			return domain.GenerationJob{}, false, err
		}
		if err := incrementGenerationJobRevision(ctx, tx); err != nil {
			return domain.GenerationJob{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationJob{}, false, err
	}
	return job, created && job.PromptID == "", nil
}

func (s *Store) GenerationJobByRequest(ctx context.Context, userID int64, requestID string) (domain.GenerationJob, error) {
	return scanGenerationJob(s.db.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
		FROM generation_jobs WHERE user_id=$1 AND request_id=$2`, userID, strings.TrimSpace(requestID)))
}

func (s *Store) GenerationJobByPublicID(ctx context.Context, userID int64, publicID string) (domain.GenerationJob, error) {
	return scanGenerationJob(s.db.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
		FROM generation_jobs WHERE user_id=$1 AND public_id=$2`, userID, strings.TrimSpace(publicID)))
}

func (s *Store) GenerationJobByPromptID(ctx context.Context, promptID string) (domain.GenerationJob, error) {
	return scanGenerationJob(s.db.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
		FROM generation_jobs WHERE prompt_id=$1`, strings.TrimSpace(promptID)))
}

func (s *Store) PrepareGenerationJob(ctx context.Context, jobID int64, prepared domain.PreparedGenerationJob) (domain.GenerationJob, error) {
	dependencies, err := json.Marshal(uniqueStrings(prepared.Dependencies))
	if err != nil {
		return domain.GenerationJob{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	defer tx.Rollback()
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs SET
		template_id=$2,workflow_id=$3,model_name=$4,seed=$5,payload_cipher=$6,dependencies=$7,input_count=$8,updated_at=now()
		WHERE id=$1 AND state IN ('draft','preparing','uploading') RETURNING `+generationJobColumns,
		jobID, strings.TrimSpace(prepared.TemplateID), strings.TrimSpace(prepared.WorkflowID), strings.TrimSpace(prepared.ModelName),
		prepared.Seed, prepared.PayloadCipher, dependencies, prepared.InputCount,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationJob{}, ErrGenerationJobStateConflict
	}
	if err != nil {
		return domain.GenerationJob{}, err
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return domain.GenerationJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationJob{}, err
	}
	return job, nil
}

func (s *Store) BindGenerationJobPrompt(ctx context.Context, jobID int64, promptID string) (domain.GenerationJob, error) {
	promptID = strings.TrimSpace(promptID)
	if promptID == "" {
		return domain.GenerationJob{}, errors.New("generation job prompt id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	defer tx.Rollback()
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs
		SET prompt_id=$2,updated_at=now() WHERE id=$1 AND prompt_id IS NULL
		RETURNING `+generationJobColumns, jobID, promptID))
	changed := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		job, err = scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
			FROM generation_jobs WHERE id=$1 FOR UPDATE`, jobID))
		if err == nil && job.PromptID != promptID {
			return domain.GenerationJob{}, ErrGenerationJobStateConflict
		}
	}
	if err != nil {
		return domain.GenerationJob{}, err
	}
	if job.UserID != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE generation_requests SET job_id=$1,prompt_id=$2,updated_at=now()
			WHERE user_id=$3 AND request_id=$4 AND (prompt_id IS NULL OR prompt_id=$2)`, job.ID, promptID, *job.UserID, job.RequestID); err != nil {
			return domain.GenerationJob{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quick_generation_mining_leases SET generation_job_id=$1
		WHERE prompt_id=$2 AND generation_job_id IS NULL`, job.ID, promptID); err != nil {
		return domain.GenerationJob{}, err
	}
	if changed {
		if err := incrementGenerationJobRevision(ctx, tx); err != nil {
			return domain.GenerationJob{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationJob{}, err
	}
	return job, nil
}

func (s *Store) TransitionGenerationJob(ctx context.Context, jobID int64, params domain.GenerationJobTransitionParams) (domain.GenerationJob, bool, error) {
	if !params.State.Valid() {
		return domain.GenerationJob{}, false, fmt.Errorf("invalid generation job state %q", params.State)
	}
	params.Message = strings.TrimSpace(params.Message)
	params.ErrorCode = strings.TrimSpace(params.ErrorCode)
	params.ErrorMessage = strings.TrimSpace(params.ErrorMessage)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	defer tx.Rollback()
	current, err := scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs WHERE id=$1 FOR UPDATE`, jobID))
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	attempt := params.Attempt
	if attempt <= 0 {
		attempt = current.Attempt
	}
	if attempt < 1 || attempt > 100 {
		return domain.GenerationJob{}, false, fmt.Errorf("invalid generation job attempt %d", attempt)
	}
	if params.State == domain.GenerationJobFailed {
		if params.Message == "" || params.ErrorCode == "" {
			return domain.GenerationJob{}, false, errors.New("failed generation job requires a message and error code")
		}
	} else {
		params.ErrorCode = ""
		params.ErrorMessage = ""
	}
	if current.State == params.State {
		if current.StatusMessage == params.Message && current.ErrorCode == params.ErrorCode && current.ErrorMessage == params.ErrorMessage && current.Attempt == attempt {
			if err := tx.Commit(); err != nil {
				return domain.GenerationJob{}, false, err
			}
			return current, false, nil
		}
		if current.State.Terminal() {
			return domain.GenerationJob{}, false, fmt.Errorf("%w: terminal job %s is immutable", ErrGenerationJobStateConflict, current.State)
		}
		job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs
			SET status_message=$2,error_code=$3,error_message=$4,attempt=$5,updated_at=now()
			WHERE id=$1 RETURNING `+generationJobColumns, jobID, params.Message, params.ErrorCode, params.ErrorMessage, attempt))
		if err != nil {
			return domain.GenerationJob{}, false, err
		}
		if err := incrementGenerationJobRevision(ctx, tx); err != nil {
			return domain.GenerationJob{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.GenerationJob{}, false, err
		}
		return job, true, nil
	}
	if !domain.CanTransitionGenerationJob(current.State, params.State) {
		return domain.GenerationJob{}, false, fmt.Errorf("%w: %s -> %s", ErrGenerationJobStateConflict, current.State, params.State)
	}
	if params.State == domain.GenerationJobQueued && current.PromptID == "" {
		return domain.GenerationJob{}, false, fmt.Errorf("%w: queued job has no prompt id", ErrGenerationJobStateConflict)
	}
	if params.State.Terminal() && current.ResourcesReleasedAt == nil {
		return domain.GenerationJob{}, false, fmt.Errorf("%w: terminal job still owns resources", ErrGenerationJobStateConflict)
	}
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs SET
		state=$2,status_message=$3,error_code=$4,error_message=$5,attempt=$6,state_changed_at=now(),updated_at=now(),
		started_at=CASE WHEN $2 IN ('running','postprocessing','archiving','completed') THEN COALESCE(started_at,now()) ELSE started_at END,
		finished_at=CASE WHEN $2 IN ('completed','failed','cancelled','expired') THEN COALESCE(finished_at,now()) ELSE finished_at END
		WHERE id=$1 RETURNING `+generationJobColumns,
		jobID, params.State, params.Message, params.ErrorCode, params.ErrorMessage, attempt,
	))
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_job_transitions
		(job_id,from_state,to_state,message,error_code,error_message,attempt)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, job.ID, current.State, job.State, job.StatusMessage, job.ErrorCode, job.ErrorMessage, job.Attempt); err != nil {
		return domain.GenerationJob{}, false, err
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return domain.GenerationJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationJob{}, false, err
	}
	return job, true, nil
}

func (s *Store) MarkGenerationJobResourcesReleased(ctx context.Context, jobID int64) (domain.GenerationJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, err
	}
	defer tx.Rollback()
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs
		SET resources_released_at=now(),updated_at=now()
		WHERE id=$1 AND resources_released_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM quick_generation_mining_leases WHERE generation_job_id=$1)
		RETURNING `+generationJobColumns, jobID))
	changed := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		job, err = scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
			FROM generation_jobs WHERE id=$1`, jobID))
	}
	if err != nil {
		return domain.GenerationJob{}, err
	}
	if !changed && job.ResourcesReleasedAt == nil {
		return domain.GenerationJob{}, fmt.Errorf("%w: generation job still has a mining lease", ErrGenerationJobStateConflict)
	}
	if changed {
		if err := incrementGenerationJobRevision(ctx, tx); err != nil {
			return domain.GenerationJob{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationJob{}, err
	}
	return job, nil
}

func (s *Store) RequestGenerationJobCancellation(ctx context.Context, jobID, userID int64) (domain.GenerationJob, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	defer tx.Rollback()
	current, err := scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
		FROM generation_jobs WHERE id=$1 AND user_id=$2 FOR UPDATE`, jobID, userID))
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	if !current.State.Cancellable() {
		return domain.GenerationJob{}, false, fmt.Errorf("%w: state %s is not cancellable", ErrGenerationJobStateConflict, current.State)
	}
	if current.CancellationRequestedAt != nil {
		if err := tx.Commit(); err != nil {
			return domain.GenerationJob{}, false, err
		}
		return current, false, nil
	}
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs
		SET cancellation_requested_at=now(),status_message='Отменяем генерацию',updated_at=now()
		WHERE id=$1 RETURNING `+generationJobColumns, jobID))
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return domain.GenerationJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationJob{}, false, err
	}
	return job, true, nil
}

func (s *Store) ClearGenerationJobCancellation(ctx context.Context, jobID, userID int64, message string) (domain.GenerationJob, bool, error) {
	message = strings.TrimSpace(message)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	defer tx.Rollback()
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs
		SET cancellation_requested_at=NULL,cancellation_confirmed_at=NULL,status_message=$3,updated_at=now()
		WHERE id=$1 AND user_id=$2 AND cancellation_requested_at IS NOT NULL
		  AND state NOT IN ('completed','failed','cancelled','expired')
		RETURNING `+generationJobColumns, jobID, userID, message))
	if errors.Is(err, sql.ErrNoRows) {
		job, err = scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
			FROM generation_jobs WHERE id=$1 AND user_id=$2`, jobID, userID))
		if err != nil {
			return domain.GenerationJob{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.GenerationJob{}, false, err
		}
		return job, false, nil
	}
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return domain.GenerationJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationJob{}, false, err
	}
	return job, true, nil
}

func (s *Store) ConfirmGenerationJobCancellation(ctx context.Context, jobID int64) (domain.GenerationJob, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	defer tx.Rollback()
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs
		SET cancellation_confirmed_at=now(),status_message='Отмена подтверждена',updated_at=now()
		WHERE id=$1 AND cancellation_requested_at IS NOT NULL AND cancellation_confirmed_at IS NULL
		  AND state NOT IN ('completed','failed','cancelled','expired')
		RETURNING `+generationJobColumns, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		job, err = scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs WHERE id=$1`, jobID))
		if err != nil {
			return domain.GenerationJob{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.GenerationJob{}, false, err
		}
		return job, false, nil
	}
	if err != nil {
		return domain.GenerationJob{}, false, err
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return domain.GenerationJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationJob{}, false, err
	}
	return job, true, nil
}

func (s *Store) LinkGenerationJobVariant(ctx context.Context, jobID int64, promptID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE quick_generation_variants SET job_id=$1 WHERE prompt_id=$2 AND (job_id IS NULL OR job_id=$1)`, jobID, strings.TrimSpace(promptID))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) LinkGenerationJobContentEvent(ctx context.Context, jobID int64, userID int64, promptID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE content_events SET generation_job_id=$1
		WHERE user_id=$2 AND service='comfyui' AND kind='comfyui_prompt' AND external_id=$3
		  AND (generation_job_id IS NULL OR generation_job_id=$1)`, jobID, userID, strings.TrimSpace(promptID))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListGenerationJobs(ctx context.Context, userID int64, limit int, finishedAfter time.Time) ([]domain.GenerationJob, error) {
	limit = boundedLimit(limit, 1, 100)
	rows, err := s.db.QueryContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs
		WHERE user_id=$1 AND (state NOT IN ('completed','failed','cancelled','expired') OR COALESCE(finished_at,created_at)>$3)
		ORDER BY created_at DESC,id DESC LIMIT $2`, userID, limit, finishedAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]domain.GenerationJob, 0)
	for rows.Next() {
		job, err := scanGenerationJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) ListActiveGenerationJobs(ctx context.Context, limit int) ([]domain.GenerationJob, error) {
	limit = boundedLimit(limit, 1, 500)
	rows, err := s.db.QueryContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs
		WHERE state NOT IN ('completed','failed','cancelled','expired') ORDER BY created_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]domain.GenerationJob, 0)
	for rows.Next() {
		job, err := scanGenerationJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) GenerationJobTransitions(ctx context.Context, jobID, userID int64) ([]domain.GenerationJobTransition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.job_id,t.from_state,t.to_state,
		t.message,t.error_code,t.error_message,t.attempt,t.created_at
		FROM generation_job_transitions t JOIN generation_jobs j ON j.id=t.job_id
		WHERE t.job_id=$1 AND j.user_id=$2 ORDER BY t.created_at,t.id`, jobID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.GenerationJobTransition, 0)
	for rows.Next() {
		var item domain.GenerationJobTransition
		var fromState, toState string
		if err := rows.Scan(&item.ID, &item.JobID, &fromState, &toState, &item.Message, &item.ErrorCode, &item.ErrorMessage, &item.Attempt, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.FromState = domain.GenerationJobState(fromState)
		item.ToState = domain.GenerationJobState(toState)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GenerationJobRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT revision FROM generation_job_revision WHERE id=1`).Scan(&revision)
	return revision, err
}

func incrementGenerationJobRevision(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE generation_job_revision SET revision=revision+1,changed_at=now() WHERE id=1`)
	return err
}
