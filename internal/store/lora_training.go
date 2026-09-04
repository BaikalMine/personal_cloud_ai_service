package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

var ErrLoraTrainingAlreadyActive = errors.New("an image LoRA training job is already active")

const loraTrainingColumns = `
	id, public_id, user_id, username_snapshot, request_id, profile_id, family, base_model,
	name, output_name, trigger_word, concept_type, preset, resolution, max_train_steps,
	network_dim, network_alpha, learning_rate, seed, sample_count, dataset_bytes, dataset_path,
	state, stage, progress, message, error_message, agent_job_id, artifact_name, artifact_bytes,
	cancellation_requested_at, started_at, finished_at, created_at, updated_at`

func (s *Store) CreateLoraTrainingJob(ctx context.Context, params domain.CreateLoraTrainingJobParams) (domain.LoraTrainingJob, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO lora_training_jobs (
			public_id,user_id,username_snapshot,request_id,profile_id,family,base_model,name,output_name,
			trigger_word,concept_type,preset,resolution,max_train_steps,network_dim,network_alpha,
			learning_rate,seed,sample_count,dataset_bytes,dataset_path
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		RETURNING `+loraTrainingColumns,
		params.PublicID, params.UserID, params.UsernameSnapshot, params.RequestID, params.ProfileID,
		params.Family, params.BaseModel, params.Name, params.OutputName, params.TriggerWord,
		params.ConceptType, params.Preset, params.Resolution, params.MaxTrainSteps, params.NetworkDim,
		params.NetworkAlpha, params.LearningRate, params.Seed, params.SampleCount, params.DatasetBytes, params.DatasetPath,
	)
	job, err := scanLoraTrainingJob(row)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == "lora_training_jobs_user_active_idx" {
		return domain.LoraTrainingJob{}, ErrLoraTrainingAlreadyActive
	}
	return job, err
}

func (s *Store) LoraTrainingJobByPublicID(ctx context.Context, publicID string, userID int64, admin bool) (domain.LoraTrainingJob, error) {
	query := `SELECT ` + loraTrainingColumns + ` FROM lora_training_jobs WHERE public_id=$1 AND user_id=$2`
	args := []any{publicID, userID}
	if admin {
		query = `SELECT ` + loraTrainingColumns + ` FROM lora_training_jobs WHERE public_id=$1`
		args = args[:1]
	}
	return scanLoraTrainingJob(s.db.QueryRowContext(ctx, query, args...))
}

func (s *Store) LoraTrainingJobByID(ctx context.Context, id int64) (domain.LoraTrainingJob, error) {
	return scanLoraTrainingJob(s.db.QueryRowContext(ctx, `SELECT `+loraTrainingColumns+` FROM lora_training_jobs WHERE id=$1`, id))
}

func (s *Store) ListLoraTrainingJobsByUser(ctx context.Context, userID int64, limit int) ([]domain.LoraTrainingJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	return s.listLoraTrainingJobs(ctx, `SELECT `+loraTrainingColumns+` FROM lora_training_jobs WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, userID, limit)
}

func (s *Store) ListLoraTrainingJobs(ctx context.Context, limit int) ([]domain.LoraTrainingJob, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.listLoraTrainingJobs(ctx, `SELECT `+loraTrainingColumns+` FROM lora_training_jobs ORDER BY created_at DESC,id DESC LIMIT $1`, limit)
}

func (s *Store) ActiveLoraTrainingJobs(ctx context.Context) ([]domain.LoraTrainingJob, error) {
	return s.listLoraTrainingJobs(ctx, `SELECT `+loraTrainingColumns+` FROM lora_training_jobs WHERE state IN ('uploading','preparing','caching','running','installing') ORDER BY created_at,id`)
}

func (s *Store) listLoraTrainingJobs(ctx context.Context, query string, args ...any) ([]domain.LoraTrainingJob, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]domain.LoraTrainingJob, 0)
	for rows.Next() {
		job, err := scanLoraTrainingJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) ClaimNextLoraTrainingJob(ctx context.Context) (domain.LoraTrainingJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.LoraTrainingJob{}, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `
		SELECT job.id FROM lora_training_jobs job
		LEFT JOIN users owner ON owner.id=job.user_id
		WHERE job.state='queued' AND job.cancellation_requested_at IS NULL
		ORDER BY COALESCE(owner.pause_mining_for_quick_generation,FALSE) DESC,job.created_at,job.id
		FOR UPDATE OF job SKIP LOCKED LIMIT 1
	`).Scan(&id)
	if err != nil {
		return domain.LoraTrainingJob{}, err
	}
	job, err := scanLoraTrainingJob(tx.QueryRowContext(ctx, `
		UPDATE lora_training_jobs SET state='uploading',stage='Передаём датасет агенту',progress=2,
			message='Подготавливаем защищённую передачу файлов.',updated_at=now()
		WHERE id=$1 RETURNING `+loraTrainingColumns, id))
	if err != nil {
		return domain.LoraTrainingJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.LoraTrainingJob{}, err
	}
	return job, nil
}

func (s *Store) RequeueLoraTrainingJob(ctx context.Context, id int64, message string) (domain.LoraTrainingJob, error) {
	return scanLoraTrainingJob(s.db.QueryRowContext(ctx, `
		UPDATE lora_training_jobs SET
			state=CASE WHEN cancellation_requested_at IS NULL THEN 'queued' ELSE 'cancelled' END,
			stage=CASE WHEN cancellation_requested_at IS NULL THEN 'В очереди' ELSE 'Отменено' END,
			progress=CASE WHEN cancellation_requested_at IS NULL THEN 0 ELSE 100 END,
			message=CASE WHEN cancellation_requested_at IS NULL THEN $2 ELSE 'Задание отменено во время передачи датасета.' END,
			finished_at=CASE WHEN cancellation_requested_at IS NULL THEN finished_at ELSE COALESCE(finished_at,now()) END,
			updated_at=now()
		WHERE id=$1 AND state='uploading' AND agent_job_id=''
		RETURNING `+loraTrainingColumns,
		id, message,
	))
}

func (s *Store) AttachLoraTrainingAgentJob(ctx context.Context, id int64, agentJobID, stage, message string, progress int) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE lora_training_jobs SET agent_job_id=$2,state='preparing',stage=$3,message=$4,progress=$5,
			started_at=COALESCE(started_at,now()),updated_at=now()
		WHERE id=$1 AND state='uploading'
	`, id, agentJobID, stage, message, progress)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
}

type UpdateLoraTrainingJobParams struct {
	State         domain.LoraTrainingState
	Stage         string
	Progress      int
	Message       string
	ErrorMessage  string
	ArtifactName  string
	ArtifactBytes int64
}

func (s *Store) UpdateLoraTrainingJob(ctx context.Context, id int64, params UpdateLoraTrainingJobParams) error {
	if !params.State.Valid() {
		return errors.New("invalid LoRA training state")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE lora_training_jobs SET state=$2,stage=$3,progress=$4,message=$5,error_message=$6,
			artifact_name=$7,artifact_bytes=$8,
			started_at=CASE WHEN $2 IN ('preparing','caching','running','installing') THEN COALESCE(started_at,now()) ELSE started_at END,
			finished_at=CASE WHEN $2 IN ('completed','failed','cancelled') THEN COALESCE(finished_at,now()) ELSE finished_at END,
			updated_at=now()
		WHERE id=$1
	`, id, params.State, params.Stage, params.Progress, params.Message, params.ErrorMessage, params.ArtifactName, params.ArtifactBytes)
	return err
}

func (s *Store) RequestLoraTrainingCancellation(ctx context.Context, publicID string, userID int64, admin bool) (domain.LoraTrainingJob, error) {
	where := "public_id=$1 AND user_id=$2"
	args := []any{publicID, userID}
	if admin {
		where = "public_id=$1"
		args = args[:1]
	}
	query := `UPDATE lora_training_jobs SET
		cancellation_requested_at=COALESCE(cancellation_requested_at,now()),
		state=CASE WHEN state='queued' THEN 'cancelled' ELSE state END,
		stage=CASE WHEN state='queued' THEN 'Отменено' ELSE 'Отменяем обучение' END,
		progress=CASE WHEN state='queued' THEN 100 ELSE progress END,
		message=CASE WHEN state='queued' THEN 'Задание отменено до запуска.' ELSE 'Отправляем отмену агенту.' END,
		finished_at=CASE WHEN state='queued' THEN now() ELSE finished_at END,updated_at=now()
		WHERE ` + where + ` AND state IN ('queued','uploading','preparing','caching','running','installing')
		RETURNING ` + loraTrainingColumns
	return scanLoraTrainingJob(s.db.QueryRowContext(ctx, query, args...))
}

func (s *Store) ClearLoraTrainingDatasetPath(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lora_training_jobs SET dataset_path='',updated_at=now() WHERE id=$1`, id)
	return err
}

func (s *Store) RecoverLoraTrainingJobs(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE lora_training_jobs SET
			state=CASE WHEN cancellation_requested_at IS NULL THEN 'queued' ELSE 'cancelled' END,
			stage=CASE WHEN cancellation_requested_at IS NULL THEN 'В очереди' ELSE 'Отменено' END,
			progress=CASE WHEN cancellation_requested_at IS NULL THEN 0 ELSE 100 END,
			message=CASE
				WHEN cancellation_requested_at IS NULL THEN 'Gateway перезапущен до передачи датасета. Задание поставлено в очередь повторно.'
				ELSE 'Отмена завершена после перезапуска Gateway.'
			END,
			finished_at=CASE WHEN cancellation_requested_at IS NULL THEN finished_at ELSE COALESCE(finished_at,now()) END,
			updated_at=now()
		WHERE state='uploading' AND agent_job_id=''
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) FailedLoraTrainingJobsBefore(ctx context.Context, before time.Time, limit int) ([]domain.LoraTrainingJob, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.listLoraTrainingJobs(ctx, `
		SELECT `+loraTrainingColumns+` FROM lora_training_jobs
		WHERE state='failed' AND COALESCE(finished_at,updated_at) < $1
		ORDER BY COALESCE(finished_at,updated_at),id LIMIT $2
	`, before, limit)
}

func (s *Store) DeleteTerminalLoraTrainingJob(ctx context.Context, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM lora_training_jobs WHERE id=$1 AND state IN ('completed','failed','cancelled')`, id)
	if err != nil {
		return false, err
	}
	deleted, err := result.RowsAffected()
	return deleted == 1, err
}

type loraTrainingScanner interface {
	Scan(...any) error
}

func scanLoraTrainingJob(scanner loraTrainingScanner) (domain.LoraTrainingJob, error) {
	var job domain.LoraTrainingJob
	var state string
	var cancellationRequestedAt, startedAt, finishedAt sql.NullTime
	err := scanner.Scan(
		&job.ID, &job.PublicID, &job.UserID, &job.UsernameSnapshot, &job.RequestID, &job.ProfileID, &job.Family, &job.BaseModel,
		&job.Name, &job.OutputName, &job.TriggerWord, &job.ConceptType, &job.Preset, &job.Resolution, &job.MaxTrainSteps,
		&job.NetworkDim, &job.NetworkAlpha, &job.LearningRate, &job.Seed, &job.SampleCount, &job.DatasetBytes, &job.DatasetPath,
		&state, &job.Stage, &job.Progress, &job.Message, &job.ErrorMessage, &job.AgentJobID, &job.ArtifactName, &job.ArtifactBytes,
		&cancellationRequestedAt, &startedAt, &finishedAt, &job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		return domain.LoraTrainingJob{}, err
	}
	job.State = domain.LoraTrainingState(state)
	if cancellationRequestedAt.Valid {
		job.CancellationRequestedAt = &cancellationRequestedAt.Time
	}
	if startedAt.Valid {
		job.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		job.FinishedAt = &finishedAt.Time
	}
	return job, nil
}
