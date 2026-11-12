package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
)

const inferenceTailColumns = `timeout_ms,state,run_token,executor_id,executor_generation,response_status,response_cipher,completion_code,cancel_requested,created_at,started_at,finished_at,result_expires_at`
const inferenceColumns = `id,lease_token,request_hash,request_cipher,` + inferenceTailColumns
const inferenceStatusColumns = `id,lease_token,request_hash,NULL::bytea,` + inferenceTailColumns

func scanInference(row datasetScanner) (job domain.GPUInference, err error) {
	err = row.Scan(&job.ID, &job.LeaseToken, &job.RequestHash, &job.RequestCipher, &job.TimeoutMS,
		&job.State, &job.RunToken, &job.ExecutorID, &job.ExecutorGeneration, &job.ResponseStatus, &job.ResponseCipher, &job.CompletionCode,
		&job.CancelRequested, &job.CreatedAt, &job.StartedAt, &job.FinishedAt, &job.ResultExpiresAt)
	return
}

func (s *Store) GPUInferenceSettled(ctx context.Context, id string) (bool, error) {
	var job domain.GPUInference
	err := s.db.QueryRowContext(ctx, `SELECT state FROM gpu_inference_requests WHERE id=$1`, id).Scan(&job.State)
	return err == nil && job.Settled(), err
}

func (s *Store) GPUInference(ctx context.Context, id string) (domain.GPUInference, error) {
	return scanInference(s.db.QueryRowContext(ctx, `SELECT `+inferenceStatusColumns+` FROM gpu_inference_requests WHERE id=$1`, id))
}

// Acceptance and transfer of the GPU permit commit together. A lost commit
// response is resolved by lookup of this ID, never by choosing a new ID.
func (s *Store) SubmitGPUInference(ctx context.Context, input domain.GPUInference) (domain.GPUInference, error) {
	return s.submitGPUInference(ctx, input, "", "", nil, "", "")
}

func (s *Store) SubmitCaptionGPUInference(ctx context.Context, input domain.GPUInference, captionID, captionToken string, metadata []byte) (domain.GPUInference, error) {
	if captionID == "" || captionToken == "" || len(metadata) == 0 || len(metadata) > 64<<10 {
		return domain.GPUInference{}, ErrGPUWorkInput
	}
	return s.submitGPUInference(ctx, input, captionID, captionToken, metadata, "", "")
}

func (s *Store) SubmitAssistantGPUInference(ctx context.Context, input domain.GPUInference, id, token string, metadata []byte) (domain.GPUInference, error) {
	if id == "" || token == "" || len(metadata) == 0 || len(metadata) > 64<<10 {
		return domain.GPUInference{}, ErrGPUWorkInput
	}
	return s.submitGPUInference(ctx, input, "", "", metadata, id, token)
}

func (s *Store) submitGPUInference(ctx context.Context, input domain.GPUInference, captionID, captionToken string, metadata []byte, assistantID, assistantToken string) (domain.GPUInference, error) {
	hash, err := hex.DecodeString(input.RequestHash)
	if err != nil || len(hash) != 32 || len(input.RequestCipher) == 0 || len(input.RequestCipher) > domain.MaxGPUInferenceRequestBytes+64 ||
		input.TimeoutMS < 1000 || input.TimeoutMS > 3600000 || !gpuLeaseValid(input.LeaseToken, time.Minute) {
		return domain.GPUInference{}, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUInference{}, err
	}
	defer tx.Rollback()
	resource, work, err := lockGPUWork(ctx, tx, input.ID)
	if err != nil {
		return domain.GPUInference{}, err
	}
	if assistantID != "" {
		source, err := scanPromptAssistantJob(tx.QueryRowContext(ctx, `SELECT `+promptJobStatusColumns+` FROM prompt_assistant_jobs WHERE id=$1 FOR UPDATE`, assistantID))
		if err != nil {
			return domain.GPUInference{}, err
		}
		if work.Kind != domain.GPUWorkAssistant || work.ID != source.ID || work.JobKey != domain.PromptAssistantGPUKey(source.ID) ||
			work.UserID == nil || *work.UserID != source.UserID || source.RunToken != assistantToken || source.State != "running" || source.CancelRequested ||
			source.BodyHash != input.RequestHash || !source.ExpiresAt.After(time.Now()) {
			return domain.GPUInference{}, ErrGPUWorkConflict
		}
	} else if work.Kind == domain.GPUWorkAssistant {
		var managed bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM prompt_assistant_jobs WHERE id=$1)`, work.ID).Scan(&managed); err != nil {
			return domain.GPUInference{}, err
		}
		if managed {
			return domain.GPUInference{}, ErrGPUWorkInput
		}
	}
	if captionID != "" {
		caption, err := scanCaption(tx.QueryRowContext(ctx, `SELECT `+captionColumns+` FROM lora_caption_jobs WHERE id=$1 FOR UPDATE`, captionID))
		if err != nil {
			return domain.GPUInference{}, err
		}
		if work.Kind != domain.GPUWorkCaption || work.UserID == nil || caption.UserID != *work.UserID || caption.RunToken != captionToken || caption.State != "running" || caption.CancelRequested ||
			(caption.InferenceWorkID != "" && caption.InferenceWorkID != work.ID) {
			return domain.GPUInference{}, ErrGPUWorkConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE lora_caption_jobs SET inference_work_id=$2,inference_metadata_cipher=$3 WHERE id=$1 AND inference_work_id IS NULL`, captionID, work.ID, metadata); err != nil {
			return domain.GPUInference{}, err
		}
	} else if work.Kind == domain.GPUWorkCaption {
		return domain.GPUInference{}, ErrGPUWorkInput
	}
	existing, err := scanInference(tx.QueryRowContext(ctx, `SELECT `+inferenceStatusColumns+` FROM gpu_inference_requests WHERE id=$1 FOR UPDATE`, input.ID))
	if err == nil {
		if existing.RequestHash != input.RequestHash || existing.LeaseToken != input.LeaseToken || existing.TimeoutMS != input.TimeoutMS {
			return existing, ErrGPUWorkConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return existing, err
	}
	if (work.Kind != domain.GPUWorkAssistant && work.Kind != domain.GPUWorkCaption) || work.State != domain.GPUWorkHeld ||
		work.LeaseToken != input.LeaseToken || work.CancellationRequested || work.InferenceCompletion != "" || work.ExternalID != "" ||
		(work.Phase != "reserved" && work.Phase != "resources_ready") {
		return existing, ErrGPUWorkConflict
	}
	var id string
	err = tx.QueryRowContext(ctx, `UPDATE gpu_work SET phase='dispatching',external_id=$3,updated_at=now()
		WHERE id=$1 AND lease_token=$2 AND lease_until>clock_timestamp() RETURNING id`, work.ID, input.LeaseToken, domain.GPUInferenceExternalID(work.ID)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrGPUWorkConflict
	}
	if err != nil {
		return existing, err
	}
	existing, err = scanInference(tx.QueryRowContext(ctx, `INSERT INTO gpu_inference_requests(id,lease_token,request_hash,request_cipher,timeout_ms)
		VALUES($1,$2,$3,$4,$5) RETURNING `+inferenceStatusColumns, input.ID, input.LeaseToken, input.RequestHash, input.RequestCipher, input.TimeoutMS))
	if err != nil {
		return existing, err
	}
	if assistantID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE prompt_assistant_jobs SET inference_work_id=$2,request_cipher='',runtime_cipher=$3,updated_at=now() WHERE id=$1`, assistantID, input.ID, metadata); err != nil {
			return existing, err
		}
	}
	if err = gpuEvent(ctx, tx, work.ID, work.State, "inference_accepted", domain.GPUInferenceExternalID(work.ID)); err != nil {
		return existing, err
	}
	if err = invalidateGPUObservation(ctx, tx, resource.ID); err != nil {
		return existing, err
	}
	return existing, tx.Commit()
}

// The running marker is permanent. No timeout or worker restart returns it to
// queued: the upstream may still be executing after the connection disappears.
func (s *Store) ClaimGPUInference(ctx context.Context, token string) (domain.GPUInference, error) {
	return s.claimGPUInference(ctx, token, domain.GPUInferenceExecutor{})
}

func (s *Store) ClaimOwnedGPUInference(ctx context.Context, token string, executor domain.GPUInferenceExecutor) (domain.GPUInference, error) {
	if !validInferenceExecutor(executor) {
		return domain.GPUInference{}, ErrGPUWorkInput
	}
	return s.claimGPUInference(ctx, token, executor)
}

func (s *Store) claimGPUInference(ctx context.Context, token string, executor domain.GPUInferenceExecutor) (domain.GPUInference, error) {
	if !gpuLeaseValid(token, time.Minute) {
		return domain.GPUInference{}, ErrGPUWorkInput
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM gpu_inference_requests WHERE state='queued' ORDER BY created_at,id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GPUInference{}, nil
	}
	if err != nil {
		return domain.GPUInference{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUInference{}, err
	}
	defer tx.Rollback()
	_, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return domain.GPUInference{}, err
	}
	if executor.ID != "" {
		if err = checkInferenceExecutor(ctx, tx, executor); err != nil {
			return domain.GPUInference{}, err
		}
	}
	job, err := scanInference(tx.QueryRowContext(ctx, `SELECT `+inferenceStatusColumns+` FROM gpu_inference_requests WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return job, err
	}
	if job.State != "queued" {
		return domain.GPUInference{}, tx.Commit()
	}
	if (work.State != domain.GPUWorkHeld && work.State != domain.GPUWorkUncertain) || work.LeaseToken != job.LeaseToken ||
		work.ExternalID != domain.GPUInferenceExternalID(id) || work.Phase != "dispatching" || work.InferenceCompletion != "" {
		return job, ErrGPUWorkConflict
	}
	var ownerActive, expired bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND NOT disabled AND
		(account_expires_at IS NULL OR account_expires_at>now())), $2::timestamptz<now()-interval '24 hours'`, work.UserID, job.CreatedAt).Scan(&ownerActive, &expired)
	if err != nil {
		return job, err
	}
	if work.Kind == domain.GPUWorkCaption {
		var allowed bool
		err = tx.QueryRowContext(ctx, `SELECT j.state IN ('queued','running') AND NOT j.cancel_requested
			AND (u.role='admin' OR u.can_train_image_lora) FROM lora_caption_jobs j JOIN users u ON u.id=j.user_id
			WHERE j.inference_work_id=$1 FOR UPDATE OF j`, id).Scan(&allowed)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
			allowed = false
		}
		if err != nil {
			return job, err
		}
		ownerActive = ownerActive && allowed
	}
	if work.Kind == domain.GPUWorkAssistant && work.JobKey == domain.PromptAssistantGPUKey(work.ID) {
		allowed, err := activePromptAssistantSource(ctx, tx, id)
		if err != nil {
			return job, err
		}
		ownerActive = ownerActive && allowed
	}
	if job.CancelRequested || work.CancellationRequested || !ownerActive || expired {
		job, err = finishQueuedInference(ctx, tx, id)
		if err != nil {
			return job, err
		}
		return job, tx.Commit()
	}
	job, err = scanInference(tx.QueryRowContext(ctx, `UPDATE gpu_inference_requests SET state='running',run_token=$2,started_at=now(),executor_id=$3,executor_generation=$4
		WHERE id=$1 RETURNING `+inferenceColumns, id, token, executor.ID, executor.Generation))
	if err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gpu_work SET phase='running',updated_at=now() WHERE id=$1`, id); err != nil {
		return job, err
	}
	if err = gpuEvent(ctx, tx, id, work.State, "inference_dispatching", token); err != nil {
		return job, err
	}
	return job, tx.Commit()
}

func finishQueuedInference(ctx context.Context, tx *sql.Tx, id string) (domain.GPUInference, error) {
	job, err := scanInference(tx.QueryRowContext(ctx, `UPDATE gpu_inference_requests SET state='not_dispatched',request_cipher=NULL,
		completion_code='request_not_dispatched',finished_at=now(),result_expires_at=now()+interval '24 hours'
		WHERE id=$1 AND state='queued' RETURNING `+inferenceColumns, id))
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE gpu_work SET inference_completion='request_not_dispatched',updated_at=now() WHERE id=$1`, id)
	}
	return job, err
}

func (s *Store) CancelGPUInference(ctx context.Context, id string) (domain.GPUInference, error) {
	return s.cancelGPUInference(ctx, id, "")
}

// Gateway can fence a queued caption without waiting for an offline executor.
// A running request still needs executor evidence before releasing the GPU.
func (s *Store) CancelInactiveCaptionInference(ctx context.Context, id string) (domain.GPUInference, error) {
	return s.cancelGPUInference(ctx, id, domain.GPUWorkCaption)
}

func (s *Store) CancelInactiveAssistantInference(ctx context.Context, id string) (domain.GPUInference, error) {
	return s.cancelGPUInference(ctx, id, domain.GPUWorkAssistant)
}

func (s *Store) cancelGPUInference(ctx context.Context, id string, onlyInactive domain.GPUWorkKind) (domain.GPUInference, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUInference{}, err
	}
	defer tx.Rollback()
	_, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return domain.GPUInference{}, err
	}
	if onlyInactive != "" {
		if work.Kind != onlyInactive {
			return domain.GPUInference{}, ErrGPUWorkInput
		}
		var active bool
		if onlyInactive == domain.GPUWorkAssistant {
			active, err = activePromptAssistantSource(ctx, tx, id)
		} else {
			err = tx.QueryRowContext(ctx, `SELECT j.state IN ('queued','running') AND NOT j.cancel_requested AND NOT u.disabled
			AND (u.account_expires_at IS NULL OR u.account_expires_at>now()) AND (u.role='admin' OR u.can_train_image_lora)
			FROM lora_caption_jobs j JOIN users u ON u.id=j.user_id WHERE j.inference_work_id=$1 FOR UPDATE OF j`, id).Scan(&active)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return domain.GPUInference{}, err
		}
		if active {
			job, err := scanInference(tx.QueryRowContext(ctx, `SELECT `+inferenceStatusColumns+` FROM gpu_inference_requests WHERE id=$1`, id))
			if err != nil {
				return job, err
			}
			return job, tx.Commit()
		}
	}
	job, err := scanInference(tx.QueryRowContext(ctx, `UPDATE gpu_inference_requests SET cancel_requested=true WHERE id=$1 RETURNING `+inferenceStatusColumns, id))
	if err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gpu_work SET cancellation_requested=true,updated_at=now() WHERE id=$1 AND state IN ('held','uncertain')`, id); err != nil {
		return job, err
	}
	if job.State == "queued" {
		job, err = finishQueuedInference(ctx, tx, id)
	}
	if err != nil {
		return job, err
	}
	return job, tx.Commit()
}

// Only the executor that permanently claimed this request can publish evidence.
// Result and GPU completion are one commit, including retries after a DB outage.
func (s *Store) FinishGPUInference(ctx context.Context, id, token, code string, status int, response []byte) error {
	return s.finishGPUInference(ctx, id, token, code, status, response, "", "", time.Time{}, nil)
}

// Replaying an observed outcome preserves its original retention deadline and
// checks all durable identities. It never claims or dispatches model work.
func (s *Store) RestoreGPUInferenceOutcome(ctx context.Context, id, token, lease, hash, code string, status int, response []byte, observedAt time.Time) error {
	if lease == "" || len(hash) != 64 || observedAt.IsZero() || observedAt.After(time.Now().Add(time.Minute)) || code == "" {
		return ErrGPUWorkInput
	}
	return s.finishGPUInference(ctx, id, token, code, status, response, lease, hash, observedAt, nil)
}

func (s *Store) finishGPUInference(ctx context.Context, id, token, code string, status int, response []byte, lease, hash string, observedAt time.Time, recovery *domain.GPUInferenceExecutor) error {
	expiresAt := observedAt.Add(24 * time.Hour)
	expiredReceipt := !observedAt.IsZero() && !expiresAt.After(time.Now())
	if (code != "" && code != "executor_terminal" && code != "request_not_dispatched" && code != "executor_stopped") ||
		(status != 0 && (status < 100 || status > 599)) || len(response) > domain.MaxGPUInferenceResponseBytes+64 ||
		(code == "executor_terminal" && (status < 200 || status >= 300 || (len(response) == 0 && !expiredReceipt))) ||
		(code == "executor_stopped" && (status != 0 || len(response) != 0)) {
		return ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return err
	}
	if recovery != nil {
		if err = checkInferenceExecutor(ctx, tx, *recovery); err != nil {
			return err
		}
	}
	job, err := scanInference(tx.QueryRowContext(ctx, `SELECT `+inferenceStatusColumns+` FROM gpu_inference_requests WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if code == "executor_stopped" && job.ExecutorID == "" {
		return ErrGPUWorkConflict
	}
	if recovery != nil {
		if job.ExecutorID != recovery.ID || job.ExecutorGeneration >= recovery.Generation || code != "executor_stopped" {
			return ErrGPUWorkConflict
		}
		if job.Settled() {
			return tx.Commit()
		}
	}
	if token == "" || job.RunToken != token {
		return ErrGPUWorkConflict
	}
	if !observedAt.IsZero() && (job.LeaseToken != lease || job.RequestHash != hash || job.StartedAt == nil || observedAt.Before(*job.StartedAt)) {
		return ErrGPUWorkConflict
	}
	if job.Settled() {
		if job.CompletionCode != code {
			return ErrGPUWorkConflict
		}
		return tx.Commit()
	}
	if (job.State != "running" && job.State != "uncertain") || work.ExternalID != domain.GPUInferenceExternalID(id) || work.LeaseToken != job.LeaseToken ||
		(work.State != domain.GPUWorkHeld && work.State != domain.GPUWorkUncertain) {
		return ErrGPUWorkConflict
	}
	state := "uncertain"
	if code == "executor_terminal" {
		state = "completed"
	} else if code == "request_not_dispatched" {
		state = "not_dispatched"
	} else if code == "executor_stopped" {
		state = "stopped"
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
		expiresAt = observedAt.Add(24 * time.Hour)
	}
	if expiredReceipt {
		response = nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE gpu_inference_requests SET state=$3,response_status=$4,response_cipher=$5,completion_code=$6,
		request_cipher=NULL,finished_at=$7,result_expires_at=$8 WHERE id=$1 AND run_token=$2`, id, token, state, status, response, code, observedAt, expiresAt)
	if err != nil {
		return err
	}
	if code != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE gpu_work SET inference_completion=$2,updated_at=now() WHERE id=$1`, id, code); err != nil {
			return err
		}
	}
	if err = gpuEvent(ctx, tx, id, work.State, "inference_"+state, code); err != nil {
		return err
	}
	return tx.Commit()
}

// Keep the small identity/dispatch tombstone to reject late duplicate sends.
// Unknown execution does not justify retaining prompt or reference bytes forever.
func (s *Store) RedactExpiredGPUInference(ctx context.Context) (int64, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gpu_inference_progress WHERE id IN
	 (SELECT p.id FROM gpu_inference_progress p JOIN gpu_inference_requests r ON r.id=p.id
	 WHERE p.expires_at<=now() OR r.state IN ('stopped','not_dispatched') LIMIT 100)`); err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE gpu_inference_requests SET request_cipher=NULL,response_cipher=NULL
		WHERE id IN (SELECT id FROM gpu_inference_requests WHERE state<>'queued' AND
		(COALESCE(result_expires_at,created_at+interval '24 hours')<=now()) AND
		(request_cipher IS NOT NULL OR response_cipher IS NOT NULL) ORDER BY created_at,id LIMIT 100 FOR UPDATE SKIP LOCKED)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
