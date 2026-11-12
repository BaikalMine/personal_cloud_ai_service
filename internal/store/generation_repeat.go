package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"ai-access-gateway/internal/domain"
)

var ErrGenerationRepeatConflict = errors.New("generation repeat confirmation changed")
var ErrGenerationRepeatSource = errors.New("generation repeat source changed or unavailable")

const generationRepeatColumns = `user_id,request_id,source_public_id,source_execution_sha256,confirmation_sha256,job_id,job_public_id,cancelled,created_at`

func scanGenerationRepeat(row generationJobScanner) (domain.GenerationRepeatReceipt, error) {
	var receipt domain.GenerationRepeatReceipt
	err := row.Scan(&receipt.UserID, &receipt.RequestID, &receipt.SourcePublicID, &receipt.SourceExecutionSHA256, &receipt.ConfirmationSHA256, &receipt.JobID, &receipt.JobPublicID, &receipt.Cancelled, &receipt.CreatedAt)
	return receipt, err
}

func (s *Store) GenerationRepeatByRequest(ctx context.Context, userID int64, requestID string) (domain.GenerationRepeatReceipt, error) {
	return scanGenerationRepeat(s.db.QueryRowContext(ctx, `SELECT `+generationRepeatColumns+` FROM generation_repeats WHERE user_id=$1 AND request_id=$2`, userID, requestID))
}

// Keep the request receipt even if retention removes the child job. It must not
// become a new operation when a client retries an uncertain acknowledgement.
func (s *Store) CreateGenerationRepeat(ctx context.Context, p domain.CreateGenerationRepeatParams) (domain.GenerationRepeatReceipt, bool, error) {
	var empty domain.GenerationRepeatReceipt
	validDigest := func(value string, allowEmpty bool) bool {
		if value == "" && allowEmpty {
			return true
		}
		b, err := hex.DecodeString(value)
		return err == nil && len(b) == 32 && strings.ToLower(value) == value
	}
	if p.Job.UserID <= 0 || !gpuTextValid(p.Job.RequestID, 96) || len(p.Job.RequestID) < 16 || !gpuTextValid(p.Job.PublicID, 96) || p.Job.PublicID == p.SourcePublicID || len(p.Prepared.PayloadCipher) == 0 || p.Prepared.Seed < 0 || !validDigest(p.ConfirmationSHA256, false) || !validDigest(p.SourceExecutionSHA256, true) {
		return empty, false, ErrGenerationRepeatConflict
	}
	dependencies, err := json.Marshal(uniqueStrings(p.Prepared.Dependencies))
	if err != nil {
		return empty, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	var disabled, expired, allowed bool
	if err = tx.QueryRowContext(ctx, `SELECT disabled,COALESCE(account_expires_at<=now(),false),can_use_quick_generation FROM users WHERE id=$1 FOR UPDATE`, p.Job.UserID).Scan(&disabled, &expired, &allowed); err != nil {
		return empty, false, err
	}
	if disabled || expired || !allowed {
		return empty, false, ErrQuickGenerationForbidden
	}
	existing, err := scanGenerationRepeat(tx.QueryRowContext(ctx, `SELECT `+generationRepeatColumns+` FROM generation_repeats WHERE user_id=$1 AND request_id=$2`, p.Job.UserID, p.Job.RequestID))
	if err == nil {
		if existing.Cancelled || existing.SourcePublicID != p.SourcePublicID || existing.ConfirmationSHA256 != p.ConfirmationSHA256 || existing.SourceExecutionSHA256 != p.SourceExecutionSHA256 {
			return empty, false, ErrGenerationRepeatConflict
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, false, err
	}
	source, err := scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs WHERE user_id=$1 AND public_id=$2 FOR SHARE`, p.Job.UserID, p.SourcePublicID))
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, ErrGenerationRepeatSource
	}
	if err != nil {
		return empty, false, err
	}
	if !source.State.Terminal() || !bytes.Equal(source.PayloadCipher, p.SourcePayloadCipher) {
		return empty, false, ErrGenerationRepeatSource
	}
	var executionHash string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM generation_job_executions WHERE job_id=$1 FOR SHARE`, source.ID).Scan(&executionHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, false, err
	}
	if executionHash != p.SourceExecutionSHA256 {
		return empty, false, ErrGenerationRepeatSource
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO generation_requests(user_id,request_id,correlation_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.Job.UserID, p.Job.RequestID, p.Job.PublicID)
	if err != nil {
		return empty, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return empty, false, err
	}
	if inserted != 1 {
		return empty, false, ErrGenerationRepeatConflict
	}
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `INSERT INTO generation_jobs
	(public_id,correlation_id,user_id,username_snapshot,request_id,parent_job_id,state,status_message,template_id,workflow_id,model_name,seed,payload_cipher,dependencies,input_count)
	VALUES($1,$1,$2,$3,$4,$5,'waiting_for_resources','Повтор подтверждён; подготавливаем очередь',$6,$7,$8,$9,$10,$11::jsonb,$12) RETURNING `+generationJobColumns,
		p.Job.PublicID, p.Job.UserID, p.Job.UsernameSnapshot, p.Job.RequestID, source.ID, p.Prepared.TemplateID, p.Prepared.WorkflowID, p.Prepared.ModelName, p.Prepared.Seed, p.Prepared.PayloadCipher, dependencies, p.Prepared.InputCount))
	if err != nil {
		return empty, false, err
	}
	if err := bindGenerationInputReferences(ctx, tx, job.ID, p.Job.UserID, p.Prepared.InputNames); err != nil {
		return empty, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_requests SET job_id=$3 WHERE user_id=$1 AND request_id=$2`, p.Job.UserID, p.Job.RequestID, job.ID); err != nil {
		return empty, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO generation_job_transitions(job_id,correlation_id,from_state,to_state,message,attempt) VALUES($1,$2,'','waiting_for_resources',$3,1)`, job.ID, job.CorrelationID, job.StatusMessage); err != nil {
		return empty, false, err
	}
	receipt, err := scanGenerationRepeat(tx.QueryRowContext(ctx, `INSERT INTO generation_repeats(user_id,request_id,source_public_id,source_execution_sha256,confirmation_sha256,job_id,job_public_id)
	VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+generationRepeatColumns, p.Job.UserID, p.Job.RequestID, p.SourcePublicID, p.SourceExecutionSHA256, p.ConfirmationSHA256, job.ID, job.PublicID))
	if err != nil {
		return empty, false, err
	}
	if err = incrementUserNotificationRevision(ctx, tx, p.Job.UserID); err != nil {
		return empty, false, err
	}
	if err = incrementGenerationJobRevision(ctx, tx); err != nil {
		return empty, false, err
	}
	return receipt, true, tx.Commit()
}

// Serialize abandonment with creation, including requests whose HTTP response
// was lost. An absent receipt alone cannot prove that an in-flight send stopped.
func (s *Store) DiscardGenerationRepeatRequest(ctx context.Context, userID int64, requestID, sourceID string) (domain.GenerationRepeatReceipt, error) {
	var empty domain.GenerationRepeatReceipt
	if userID <= 0 || !gpuTextValid(requestID, 96) || len(requestID) < 16 || !gpuTextValid(sourceID, 96) || len(sourceID) < 16 || requestID == sourceID {
		return empty, ErrGenerationRepeatConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&id); err != nil {
		return empty, err
	}
	receipt, err := scanGenerationRepeat(tx.QueryRowContext(ctx, `SELECT `+generationRepeatColumns+` FROM generation_repeats WHERE user_id=$1 AND request_id=$2`, userID, requestID))
	if err == nil {
		if receipt.SourcePublicID != sourceID {
			return empty, ErrGenerationRepeatConflict
		}
		return receipt, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	receipt, err = scanGenerationRepeat(tx.QueryRowContext(ctx, `INSERT INTO generation_repeats(user_id,request_id,source_public_id,source_execution_sha256,confirmation_sha256,job_public_id,cancelled)
	VALUES($1,$2,$3,'',repeat('0',64),'',true) RETURNING `+generationRepeatColumns, userID, requestID, sourceID))
	if err != nil {
		return empty, err
	}
	return receipt, tx.Commit()
}
