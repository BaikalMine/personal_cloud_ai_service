package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"

	"ai-access-gateway/internal/domain"
)

// PublishGenerationPostprocess consumes durable consent, not a new client plan.
// Quota and dispatch publication commit together; retrying never creates a child.
// A deleted child returns sql.ErrNoRows; its receipt remains the recovery fence.
func (s *Store) PublishGenerationPostprocess(ctx context.Context, userID int64, requestID string) (domain.GenerationJob, bool, error) {
	var empty domain.GenerationJob
	if userID <= 0 || !postprocessIDValid(requestID) {
		return empty, false, ErrPostprocessConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	r, err := scanPostprocessReceipt(tx.QueryRowContext(ctx, `SELECT `+postprocessReceiptColumns+` FROM generation_postprocess_receipts WHERE user_id=$1 AND request_id=$2`, userID, requestID))
	if err != nil {
		return empty, false, err
	}
	if r.State != "accepted" || r.Version != 1 || !postprocessDigestValid(r.ConfirmationSHA256) || !postprocessSourcesValid(r.Sources) {
		return empty, false, ErrPostprocessConflict
	}
	if r.JobID == nil {
		return empty, false, sql.ErrNoRows
	}
	// Follow the shared quota/cancellation lock order: child before owner.
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs WHERE id=$1 AND user_id=$2 FOR UPDATE`, *r.JobID, userID))
	if err != nil {
		return empty, false, err
	}
	templateID := domain.PostprocessImageTemplate
	if r.Sources[0].MediaType == "video" {
		templateID = domain.PostprocessVideoTemplate
	}
	if job.PublicID != r.JobPublicID || job.RequestID != requestID || job.CorrelationID != job.PublicID || job.TemplateID != templateID || job.WorkflowID != domain.PostprocessWorkflow || job.BatchID != nil || len(r.PayloadCipher) == 0 || !bytes.Equal(job.PayloadCipher, r.PayloadCipher) {
		return empty, false, ErrPostprocessConflict
	}
	var bound bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM generation_requests WHERE user_id=$1 AND request_id=$2 AND job_id=$3 AND correlation_id=$4)`, userID, requestID, job.ID, job.PublicID).Scan(&bound); err != nil {
		return empty, false, err
	}
	if !bound {
		return empty, false, ErrPostprocessConflict
	}
	if job.State != domain.GenerationJobWaitingForResources || job.DispatchQueuedAt != nil || job.DispatchClosedAt != nil || job.ResourcesReleasedAt != nil || job.CancellationRequestedAt != nil || job.SubmissionStartedAt != nil || job.PromptID != "" || job.QuotaCommittedAt != nil {
		return job, false, tx.Commit()
	}
	u, err := lockPostprocessUser(ctx, tx, userID)
	if err != nil {
		return empty, false, err
	}
	sensitive, err := checkPostprocessSources(ctx, tx, u, r.Sources)
	if err != nil {
		return empty, false, err
	}
	if sensitive && !r.Sensitive {
		if _, err = tx.ExecContext(ctx, `UPDATE generation_postprocess_receipts SET sensitive=true WHERE user_id=$1 AND request_id=$2 AND state='accepted'`, userID, requestID); err != nil {
			return empty, false, err
		}
	}
	if _, _, err = reserveQuickGenerationForJob(ctx, tx, job.ID, userID); err != nil {
		return empty, false, err
	}
	job, err = queueGenerationJobDispatch(ctx, tx, job.ID)
	if err != nil {
		return empty, false, err
	}
	if err = tx.Commit(); err != nil {
		return empty, false, err
	}
	return job, true, nil
}

// Persist the failure fence before invoking the common resource-release path.
// A stale rejection cannot close a child that another publisher already queued.
func (s *Store) FencePostprocessPublicationFailure(ctx context.Context, jobID, userID int64, requestID, code, message string) (domain.GenerationJob, bool, error) {
	var empty domain.GenerationJob
	if jobID <= 0 || userID <= 0 || !postprocessIDValid(requestID) || !strings.HasPrefix(code, "postprocess_publish_") || !gpuTextValid(code, 96) || strings.TrimSpace(message) == "" || len(message) > 1024 {
		return empty, false, ErrPostprocessConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `UPDATE generation_jobs SET
	 dispatch_closed_at=clock_timestamp(),error_code=$4,error_message='',status_message=$5,updated_at=now()
	 WHERE id=$1 AND user_id=$2 AND request_id=$3
	 AND template_id IN ($6,$7) AND batch_id IS NULL AND state='waiting_for_resources'
	 AND dispatch_queued_at IS NULL AND dispatch_closed_at IS NULL AND dispatch_token=''
	 AND submission_started_at IS NULL AND prompt_id IS NULL AND cancellation_requested_at IS NULL
	 AND quota_committed_at IS NULL AND resources_released_at IS NULL
	 AND EXISTS(SELECT 1 FROM generation_postprocess_receipts r WHERE r.user_id=$2 AND r.request_id=$3 AND r.job_id=$1 AND r.state='accepted' AND r.job_public_id=generation_jobs.public_id)
	 RETURNING `+generationJobColumns, jobID, userID, requestID, code, message, domain.PostprocessImageTemplate, domain.PostprocessVideoTemplate))
	if errors.Is(err, sql.ErrNoRows) {
		job, err = scanGenerationJob(tx.QueryRowContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs WHERE id=$1 AND user_id=$2 AND request_id=$3`, jobID, userID, requestID))
		if err != nil {
			return empty, false, err
		}
		return job, false, tx.Commit()
	}
	if err != nil {
		return empty, false, err
	}
	if err = incrementGenerationJobRevision(ctx, tx); err != nil {
		return empty, false, err
	}
	return job, true, tx.Commit()
}
