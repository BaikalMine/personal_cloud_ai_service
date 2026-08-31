package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

var errGenerationCancellationNotSent = errors.New("generation cancellation was not sent")

func generationJobClientState(job domain.GenerationJob) string {
	if job.CancellationRequestedAt != nil && !job.State.Terminal() {
		return "cancelling"
	}
	switch job.State {
	case domain.GenerationJobDraft, domain.GenerationJobPreparing, domain.GenerationJobUploading, domain.GenerationJobWaitingForResources:
		return "submitting"
	case domain.GenerationJobPostprocessing, domain.GenerationJobArchiving:
		return "running"
	case domain.GenerationJobFailed:
		return "error"
	default:
		return string(job.State)
	}
}

func generationStatusForJob(job domain.GenerationJob, status generationStatus) generationStatus {
	status.PromptID = job.PromptID
	status.JobID = job.PublicID
	status.RequestID = job.RequestID
	status.CorrelationID = job.CorrelationID
	status.JobState = string(job.State)
	status.State = generationJobClientState(job)
	if job.CancellationRequestedAt != nil || job.State.Terminal() || status.Message == "" {
		status.Message = job.StatusMessage
	}
	if status.Message == "" {
		status.Message = generationJobStateMessage(job.State)
	}
	return status
}

func (a *App) generationJobResponse(ctx context.Context, job domain.GenerationJob) map[string]any {
	response := map[string]any{
		"job_id": job.PublicID, "request_id": job.RequestID, "correlation_id": job.CorrelationID, "job_state": job.State,
		"state": generationJobClientState(job), "message": job.StatusMessage,
	}
	if job.PromptID != "" {
		response["prompt_id"] = job.PromptID
		if !job.State.Terminal() {
			for key, value := range a.generationRunResponse(ctx, job.PromptID) {
				if key != "state" && key != "message" {
					response[key] = value
				}
			}
		}
	}
	if job.ErrorCode != "" {
		response["error_code"] = job.ErrorCode
	}
	return response
}

func writeGenerationJobError(w http.ResponseWriter, status int, job domain.GenerationJob, message string) {
	writeJSON(w, status, map[string]any{
		"error": message, "job_id": job.PublicID, "request_id": job.RequestID,
		"job_state": job.State, "state": generationJobClientState(job),
	})
}

func generationJobInputCount(input generationForm) int {
	count := input.imageCount()
	if strings.TrimSpace(input.InputAudio) != "" {
		count++
	}
	if strings.TrimSpace(input.InputVideo) != "" {
		count++
	}
	return count
}

func generationJobDependencies(input generationForm, user *User) []string {
	dependencies := []string{"comfyui"}
	if user != nil && user.PauseMiningForQuickGeneration {
		dependencies = append(dependencies, "mining-agent")
	}
	if input.AssistantRequested {
		dependencies = append(dependencies, "prompt-assistant")
	}
	if input.VideoRIFEEnabled {
		dependencies = append(dependencies, "rife")
	}
	if input.VideoRTXEnabled {
		dependencies = append(dependencies, "rtx-video-super-resolution")
	}
	if input.VideoColorMatch {
		dependencies = append(dependencies, "color-match")
	}
	return dependencies
}

func generationJobValues(form url.Values, resolvedSeed int64) map[string]string {
	values := make(map[string]string)
	for name, raw := range form {
		if len(raw) == 0 || name == "csrf" || name == "client_request_id" || name == "recipe_name" {
			continue
		}
		allowed := allowedGenerationRecipeField(name) || strings.HasPrefix(name, "input_image") || strings.HasPrefix(name, "assistant_") || name == "input_audio" || name == "input_video"
		if !allowed {
			continue
		}
		value := strings.TrimSpace(raw[0])
		if len(value) > 16000 {
			continue
		}
		values[name] = value
	}
	if resolvedSeed >= 0 {
		values["seed"] = strconv.FormatInt(resolvedSeed, 10)
	}
	return values
}

func (a *App) generationJobPayloadCipher(input generationForm, values url.Values) ([]byte, error) {
	if a.contentCipher == nil {
		return nil, errors.New("content cipher is not configured")
	}
	payload, err := json.Marshal(generationSavedPayload{Version: 1, Values: generationJobValues(values, input.Seed)})
	if err != nil {
		return nil, err
	}
	if len(payload) > maxGenerationRequest*2 {
		return nil, errors.New("параметры задания слишком большие")
	}
	return a.contentCipher.Encrypt(string(payload))
}

func generationJobFormValues(values map[string]string) url.Values {
	form := make(url.Values, len(values))
	for name, value := range values {
		form.Set(name, value)
	}
	return form
}

func generationJobDefinition(job domain.GenerationJob) (workflowDefinition, string, error) {
	definitionID := job.TemplateID
	family := ""
	switch job.WorkflowID {
	case "photoflow-krea2":
		definitionID, family = "text-to-image-krea2", modelFamilyKrea2
	case "photoflow-krea2-edit":
		definitionID, family = "image-to-image-krea2", modelFamilyKrea2
	case "photoflow-flux2-edit":
		definitionID, family = "image-to-image-flux2", modelFamilyFlux2
	case "minimax-h3-video":
		definitionID, family = "minimax-h3-video", modelFamilyMiniMaxH3
	}
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		return workflowDefinition{}, "", err
	}
	definition, ok := findWorkflow(definitions, definitionID)
	if !ok {
		return workflowDefinition{}, "", fmt.Errorf("generation workflow %q is unavailable", definitionID)
	}
	return definition, family, nil
}

func (a *App) ensureGenerationJobProjections(ctx context.Context, job domain.GenerationJob) error {
	if job.UserID == nil || job.PromptID == "" || len(job.PayloadCipher) == 0 {
		return nil
	}
	payload, err := a.decodeGenerationSavedPayload(job.PayloadCipher)
	if err != nil {
		return err
	}
	form := generationJobFormValues(payload.Values)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/generate/run", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	input, err := parseGenerationForm(request)
	if err != nil {
		return err
	}
	definition, family, err := generationJobDefinition(job)
	if err != nil {
		return err
	}
	input.TemplateID = job.TemplateID
	input.PresetID = job.WorkflowID
	input.ModelName = job.ModelName
	input.ModelFamily = family
	input.Seed = job.Seed
	a.recordGenerationEvent(ctx, job.ID, *job.UserID, job.PromptID, definition, input)
	a.rememberGenerationVariant(ctx, job.ID, *job.UserID, job.PromptID, input, form)
	if err := a.store.LinkGenerationJobContentEvent(ctx, job.ID, *job.UserID, job.PromptID); err != nil {
		return fmt.Errorf("link generation content projection: %w", err)
	}
	if err := a.store.LinkGenerationJobVariant(ctx, job.ID, job.PromptID); err != nil {
		return fmt.Errorf("link generation variant projection: %w", err)
	}
	return nil
}

func comfyExtraDataJobID(raw json.RawMessage) string {
	var extra map[string]json.RawMessage
	if json.Unmarshal(raw, &extra) != nil {
		return ""
	}
	var jobID string
	_ = json.Unmarshal(extra["gateway_job_id"], &jobID)
	return strings.TrimSpace(jobID)
}

func comfyQueueItemJobID(raw json.RawMessage) string {
	var item []json.RawMessage
	if json.Unmarshal(raw, &item) != nil || len(item) < 4 {
		return ""
	}
	return comfyExtraDataJobID(item[3])
}

func comfyHistoryEntryJobID(raw json.RawMessage) string {
	var entry struct {
		Prompt []json.RawMessage `json:"prompt"`
	}
	if json.Unmarshal(raw, &entry) != nil || len(entry.Prompt) < 4 {
		return ""
	}
	return comfyExtraDataJobID(entry.Prompt[3])
}

func (a *App) fetchGenerationHistory(ctx context.Context) (map[string]json.RawMessage, error) {
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/history")
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("history returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGenerationHistory+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxGenerationHistory {
		return nil, errors.New("ответ history слишком большой")
	}
	var history map[string]json.RawMessage
	if err := json.Unmarshal(body, &history); err != nil {
		return nil, err
	}
	return history, nil
}

func (a *App) findComfyPromptByGenerationJob(ctx context.Context, publicID string) (string, bool, error) {
	publicID = strings.TrimSpace(publicID)
	queue, queueErr := a.fetchGenerationQueue(ctx)
	if queueErr == nil {
		for _, raw := range append(queue.Running, queue.Pending...) {
			if comfyQueueItemJobID(raw) == publicID {
				promptID := comfyQueueItemPromptID(raw)
				return promptID, validComfyPromptID(promptID), nil
			}
		}
	}
	history, historyErr := a.fetchGenerationHistory(ctx)
	if historyErr == nil {
		for promptID, raw := range history {
			if comfyHistoryEntryJobID(raw) == publicID && validComfyPromptID(promptID) {
				return promptID, true, nil
			}
		}
	}
	if queueErr != nil || historyErr != nil {
		return "", false, errors.Join(queueErr, historyErr)
	}
	return "", false, nil
}

func (a *App) recoverGenerationJobPrompt(ctx context.Context, job domain.GenerationJob) (domain.GenerationJob, bool, error) {
	ctx = generationJobTraceContext(ctx, job)
	if job.PromptID != "" {
		return job, false, nil
	}
	promptID, found, err := a.findComfyPromptByGenerationJob(ctx, job.PublicID)
	if err != nil || !found {
		return job, false, err
	}
	job, err = a.store.BindGenerationJobPrompt(ctx, job.ID, promptID)
	if err != nil {
		return job, false, err
	}
	ctx = generationJobTraceContext(ctx, job)
	if _, err := a.store.CommitQuickGenerationForJob(ctx, job.ID); err != nil {
		return job, false, err
	}
	job, err = a.advanceGenerationJob(ctx, job, domain.GenerationJobQueued, "Генерация восстановлена в очереди ComfyUI")
	if err != nil {
		return job, false, err
	}
	if job.UserID != nil {
		a.rememberGeneration(promptID, *job.UserID)
	}
	if err := a.ensureGenerationJobProjections(ctx, job); err != nil {
		return job, true, err
	}
	return job, true, nil
}

func (a *App) expireGenerationJob(ctx context.Context, job domain.GenerationJob, message string) (domain.GenerationJob, error) {
	ctx = generationJobTraceContext(ctx, job)
	released, complete, err := a.releaseGenerationJobResources(ctx, job)
	if err != nil {
		return job, err
	}
	job = released
	if !complete {
		updated, _, updateErr := a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{State: job.State, Message: message + ". Восстанавливаем ресурсы"})
		if updateErr == nil {
			job = updated
		}
		return job, nil
	}
	updated, _, err := a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{State: domain.GenerationJobExpired, Message: message})
	return updated, err
}

func (a *App) continueGenerationJobCancellation(ctx context.Context, job domain.GenerationJob) (domain.GenerationJob, bool, error) {
	ctx = generationJobTraceContext(ctx, job)
	if job.State.Terminal() {
		return job, job.State == domain.GenerationJobCancelled, nil
	}
	if job.CancellationRequestedAt == nil {
		return job, false, errors.New("generation cancellation was not requested")
	}
	if job.UserID == nil {
		return job, false, errors.New("generation job owner was deleted")
	}
	if job.PromptID == "" {
		confirmed, _, err := a.store.ConfirmGenerationJobCancellation(ctx, job.ID)
		if err != nil {
			return job, false, err
		}
		updated, err := a.reconcileGenerationJobStatus(ctx, confirmed, generationStatus{})
		return updated, updated.State == domain.GenerationJobCancelled, err
	}
	queued, running, _, _, err := a.generationQueueState(ctx, job.PromptID)
	if err != nil {
		return job, false, fmt.Errorf("%w: inspect ComfyUI queue: %v", errGenerationCancellationNotSent, err)
	}
	cancelSent := false
	if queued {
		err = a.cancelQueuedComfyGeneration(ctx, job.PromptID)
		cancelSent = err == nil
	} else if running {
		err = a.interruptRunningComfyGeneration(ctx, job.PromptID)
		cancelSent = err == nil
	}
	if err != nil {
		return job, false, fmt.Errorf("%w: %v", errGenerationCancellationNotSent, err)
	}
	status, err := a.fetchGenerationStatus(ctx, job.PromptID, *job.UserID)
	if err != nil {
		if !cancelSent {
			return job, false, fmt.Errorf("%w: inspect ComfyUI status: %v", errGenerationCancellationNotSent, err)
		}
		return job, false, err
	}
	if status.Known && status.State == "completed" {
		cleared, _, err := a.store.ClearGenerationJobCancellation(ctx, job.ID, *job.UserID, "ComfyUI уже завершил генерацию")
		if err != nil {
			return job, false, err
		}
		updated, err := a.reconcileGenerationJobStatus(ctx, cleared, status)
		return updated, false, err
	}
	if status.Known && status.State == "error" && !cancelSent {
		cleared, _, err := a.store.ClearGenerationJobCancellation(ctx, job.ID, *job.UserID, status.Message)
		if err != nil {
			return job, false, err
		}
		updated, err := a.reconcileGenerationJobStatus(ctx, cleared, status)
		return updated, false, err
	}
	if status.Known && (status.State == "queued" || status.State == "running") {
		return job, false, nil
	}
	confirmed, _, err := a.store.ConfirmGenerationJobCancellation(ctx, job.ID)
	if err != nil {
		return job, false, err
	}
	updated, err := a.reconcileGenerationJobStatus(ctx, confirmed, status)
	return updated, updated.State == domain.GenerationJobCancelled, err
}

func generationJobStateMessage(state domain.GenerationJobState) string {
	switch state {
	case domain.GenerationJobPreparing:
		return "Проверяем параметры и workflow"
	case domain.GenerationJobWaitingForResources:
		return "Ожидаем ресурсы"
	case domain.GenerationJobQueued:
		return "Генерация поставлена в очередь ComfyUI"
	case domain.GenerationJobRunning:
		return "ComfyUI выполняет workflow"
	case domain.GenerationJobPostprocessing:
		return "Обрабатываем результат"
	case domain.GenerationJobArchiving:
		return "Сохраняем результат"
	default:
		return string(state)
	}
}

func (a *App) advanceGenerationJob(ctx context.Context, job domain.GenerationJob, target domain.GenerationJobState, finalMessage string) (domain.GenerationJob, error) {
	ctx = generationJobTraceContext(ctx, job)
	for job.State != target {
		if job.State.Terminal() {
			return job, fmt.Errorf("generation job %s is already terminal", job.PublicID)
		}
		var next domain.GenerationJobState
		switch job.State {
		case domain.GenerationJobDraft:
			next = domain.GenerationJobPreparing
		case domain.GenerationJobPreparing, domain.GenerationJobUploading:
			next = domain.GenerationJobWaitingForResources
		case domain.GenerationJobWaitingForResources:
			next = domain.GenerationJobQueued
		case domain.GenerationJobQueued:
			if target == domain.GenerationJobRunning {
				next = domain.GenerationJobRunning
			} else {
				next = domain.GenerationJobPostprocessing
			}
		case domain.GenerationJobRunning:
			next = domain.GenerationJobPostprocessing
		case domain.GenerationJobPostprocessing:
			next = domain.GenerationJobArchiving
		default:
			return job, fmt.Errorf("cannot advance generation job from %s to %s", job.State, target)
		}
		message := generationJobStateMessage(next)
		if next == target && strings.TrimSpace(finalMessage) != "" {
			message = strings.TrimSpace(finalMessage)
		}
		updated, _, err := a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{State: next, Message: message})
		if err != nil {
			return job, err
		}
		job = updated
	}
	return job, nil
}

func (a *App) releaseGenerationJobResources(ctx context.Context, job domain.GenerationJob) (domain.GenerationJob, bool, error) {
	ctx = generationJobTraceContext(ctx, job)
	if _, err := a.store.ReleaseQuickGenerationForJob(ctx, job.ID); err != nil {
		return job, false, err
	}
	if !a.releaseMiningPauseForJob(ctx, job.ID) {
		return job, false, nil
	}
	released, err := a.store.MarkGenerationJobResourcesReleased(ctx, job.ID)
	if err != nil {
		return job, false, err
	}
	return released, true, nil
}

func (a *App) failGenerationJob(ctx context.Context, job domain.GenerationJob, code, message string, cause error) domain.GenerationJob {
	ctx = generationJobTraceContext(ctx, job)
	technical := ""
	if cause != nil {
		technical = cause.Error()
	}
	released, complete, err := a.releaseGenerationJobResources(ctx, job)
	if err != nil {
		logGateway(ctx, slog.LevelError, "generation_job_resource_release_failed", "Failed to release generation job resources",
			"job_public_id", job.PublicID,
			"error", err,
		)
		return job
	}
	job = released
	if !complete {
		updated, _, updateErr := a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{
			State: job.State, Message: message + ". Восстанавливаем ресурсы",
		})
		if updateErr == nil {
			job = updated
		}
		return job
	}
	updated, _, err := a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobFailed, Message: message, ErrorCode: code, ErrorMessage: technical,
	})
	if err != nil {
		logGateway(ctx, slog.LevelError, "generation_job_failure_persist_failed", "Failed to persist generation job failure",
			"job_public_id", job.PublicID,
			"error", err,
		)
		return job
	}
	if job.UserID != nil && job.PromptID != "" {
		a.syncGenerationAuditState(ctx, *job.UserID, job.PromptID, "error")
	}
	return updated
}

func (a *App) reconcileGenerationJobStatus(ctx context.Context, job domain.GenerationJob, status generationStatus) (domain.GenerationJob, error) {
	ctx = generationJobTraceContext(ctx, job)
	if job.State.Terminal() {
		return job, nil
	}
	if job.CancellationRequestedAt != nil && job.CancellationConfirmedAt == nil {
		return job, nil
	}
	if job.CancellationConfirmedAt != nil {
		released, complete, err := a.releaseGenerationJobResources(ctx, job)
		if err != nil {
			return job, err
		}
		job = released
		if !complete {
			updated, _, updateErr := a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{State: job.State, Message: "Отмена подтверждена. Восстанавливаем ресурсы"})
			if updateErr == nil {
				job = updated
			}
			return job, nil
		}
		updated, _, err := a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{State: domain.GenerationJobCancelled, Message: "Генерация отменена"})
		if err != nil {
			return job, err
		}
		if job.UserID != nil && job.PromptID != "" {
			a.syncGenerationAuditState(ctx, *job.UserID, job.PromptID, "cancelled")
		}
		if job.PromptID != "" {
			a.generationMu.Lock()
			delete(a.generationJobs, job.PromptID)
			a.generationMu.Unlock()
		}
		a.releaseComfyMemoryIfIdle(ctx)
		a.scheduleComfyMemoryRelease()
		return updated, nil
	}
	if job.UserID == nil {
		return a.failGenerationJob(ctx, job, "generation_owner_deleted", "Владелец задания удалён", errors.New("generation owner was deleted")), nil
	}
	userID := *job.UserID
	switch status.State {
	case "queued":
		updated, err := a.advanceGenerationJob(ctx, job, domain.GenerationJobQueued, status.Message)
		if err == nil {
			a.syncGenerationAuditState(ctx, userID, job.PromptID, "queued")
		}
		return updated, err
	case "running":
		updated, err := a.advanceGenerationJob(ctx, job, domain.GenerationJobRunning, status.Message)
		if err == nil {
			a.syncGenerationAuditState(ctx, userID, job.PromptID, "running")
		}
		return updated, err
	case "error":
		return a.failGenerationJob(ctx, job, "comfy_execution_failed", status.Message, errors.New(status.Message)), nil
	case "completed":
		// Reconciliation is deliberately resumable. A previous pass may have
		// persisted the archiving state before a media fetch or resource release
		// failed, so never try to walk that durable state backwards on retry.
		if job.State != domain.GenerationJobPostprocessing && job.State != domain.GenerationJobArchiving {
			updated, err := a.advanceGenerationJob(ctx, job, domain.GenerationJobPostprocessing, "Обрабатываем результат")
			if err != nil {
				return job, err
			}
			job = updated
		}
		if job.State == domain.GenerationJobPostprocessing {
			updated, err := a.advanceGenerationJob(ctx, job, domain.GenerationJobArchiving, "Сохраняем результат")
			if err != nil {
				return job, err
			}
			job = updated
		}
		if job.State != domain.GenerationJobArchiving {
			return job, fmt.Errorf("cannot archive completed generation job from %s", job.State)
		}
		newOutputs := a.rememberGenerationOutputs(job.PromptID, status.Outputs)
		if err := a.archiveGenerationOutputs(ctx, userID, newOutputs); err != nil {
			a.forgetGenerationOutputs(job.PromptID, newOutputs)
			_, _, _ = a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{State: job.State, Message: "Не удалось сохранить результат. Повторяем архивацию"})
			return job, err
		}
		a.releaseComfyMemoryIfIdle(ctx)
		a.scheduleComfyMemoryRelease()
		released, complete, err := a.releaseGenerationJobResources(ctx, job)
		if err != nil {
			return job, err
		}
		job = released
		if !complete {
			_, _, _ = a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{State: job.State, Message: "Результат сохранён. Восстанавливаем ресурсы"})
			return job, nil
		}
		completed, _, err := a.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{State: domain.GenerationJobCompleted, Message: "Готово"})
		if err != nil {
			return job, err
		}
		a.syncGenerationAuditState(ctx, userID, job.PromptID, "completed")
		return completed, nil
	default:
		return job, nil
	}
}
