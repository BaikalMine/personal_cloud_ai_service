package gateway

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func postprocessPublicationFailure(err error) (string, string) {
	switch {
	case errors.Is(err, store.ErrQuickGenerationForbidden):
		return "postprocess_publish_forbidden", "Доступ к постобработке закрыт"
	case errors.Is(err, store.ErrQuickGenerationDailyLimit), errors.Is(err, store.ErrQuickGenerationTotalLimit):
		return "postprocess_publish_quota", "Недостаточно квоты для постобработки"
	case errors.Is(err, store.ErrPostprocessSource):
		return "postprocess_publish_source", "Исходный файл постобработки больше недоступен"
	case errors.Is(err, store.ErrPostprocessConflict), errors.Is(err, errPostprocessQuoteInvalid):
		return "postprocess_publish_invalid", "Подтверждение постобработки больше не соответствует заданию"
	default:
		return "", ""
	}
}

func (a *App) finishPostprocessPublicationFailure(ctx context.Context, job domain.GenerationJob) (domain.GenerationJob, error) {
	released, complete, err := a.releaseGenerationJobResources(ctx, job)
	if err != nil || !complete {
		// Preserve the durable failure code while external resource release is
		// pending; an ordinary same-state transition would clear that code.
		return released, err
	}
	updated, _, err := a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobFailed, Message: job.StatusMessage, ErrorCode: job.ErrorCode,
	})
	if errors.Is(err, store.ErrGenerationJobStateConflict) {
		current, readErr := a.store.GenerationJobByID(ctx, job.ID)
		if readErr == nil && current.State.Terminal() {
			return current, nil
		}
	}
	return updated, err
}

// Only explicit durable acceptance enters this path. GET status/discard never
// call it; the background reconciler continues it without a browser connection.
func (a *App) resumePostprocessPublication(ctx context.Context, userID int64, requestID string) (domain.GenerationJob, error) {
	var empty domain.GenerationJob
	r, err := a.store.GenerationPostprocessByRequest(ctx, userID, requestID)
	if err != nil {
		return empty, err
	}
	if r.State != "accepted" {
		return empty, store.ErrPostprocessConflict
	}
	if r.JobID == nil {
		return empty, nil
	}
	job, err := a.store.GenerationJobByPublicID(ctx, userID, r.JobPublicID)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	if job.ID != *r.JobID || !isPostprocessJob(job) || job.RequestID != r.RequestID {
		return empty, store.ErrPostprocessConflict
	}
	if job.State.Terminal() || job.PromptID != "" || job.SubmissionStartedAt != nil || job.DispatchQueuedAt != nil || job.CancellationRequestedAt != nil {
		return job, nil
	}
	if job.DispatchClosedAt != nil {
		if strings.HasPrefix(job.ErrorCode, "postprocess_publish_") {
			return a.finishPostprocessPublicationFailure(ctx, job)
		}
		return job, nil
	}
	// Verify owner-bound encrypted intent again after a restart, before charging.
	_, err = a.decodePostprocessQuote(r)
	if err == nil {
		var published domain.GenerationJob
		published, _, err = a.store.PublishGenerationPostprocess(ctx, userID, requestID)
		if err == nil {
			return published, nil
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return empty, nil
	}
	code, message := postprocessPublicationFailure(err)
	if code == "" {
		return job, err
	}
	closed, _, closeErr := a.store.FencePostprocessPublicationFailure(ctx, job.ID, userID, requestID, code, message)
	if closeErr != nil {
		return job, closeErr
	}
	if closed.DispatchClosedAt != nil && strings.HasPrefix(closed.ErrorCode, "postprocess_publish_") && !closed.State.Terminal() && closed.CancellationRequestedAt == nil {
		return a.finishPostprocessPublicationFailure(ctx, closed)
	}
	return closed, nil
}
