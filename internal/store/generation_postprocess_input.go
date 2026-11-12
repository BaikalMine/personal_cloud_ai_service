package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

var ErrPostprocessInput = errors.New("postprocess input identity changed")

const postprocessInputColumns = `a.id,p.job_id,p.source_position,p.dispatch_token,a.filename,a.subfolder,a.size_bytes,a.content_hash,a.state,p.cleanup_blocked`

func scanPostprocessInput(row generationJobScanner) (domain.PostprocessInput, error) {
	var input domain.PostprocessInput
	err := row.Scan(&input.ID, &input.JobID, &input.SourcePosition, &input.DispatchToken, &input.Filename, &input.Subfolder, &input.SizeBytes, &input.SHA256, &input.State, &input.CleanupBlocked)
	return input, err
}

func lockPostprocessDispatch(ctx context.Context, tx *sql.Tx, userID, jobID int64, token string) (domain.GenerationPostprocessReceipt, error) {
	var requestID string
	var payload []byte
	var templateID string
	err := tx.QueryRowContext(ctx, `SELECT request_id,payload_cipher,template_id FROM generation_jobs WHERE id=$1 AND user_id=$2 AND dispatch_token=$3 AND dispatch_until>clock_timestamp()
	 AND state='waiting_for_resources' AND template_id IN ('postprocess-image-v1','postprocess-video-v1') AND workflow_id='postprocess-v1'
	 AND prompt_id IS NULL AND submission_started_at IS NULL AND cancellation_requested_at IS NULL AND dispatch_closed_at IS NULL
	 AND resources_released_at IS NULL AND quota_reserved_on IS NOT NULL FOR UPDATE`, jobID, userID, token).Scan(&requestID, &payload, &templateID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationPostprocessReceipt{}, ErrGenerationJobStateConflict
	}
	if err != nil {
		return domain.GenerationPostprocessReceipt{}, err
	}
	u, err := lockPostprocessUser(ctx, tx, userID)
	if err != nil {
		return domain.GenerationPostprocessReceipt{}, err
	}
	receipt, err := scanPostprocessReceipt(tx.QueryRowContext(ctx, `SELECT `+postprocessReceiptColumns+` FROM generation_postprocess_receipts WHERE user_id=$1 AND request_id=$2 AND state='accepted' AND job_id=$3`, userID, requestID, jobID))
	if err != nil {
		return receipt, err
	}
	if len(receipt.Sources) == 0 || !bytes.Equal(payload, receipt.PayloadCipher) {
		return receipt, ErrGenerationJobStateConflict
	}
	family := domain.PostprocessImageTemplate
	if receipt.Sources[0].MediaType == "video" {
		family = domain.PostprocessVideoTemplate
	}
	if templateID != family {
		return receipt, ErrGenerationJobStateConflict
	}
	currentSensitive, checkErr := checkPostprocessSources(ctx, tx, u, receipt.Sources)
	if checkErr == nil && currentSensitive && !receipt.Sensitive {
		_, checkErr = tx.ExecContext(ctx, `UPDATE generation_postprocess_receipts SET sensitive=true WHERE user_id=$1 AND request_id=$2`, userID, receipt.RequestID)
	}
	receipt.Sensitive = receipt.Sensitive || currentSensitive
	err = checkErr
	if err == nil {
		var live bool
		err = tx.QueryRowContext(ctx, `SELECT dispatch_until>clock_timestamp() FROM generation_jobs WHERE id=$1`, jobID).Scan(&live)
		if err == nil && !live {
			err = ErrGenerationJobStateConflict
		}
	}
	return receipt, err
}

func (s *Store) CheckGenerationPostprocessDispatch(ctx context.Context, userID, jobID int64, token string) (domain.GenerationPostprocessReceipt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationPostprocessReceipt{}, err
	}
	defer tx.Rollback()
	r, err := lockPostprocessDispatch(ctx, tx, userID, jobID, token)
	if err != nil {
		return r, err
	}
	return r, tx.Commit()
}

// Readback is informational. The dispatcher must check its current claim and
// re-read exact remote bytes before it can reuse an earlier attempt's copy.
func (s *Store) GenerationPostprocessInputForJob(ctx context.Context, userID, jobID int64, position int) (domain.PostprocessInput, error) {
	return scanPostprocessInput(s.db.QueryRowContext(ctx, `SELECT `+postprocessInputColumns+` FROM generation_postprocess_inputs p
	 JOIN comfy_input_assets a ON a.id=p.input_id JOIN generation_jobs j ON j.id=p.job_id
	 WHERE j.user_id=$1 AND p.job_id=$2 AND p.source_position=$3
	 ORDER BY (a.state='stored' AND NOT p.cleanup_blocked) DESC,p.created_at DESC,a.id DESC LIMIT 1`, userID, jobID, position))
}

// The shared disk quota and active-job cleanup hold are committed together,
// before any upload. A reused attempt is never permission to POST again.
func (s *Store) ReserveGenerationPostprocessInput(ctx context.Context, userID int64, input domain.PostprocessInput, quota ComfyInputQuota) (domain.PostprocessInput, bool, error) {
	var empty domain.PostprocessInput
	if !postprocessIDValid(input.ID) || !postprocessIDValid(input.DispatchToken) || input.JobID <= 0 || input.SourcePosition < 0 || input.SourcePosition > 1 ||
		input.Filename == "" || path.Base(input.Filename) != input.Filename || strings.ContainsAny(input.Filename, "\\\r\n") ||
		input.Subfolder == "" || input.Subfolder == ".." || strings.HasPrefix(input.Subfolder, "../") || path.Clean(input.Subfolder) != input.Subfolder || strings.HasPrefix(input.Subfolder, "/") || strings.ContainsAny(input.Subfolder, "\\\r\n") {
		return empty, false, ErrPostprocessInput
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	receipt, err := lockPostprocessDispatch(ctx, tx, userID, input.JobID, input.DispatchToken)
	if err != nil {
		return empty, false, err
	}
	if input.SourcePosition >= len(receipt.Sources) {
		return empty, false, ErrPostprocessInput
	}
	source := receipt.Sources[input.SourcePosition]
	if input.SizeBytes != source.SizeBytes || input.SHA256 != source.SHA256 {
		return empty, false, ErrPostprocessInput
	}
	existing, err := scanPostprocessInput(tx.QueryRowContext(ctx, `SELECT `+postprocessInputColumns+` FROM generation_postprocess_inputs p JOIN comfy_input_assets a ON a.id=p.input_id
	 WHERE p.job_id=$1 AND p.dispatch_token=$2 AND p.source_position=$3`, input.JobID, input.DispatchToken, input.SourcePosition))
	if err == nil {
		if existing.ID != input.ID || existing.Filename != input.Filename || existing.Subfolder != input.Subfolder || existing.SizeBytes != input.SizeBytes || existing.SHA256 != input.SHA256 {
			return empty, false, ErrPostprocessInput
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, false, err
	}
	if err = reserveComfyInputAsset(ctx, tx, userID, input.ID, input.Filename, input.Subfolder, input.SizeBytes, input.SHA256, quota); err != nil {
		return empty, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO generation_postprocess_inputs(input_id,job_id,source_position,dispatch_token) VALUES($1,$2,$3,$4)`, input.ID, input.JobID, input.SourcePosition, input.DispatchToken); err != nil {
		return empty, false, err
	}
	input.State, input.CleanupBlocked = "reserved", true
	return input, true, tx.Commit()
}

// Clear the uncertainty hold only after the gateway verified the exact path,
// size and SHA-256. The current claim may adopt a previous attempt's copy.
func (s *Store) VerifyGenerationPostprocessInput(ctx context.Context, userID, jobID int64, token string, input domain.PostprocessInput, retention time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = lockPostprocessDispatch(ctx, tx, userID, jobID, token); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE comfy_input_assets a SET state='stored',updated_at=now(),expires_at=now()+($8::bigint*interval '1 second'),cleanup_retry_at=now(),cleanup_attempts=0,cleanup_last_outcome='pending',cleanup_last_attempt_at=NULL
	 FROM generation_postprocess_inputs p WHERE a.id=p.input_id AND a.id=$1 AND a.user_id=$2 AND p.job_id=$3 AND p.source_position=$4
	 AND a.filename=$5 AND a.subfolder=$6 AND a.content_hash=$7 AND a.size_bytes=$9`, input.ID, userID, jobID, input.SourcePosition, input.Filename, input.Subfolder, input.SHA256, int64(retention.Seconds()), input.SizeBytes)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrPostprocessInput
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_postprocess_inputs SET cleanup_blocked=false WHERE input_id=$1`, input.ID); err != nil {
		return err
	}
	return tx.Commit()
}
