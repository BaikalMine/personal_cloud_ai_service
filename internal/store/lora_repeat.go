package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"ai-access-gateway/internal/domain"
)

var (
	ErrLoraTrainingRepeatConflict = errors.New("LoRA training repeat request changed")
	ErrLoraTrainingRepeatSource   = errors.New("LoRA training repeat source changed or unavailable")
	ErrLoraTrainingForbidden      = errors.New("LoRA training access denied")
)

const loraRepeatColumns = `user_id,request_id,request_sha256,source_job_id,source_public_id,source_name,source_sha256,
 job_id,job_public_id,dataset_snapshot_id,dataset_snapshot_hash,execution_constraint,created_at`

func scanLoraRepeat(row loraTrainingScanner) (domain.LoraTrainingRepeatReceipt, error) {
	var r domain.LoraTrainingRepeatReceipt
	var encoded []byte
	err := row.Scan(&r.UserID, &r.RequestID, &r.RequestSHA256, &r.SourceJobID, &r.SourcePublicID, &r.SourceName, &r.SourceSHA256,
		&r.JobID, &r.JobPublicID, &r.DatasetSnapshotID, &r.DatasetSnapshotHash, &encoded, &r.CreatedAt)
	if err != nil {
		return r, err
	}
	if json.Unmarshal(encoded, &r.Constraint) != nil || r.Constraint == nil || r.Constraint.Validate() != nil || r.Constraint.SourceJobID != r.SourcePublicID {
		return r, domain.ErrLoraTrainingRepeat
	}
	digest, err := domain.LoraTrainingRepeatRequestHash(r.SourceSHA256, r.Constraint)
	if err != nil || digest != r.RequestSHA256 {
		return r, domain.ErrLoraTrainingRepeat
	}
	return r, nil
}

func (s *Store) LoraTrainingRepeatByRequest(ctx context.Context, userID int64, requestID string) (domain.LoraTrainingRepeatReceipt, error) {
	return scanLoraRepeat(s.db.QueryRowContext(ctx, `SELECT `+loraRepeatColumns+` FROM lora_training_repeats WHERE user_id=$1 AND request_id=$2`, userID, requestID))
}

func (s *Store) LoraTrainingRepeatForJob(ctx context.Context, jobID int64) (domain.LoraTrainingRepeatReceipt, error) {
	return scanLoraRepeat(s.db.QueryRowContext(ctx, `SELECT `+loraRepeatColumns+` FROM lora_training_repeats WHERE job_id=$1`, jobID))
}

func (s *Store) UserHasActiveLoraTrainingJob(ctx context.Context, userID int64) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lora_training_jobs WHERE user_id=$1 AND state NOT IN ('completed','failed','cancelled'))`, userID).Scan(&active)
	return active, err
}

func (s *Store) CreateLoraTrainingRepeat(ctx context.Context, params domain.CreateLoraTrainingRepeatParams) (domain.LoraTrainingRepeatReceipt, bool, error) {
	var empty domain.LoraTrainingRepeatReceipt
	jobParams := params.Job
	digest, err := domain.LoraTrainingRepeatRequestHash(params.SourceSHA256, params.Constraint)
	if err != nil || params.Constraint.SourceJobID != params.SourceJobID || jobParams.UserID <= 0 ||
		!gpuTextValid(jobParams.RequestID, 96) || len(jobParams.RequestID) < 16 || jobParams.PublicID == params.SourceJobID || jobParams.DatasetBytes <= 0 || jobParams.DatasetPath == "" {
		return empty, false, domain.ErrLoraTrainingRepeat
	}
	encoded, err := json.Marshal(params.Constraint)
	if err != nil {
		return empty, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	var role string
	var disabled, expired, training bool
	if err := tx.QueryRowContext(ctx, `SELECT role,disabled,COALESCE(account_expires_at<=now(),false),can_train_image_lora
 FROM users WHERE id=$1 FOR UPDATE`, jobParams.UserID).Scan(&role, &disabled, &expired, &training); err != nil {
		return empty, false, err
	}
	if disabled || expired || role != "admin" && !training {
		return empty, false, ErrLoraTrainingForbidden
	}
	existing, err := scanLoraRepeat(tx.QueryRowContext(ctx, `SELECT `+loraRepeatColumns+` FROM lora_training_repeats WHERE user_id=$1 AND request_id=$2`, jobParams.UserID, jobParams.RequestID))
	if err == nil {
		if existing.SourcePublicID != params.SourceJobID || existing.RequestSHA256 != digest {
			return empty, false, ErrLoraTrainingRepeatConflict
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, false, err
	}
	source, err := scanLoraTrainingJob(tx.QueryRowContext(ctx, `SELECT `+loraTrainingColumns+` FROM lora_training_jobs WHERE public_id=$1 AND user_id=$2 FOR SHARE`, params.SourceJobID, jobParams.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, ErrLoraTrainingRepeatSource
	}
	if err != nil {
		return empty, false, err
	}
	if domain.LoraTrainingRepeatSourceHash(source) != params.SourceSHA256 || !domain.LoraTrainingRepeatMatchesSource(source, params.Constraint, jobParams) {
		return empty, false, ErrLoraTrainingRepeatSource
	}
	var snapshot string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM lora_dataset_snapshots WHERE id=$1 AND user_id=$2 AND manifest_hash=$3 FOR SHARE`,
		source.DatasetSnapshotID, jobParams.UserID, source.DatasetSnapshotHash).Scan(&snapshot); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return empty, false, ErrLoraTrainingRepeatSource
		}
		return empty, false, err
	}
	job, err := insertLoraTrainingJob(ctx, tx, jobParams)
	if err != nil {
		return empty, false, err
	}
	receipt, err := scanLoraRepeat(tx.QueryRowContext(ctx, `INSERT INTO lora_training_repeats
 (user_id,request_id,request_sha256,source_job_id,source_public_id,source_name,source_sha256,job_id,job_public_id,dataset_snapshot_id,dataset_snapshot_hash,execution_constraint)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING `+loraRepeatColumns,
		jobParams.UserID, jobParams.RequestID, digest, source.ID, source.PublicID, source.Name, params.SourceSHA256, job.ID, job.PublicID, snapshot, source.DatasetSnapshotHash, encoded))
	if err != nil {
		return empty, false, err
	}
	return receipt, true, tx.Commit()
}
