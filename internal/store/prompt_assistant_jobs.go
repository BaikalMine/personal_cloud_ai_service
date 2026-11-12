package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
)

var ErrPromptAssistantQuota = errors.New("prompt assistant queue quota exceeded")
var ErrPromptAssistantConflict = errors.New("prompt assistant request identity conflict")

const promptJobTail = `state,status,run_token,cancel_requested,COALESCE(inference_work_id,''),COALESCE(content_event_id,0),created_at,updated_at,available_at,expires_at,model_id`
const promptJobStatusColumns = `id,user_id,mode,request_hash,body_hash,NULL::bytea,metadata_cipher,result_cipher,runtime_cipher,` + promptJobTail

func scanPromptAssistantJob(row datasetScanner) (job domain.PromptAssistantJob, err error) {
	err = row.Scan(&job.ID, &job.UserID, &job.Mode, &job.RequestHash, &job.BodyHash, &job.RequestCipher, &job.MetadataCipher, &job.ResultCipher, &job.RuntimeCipher,
		&job.State, &job.Status, &job.RunToken, &job.CancelRequested, &job.InferenceWorkID, &job.ContentEventID,
		&job.CreatedAt, &job.UpdatedAt, &job.AvailableAt, &job.ExpiresAt, &job.ModelID)
	return
}

// The same right is checked again immediately before executor dispatch.
const promptAssistantOwnerAllowed = `NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>now()) AND
 (u.role='admin' OR (u.can_use_quick_generation AND CASE $2
 WHEN 'text-to-image' THEN u.can_generate_text_to_image WHEN 'image-to-image' THEN u.can_generate_image_to_image
 WHEN 'minimax-h3-video' THEN u.can_generate_video ELSE false END)) AND ` + promptAssistantModelAllowedSQL

func (s *Store) EnqueuePromptAssistantJob(ctx context.Context, input domain.PromptAssistantJob) (domain.PromptAssistantJob, error) {
	for _, hash := range []string{input.RequestHash, input.BodyHash} {
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 {
			return domain.PromptAssistantJob{}, ErrGPUWorkInput
		}
	}
	if len(input.ID) < 16 || len(input.ID) > 64 || !gpuTextValid(input.ID, 64) || input.UserID <= 0 ||
		(input.ModelID != "" && !domain.ValidPromptAssistantModelID(input.ModelID)) ||
		len(input.RequestCipher) == 0 || len(input.RequestCipher) > domain.MaxGPUInferenceRequestBytes+64 ||
		len(input.MetadataCipher) == 0 || len(input.MetadataCipher) > 256<<10 {
		return domain.PromptAssistantJob{}, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PromptAssistantJob{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO gpu_resources(id) VALUES($1) ON CONFLICT DO NOTHING`, domain.GPUPrimaryResource); err != nil {
		return domain.PromptAssistantJob{}, err
	}
	if _, err = scanGPUResource(tx.QueryRowContext(ctx, `SELECT `+gpuResourceColumns+` FROM gpu_resources WHERE id=$1 FOR UPDATE`, domain.GPUPrimaryResource)); err != nil {
		return domain.PromptAssistantJob{}, err
	}
	existing, err := scanPromptAssistantJob(tx.QueryRowContext(ctx, `SELECT `+promptJobStatusColumns+` FROM prompt_assistant_jobs WHERE id=$1 FOR UPDATE`, input.ID))
	if err == nil {
		if existing.UserID != input.UserID || existing.RequestHash != input.RequestHash || existing.ModelID != input.ModelID || !existing.ExpiresAt.After(time.Now()) {
			return domain.PromptAssistantJob{}, ErrPromptAssistantConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return existing, err
	}
	var priority, allowed, reused bool
	if err = tx.QueryRowContext(ctx, `SELECT u.queue_priority,`+promptAssistantOwnerAllowed+` FROM users u WHERE u.id=$1`, input.UserID, input.Mode, input.ModelID).Scan(&priority, &allowed); err != nil || !allowed {
		if err == nil {
			err = ErrPromptAssistantConflict
		}
		return existing, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gpu_work WHERE id=$1)`, input.ID).Scan(&reused); err != nil {
		return existing, err
	}
	if reused {
		return existing, ErrPromptAssistantConflict
	}
	var pending, daily int
	var totalBytes int64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE user_id=$1 AND state IN ('queued','running')),
	 count(*) FILTER(WHERE user_id=$1 AND created_at>now()-interval '24 hours'),COALESCE(sum(octet_length(request_cipher)),0)
	 FROM prompt_assistant_jobs`, input.UserID).Scan(&pending, &daily, &totalBytes); err != nil {
		return existing, err
	}
	if pending >= 4 || daily >= 100 || totalBytes+int64(len(input.RequestCipher)) > 512<<20 {
		return existing, ErrPromptAssistantQuota
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO gpu_work(id,resource_id,kind,job_key,user_id,priority,ready_until)
	 VALUES($1,$2,$3,$4,$5,$6,now()+($7::bigint*interval '1 second'))`, input.ID, domain.GPUPrimaryResource, domain.GPUWorkAssistant,
		domain.PromptAssistantGPUKey(input.ID), input.UserID, priority, int64(domain.GPUIntentLifetime.Seconds())); err != nil {
		return existing, err
	}
	existing, err = scanPromptAssistantJob(tx.QueryRowContext(ctx, `INSERT INTO prompt_assistant_jobs(id,user_id,mode,request_hash,body_hash,request_cipher,metadata_cipher,model_id)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+promptJobStatusColumns, input.ID, input.UserID, input.Mode, input.RequestHash, input.BodyHash, input.RequestCipher, input.MetadataCipher, input.ModelID))
	if err != nil {
		return existing, err
	}
	if err = gpuEvent(ctx, tx, input.ID, domain.GPUWorkWaiting, "enqueued", "prompt assistant accepted"); err != nil {
		return existing, err
	}
	return existing, tx.Commit()
}

func (s *Store) PromptAssistantJob(ctx context.Context, userID int64, id string) (domain.PromptAssistantJob, error) {
	job, err := scanPromptAssistantJob(s.db.QueryRowContext(ctx, `SELECT `+promptJobStatusColumns+` FROM prompt_assistant_jobs WHERE user_id=$1 AND id=$2 AND expires_at>now()`, userID, id))
	if !errors.Is(err, sql.ErrNoRows) {
		return job, err
	}
	// A cancellation can precede source creation. Read its fence after reload too.
	job = domain.PromptAssistantJob{ID: id, UserID: userID, State: "cancelled", Status: "Запрос отменён"}
	err = s.db.QueryRowContext(ctx, `SELECT created_at + interval '24 hours' FROM gpu_work
	 WHERE id=$1 AND user_id=$2 AND kind=$3 AND job_key=$4 AND state='cancelled'
	 AND cancellation_requested=true AND released_at IS NOT NULL AND created_at>now()-interval '24 hours'
	 AND NOT EXISTS (SELECT 1 FROM prompt_assistant_jobs WHERE id=$1)`, id, userID, domain.GPUWorkAssistant, domain.PromptAssistantGPUKey(id)).Scan(&job.ExpiresAt)
	return job, err
}

func (s *Store) WithPromptAssistantWorker(ctx context.Context, work func(*sql.Conn) error) (bool, error) {
	return s.withAssistantWorkerLock(ctx, 732190356, work)
}

// Only call while owning WithPromptAssistantWorker's session lock.
func (s *Store) ClaimPromptAssistantJob(ctx context.Context, token string) (domain.PromptAssistantJob, error) {
	if !gpuLeaseValid(token, time.Minute) {
		return domain.PromptAssistantJob{}, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PromptAssistantJob{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO gpu_resources(id) VALUES($1) ON CONFLICT DO NOTHING`, domain.GPUPrimaryResource); err != nil {
		return domain.PromptAssistantJob{}, err
	}
	if _, err = scanGPUResource(tx.QueryRowContext(ctx, `SELECT `+gpuResourceColumns+` FROM gpu_resources WHERE id=$1 FOR UPDATE`, domain.GPUPrimaryResource)); err != nil {
		return domain.PromptAssistantJob{}, err
	}
	// Polling a running source must not let other accepted queue intents expire.
	if _, err = tx.ExecContext(ctx, `UPDATE gpu_work w SET ready_until=now()+($1::bigint*interval '1 second'),priority=u.queue_priority
	 FROM prompt_assistant_jobs j JOIN users u ON u.id=j.user_id
	 WHERE w.id=j.id AND w.kind=$2 AND w.state='waiting' AND j.state IN ('queued','running')
	 AND NOT j.cancel_requested AND j.expires_at>now() AND NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>now())`, int64(domain.GPUIntentLifetime.Seconds()), domain.GPUWorkAssistant); err != nil {
		return domain.PromptAssistantJob{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE prompt_assistant_jobs SET state='queued',run_token='',available_at=now(),updated_at=now() WHERE state='running'`); err != nil {
		return domain.PromptAssistantJob{}, err
	}
	job, err := scanPromptAssistantJob(tx.QueryRowContext(ctx, `UPDATE prompt_assistant_jobs SET state='running',run_token=$1,updated_at=now()
	 WHERE id=(SELECT j.id FROM prompt_assistant_jobs j JOIN users u ON u.id=j.user_id
	 WHERE j.state='queued' AND j.available_at<=now()
	 ORDER BY (j.cancel_requested OR j.expires_at<=now()) DESC,(j.inference_work_id IS NOT NULL) DESC,(u.queue_priority OR j.created_at<now()-interval '15 minutes') DESC,j.created_at,j.id
	 LIMIT 1 FOR UPDATE OF j SKIP LOCKED) RETURNING `+promptJobStatusColumns, token))
	if errors.Is(err, sql.ErrNoRows) {
		return job, tx.Commit()
	}
	if err != nil {
		return job, err
	}
	return job, tx.Commit()
}

func (s *Store) PostponePromptAssistantJob(ctx context.Context, id, token, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE prompt_assistant_jobs SET state='queued',run_token='',status=$3,available_at=now()+interval '3 seconds',updated_at=now()
	 WHERE id=$1 AND run_token=$2 AND state='running'`, id, token, message)
	return err
}

func (s *Store) PromptAssistantRequest(ctx context.Context, id, token string) ([]byte, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, `SELECT request_cipher FROM prompt_assistant_jobs WHERE id=$1 AND run_token=$2 AND state='running' AND NOT cancel_requested AND inference_work_id IS NULL AND expires_at>now()`, id, token).Scan(&body)
	return body, err
}

func (s *Store) CancelPromptAssistantJob(ctx context.Context, userID int64, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE prompt_assistant_jobs SET cancel_requested=true,status='Отмена описания',available_at=now(),updated_at=now()
	 WHERE id=$1 AND user_id=$2 AND state IN ('queued','running')`, id, userID)
	return err
}

// Cancellation also fences an HTTP submission whose acceptance reply was lost.
// Enqueue takes the same resource lock and rejects this reserved ID thereafter.
func (s *Store) CancelPromptAssistantSubmission(ctx context.Context, userID int64, id string) (domain.PromptAssistantJob, error) {
	var empty domain.PromptAssistantJob
	if len(id) < 16 || len(id) > 64 || !gpuTextValid(id, 64) || userID <= 0 {
		return empty, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO gpu_resources(id) VALUES($1) ON CONFLICT DO NOTHING`, domain.GPUPrimaryResource); err != nil {
		return empty, err
	}
	if _, err = scanGPUResource(tx.QueryRowContext(ctx, `SELECT `+gpuResourceColumns+` FROM gpu_resources WHERE id=$1 FOR UPDATE`, domain.GPUPrimaryResource)); err != nil {
		return empty, err
	}
	work, err := scanGPUWork(tx.QueryRowContext(ctx, `SELECT `+gpuWorkColumns+` FROM gpu_work WHERE id=$1 FOR UPDATE`, id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if err == nil && (work.UserID == nil || *work.UserID != userID || work.Kind != domain.GPUWorkAssistant || work.JobKey != domain.PromptAssistantGPUKey(id)) {
		return empty, ErrPromptAssistantConflict
	}
	job, readErr := scanPromptAssistantJob(tx.QueryRowContext(ctx, `SELECT `+promptJobStatusColumns+` FROM prompt_assistant_jobs WHERE id=$1 FOR UPDATE`, id))
	if readErr == nil {
		if job.UserID != userID {
			return empty, ErrPromptAssistantConflict
		}
		if !job.Terminal() {
			job, readErr = scanPromptAssistantJob(tx.QueryRowContext(ctx, `UPDATE prompt_assistant_jobs SET cancel_requested=true,status='Отмена описания',available_at=now(),updated_at=now() WHERE id=$1 RETURNING `+promptJobStatusColumns, id))
			if readErr == nil && job.InferenceWorkID != "" {
				_, readErr = tx.ExecContext(ctx, `UPDATE gpu_inference_requests SET cancel_requested=true WHERE id=$1 AND state IN ('queued','running','uncertain')`, job.InferenceWorkID)
				if readErr == nil {
					_, readErr = tx.ExecContext(ctx, `UPDATE gpu_work SET cancellation_requested=true,updated_at=now() WHERE id=$1 AND state IN ('held','uncertain')`, job.InferenceWorkID)
				}
			}
		}
		if readErr != nil {
			return empty, readErr
		}
		return job, tx.Commit()
	}
	if !errors.Is(readErr, sql.ErrNoRows) {
		return empty, readErr
	}
	if work.ID != "" {
		if work.State != domain.GPUWorkCancelled || !work.CancellationRequested || !work.CreatedAt.Add(24*time.Hour).After(time.Now()) {
			return empty, sql.ErrNoRows
		}
		return domain.PromptAssistantJob{ID: id, UserID: userID, State: "cancelled", Status: "Запрос отменён", ExpiresAt: work.CreatedAt.Add(24 * time.Hour)}, tx.Commit()
	}
	if work.ID == "" {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM gpu_work WHERE user_id=$1 AND kind=$2 AND created_at>now()-interval '24 hours'`, userID, domain.GPUWorkAssistant).Scan(&count); err != nil {
			return empty, err
		}
		if count >= 100 {
			return empty, ErrPromptAssistantQuota
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO gpu_work(id,resource_id,kind,job_key,user_id,state,cancellation_requested,ready_until,released_at)
		 VALUES($1,$2,$3,$4,$5,'cancelled',true,now(),now())`, id, domain.GPUPrimaryResource, domain.GPUWorkAssistant, domain.PromptAssistantGPUKey(id), userID); err != nil {
			return empty, err
		}
		if err = gpuEvent(ctx, tx, id, domain.GPUWorkCancelled, "assistant_submission_cancelled", "cancelled before source acceptance"); err != nil {
			return empty, err
		}
	}
	return domain.PromptAssistantJob{ID: id, UserID: userID, State: "cancelled", Status: "Запрос отменён", ExpiresAt: time.Now().Add(24 * time.Hour)}, tx.Commit()
}

// The final result and its AI-content/metrics records share the source token.
// A lost commit response can be read back; a stale worker cannot insert duplicates.
func (s *Store) FinishPromptAssistantJob(ctx context.Context, job domain.PromptAssistantJob, state, message string, result []byte, event domain.ContentEventRecord, run domain.PromptAssistantRunRecord) (bool, error) {
	if !gpuLeaseValid(job.RunToken, time.Minute) || (state != "completed" && state != "failed" && state != "cancelled") {
		return false, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	current, err := scanPromptAssistantJob(tx.QueryRowContext(ctx, `SELECT `+promptJobStatusColumns+` FROM prompt_assistant_jobs WHERE id=$1 FOR UPDATE`, job.ID))
	if err != nil {
		return false, err
	}
	if current.RunToken != job.RunToken || current.State != "running" {
		return false, tx.Commit()
	}
	if current.CancelRequested {
		state, message, result = "cancelled", "Описание отменено", nil
	}
	var eventID any
	if state != "cancelled" {
		if event.UserID != current.UserID || event.Kind != "prompt_assistant" || event.CorrelationID != current.ID ||
			(state == "completed" && len(result) == 0) {
			return false, ErrGPUWorkInput
		}
		id, err := insertContentEvent(ctx, tx, event)
		if err != nil {
			return false, err
		}
		run.ContentEventID, run.UserID, run.CorrelationID = id, current.UserID, current.ID
		if _, err = insertPromptAssistantRun(ctx, tx, run); err != nil {
			return false, err
		}
		eventID = id
	}
	if result == nil {
		result = []byte{}
	}
	_, err = tx.ExecContext(ctx, `UPDATE prompt_assistant_jobs SET state=$2,status=$3,result_cipher=$4,content_event_id=$5,request_cipher='',run_token='',updated_at=now() WHERE id=$1`, job.ID, state, message, result, eventID)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) CleanupPromptAssistantJobs(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM prompt_assistant_jobs WHERE id IN (SELECT id FROM prompt_assistant_jobs WHERE expires_at<=now() AND state IN ('completed','failed','cancelled') LIMIT 100 FOR UPDATE SKIP LOCKED)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func activePromptAssistantSource(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	job, err := scanPromptAssistantJob(tx.QueryRowContext(ctx, `SELECT `+promptJobStatusColumns+` FROM prompt_assistant_jobs WHERE inference_work_id=$1 FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if job.Terminal() || job.CancelRequested || !job.ExpiresAt.After(time.Now()) {
		return false, nil
	}
	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT `+promptAssistantOwnerAllowed+` FROM users u WHERE u.id=$1`, job.UserID, job.Mode, job.ModelID).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return allowed, err
}

// An abandoned preparation may be fenced only once the prior resource commands
// finished, and before any request was handed to the model executor.
func (s *Store) FencePromptAssistantPreparation(ctx context.Context, id, token string) (domain.GPUWork, bool, error) {
	if !gpuLeaseValid(token, time.Minute) {
		return domain.GPUWork{}, false, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUWork{}, false, err
	}
	defer tx.Rollback()
	_, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return work, false, err
	}
	source, err := scanPromptAssistantJob(tx.QueryRowContext(ctx, `SELECT `+promptJobStatusColumns+` FROM prompt_assistant_jobs WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return work, false, err
	}
	if source.RunToken != token || source.State != "running" || source.InferenceWorkID != "" ||
		work.Kind != domain.GPUWorkAssistant || work.JobKey != domain.PromptAssistantGPUKey(id) || work.UserID == nil || *work.UserID != source.UserID {
		return work, false, ErrGPUWorkConflict
	}
	if (work.State != domain.GPUWorkHeld && work.State != domain.GPUWorkUncertain) || work.LeaseToken == token || work.ExternalID != "" ||
		(work.Phase != "reserved" && work.Phase != "resources_ready") {
		return work, false, tx.Commit()
	}
	work, err = scanGPUWork(tx.QueryRowContext(ctx, `UPDATE gpu_work SET state='uncertain',lease_until=now(),inference_completion='request_not_dispatched',updated_at=now() WHERE id=$1 RETURNING `+gpuWorkColumns, id))
	if err != nil {
		return work, false, err
	}
	if err = gpuEvent(ctx, tx, id, work.State, "assistant_preparation_fenced", "source worker token replaced before inference acceptance"); err != nil {
		return work, false, err
	}
	if err = invalidateGPUObservation(ctx, tx, work.ResourceID); err != nil {
		return work, false, err
	}
	return work, true, tx.Commit()
}
