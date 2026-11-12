package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/inference"
	"ai-access-gateway/internal/promptassistant"
)

func (a *App) refreshPromptAssistantJobs(ctx context.Context) (int64, error) {
	if a.store == nil || a.contentCipher == nil || a.gpuCoordinator == nil {
		return 0, nil
	}
	var count int64
	_, err := a.store.WithPromptAssistantWorker(ctx, func(*sql.Conn) error {
		job, err := a.store.ClaimPromptAssistantJob(ctx, newRequestID())
		if err != nil || job.ID == "" {
			return err
		}
		count = 1
		if err = a.runPromptAssistantJob(ctx, job); err != nil {
			recoverCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = a.store.PostponePromptAssistantJob(recoverCtx, job.ID, job.RunToken, "Проверяем сохранённое задание ассистента.")
		}
		return err
	})
	return count, err
}

func (a *App) runPromptAssistantJob(ctx context.Context, job domain.PromptAssistantJob) error {
	user, err := a.store.UserByID(ctx, job.UserID)
	if err != nil {
		return err
	}
	if job.CancelRequested || !job.ExpiresAt.After(time.Now()) || user.Disabled ||
		(user.AccountExpiresAt.Valid && !user.AccountExpiresAt.Time.After(time.Now())) || !user.CanUseQuickGenerationType(job.Mode) {
		return a.cancelPromptAssistantJob(ctx, job)
	}
	snapshot, err := a.promptAssistantSnapshot(job)
	if err != nil {
		return err
	}
	if job.InferenceWorkID != "" {
		return a.recoverPromptAssistantJob(ctx, job, snapshot)
	}
	modelAllowed, err := a.store.PromptAssistantModelAllowed(ctx, job.UserID, job.ModelID)
	if err != nil {
		return err
	}
	if !modelAllowed || !a.assistantSnapshotMatchesCatalog(snapshot) {
		return a.cancelPromptAssistantJob(ctx, job)
	}
	work, changed, err := a.store.FencePromptAssistantPreparation(ctx, job.ID, job.RunToken)
	if err != nil {
		return err
	}
	key := domain.PromptAssistantGPUKey(job.ID)
	if changed {
		if err = a.gpuCoordinator.release(ctx, domain.GPUWorkAssistant, key, "request_not_dispatched"); err != nil {
			return err
		}
		work, err = a.store.GPUWork(ctx, job.ID)
		if err != nil {
			return err
		}
	}
	if err := a.checkAssistantModelExecutor(ctx, job.ModelID); err != nil {
		return a.store.PostponePromptAssistantJob(ctx, job.ID, job.RunToken, "Ожидаем совместимого исполнителя модели ассистента.")
	}
	if work.State == domain.GPUWorkReleased {
		if work, err = a.store.RetryGPUWork(ctx, job.ID); err != nil {
			return err
		}
	}
	if (work.State == domain.GPUWorkHeld || work.State == domain.GPUWorkUncertain) && work.LeaseToken != job.RunToken {
		return a.store.PostponePromptAssistantJob(ctx, job.ID, job.RunToken, "Проверяем завершение предыдущей подготовки GPU.")
	}
	release, ok := a.mediaByteLimiter().tryAcquire(promptAssistantMemoryReservation)
	if !ok {
		return a.store.PostponePromptAssistantJob(ctx, job.ID, job.RunToken, "Ожидаем освобождения памяти для ассистента.")
	}
	defer release()
	work, err = a.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, key, &user, job.RunToken, job.CreatedAt)
	var waiting *gpuAdmissionWait
	if errors.As(err, &waiting) {
		return a.store.PostponePromptAssistantJob(ctx, job.ID, job.RunToken, waiting.Error())
	}
	if err != nil {
		return err
	}
	cipher, err := a.store.PromptAssistantRequest(ctx, job.ID, job.RunToken)
	if err != nil {
		return err
	}
	defer clear(cipher)
	body, err := a.contentCipher.DecryptBytesWithAAD(cipher, promptAssistantRequestAAD(job.ID))
	defer clear(body)
	bodyHash := sha256.Sum256(body)
	if err != nil || hex.EncodeToString(bodyHash[:]) != job.BodyHash {
		result := promptassistant.Result{Model: snapshot.Request.Model, Policy: snapshot.Request.Policy}
		return a.completePromptAssistantJob(ctx, job, snapshot, result, "Сохранённый запрос повреждён. Отправьте новый запрос.", 0, errors.New("saved assistant request is unreadable"))
	}
	lease, warning, err := a.pauseMiningForInference(ctx, &user, work)
	if err != nil {
		return err
	}
	runtime, err := json.Marshal(promptAssistantJobRuntime{MiningWarning: warning, MiningPaused: lease != nil && lease.ResumeMining && warning == ""})
	if err != nil {
		return err
	}
	defer clear(runtime)
	metadata, err := a.contentCipher.EncryptBytes(runtime)
	if err != nil {
		return err
	}
	_, err = inference.NewClient(a.store, a.contentCipher).SubmitAssistant(ctx, work, body, a.assistantDispatchTimeout(snapshot), job.ID, job.RunToken, metadata)
	if err != nil {
		return err
	}
	return a.store.PostponePromptAssistantJob(ctx, job.ID, job.RunToken, "Модель готовит вариант описания.")
}

func (a *App) cancelPromptAssistantJob(ctx context.Context, job domain.PromptAssistantJob) error {
	if err := a.store.CancelPromptAssistantJob(ctx, job.UserID, job.ID); err != nil {
		return err
	}
	if job.InferenceWorkID != "" {
		if _, err := a.store.CancelGPUInference(ctx, job.InferenceWorkID); err != nil {
			return err
		}
	}
	changed, err := a.store.FinishPromptAssistantJob(ctx, job, "cancelled", "Описание отменено", nil, domain.ContentEventRecord{}, domain.PromptAssistantRunRecord{})
	if err == nil && changed {
		a.releaseUnsentPromptAssistant(ctx, job)
	}
	return err
}

func (a *App) releaseUnsentPromptAssistant(ctx context.Context, job domain.PromptAssistantJob) {
	work, err := a.store.GPUWork(ctx, job.ID)
	if err != nil || work.ExternalID != "" {
		return
	}
	if work.State == domain.GPUWorkWaiting {
		_, _ = a.store.RequestGPUWorkCancellation(ctx, job.ID)
	} else if work.Phase == "reserved" || work.Phase == "resources_ready" {
		a.gpuCoordinator.finishInference(domain.GPUWorkAssistant, work.JobKey, promptassistant.ExecutionNotDispatched)
	}
}

func (a *App) recoverPromptAssistantJob(ctx context.Context, job domain.PromptAssistantJob, snapshot promptAssistantJobSnapshot) error {
	request, err := a.store.GPUInference(ctx, job.InferenceWorkID)
	if err != nil {
		return err
	}
	snapshot.Request.Policy.TimeoutMS = request.TimeoutMS
	snapshot.Request.Policy.Timeout = time.Duration(request.TimeoutMS) * time.Millisecond
	if !request.Settled() {
		message := "Модель готовит вариант описания."
		if request.State == "uncertain" {
			message = "Ответ модели пока не подтверждён. Проверяем прежний запрос."
		}
		return a.store.PostponePromptAssistantJob(ctx, job.ID, job.RunToken, message)
	}
	execution := promptassistant.ExecutionCompleted
	if request.State == "not_dispatched" {
		execution = promptassistant.ExecutionNotDispatched
	} else if request.State == "stopped" {
		execution = promptassistant.ExecutionStopped
	}
	defer a.gpuCoordinator.finishInference(domain.GPUWorkAssistant, domain.PromptAssistantGPUKey(job.ID), execution)
	result := promptassistant.Result{Model: snapshot.Request.Model, Policy: snapshot.Request.Policy}
	message := "Вариант описания готов"
	saved, body, runErr := inference.NewClient(a.store, a.contentCipher).Wait(ctx, request.ID, request.LeaseToken)
	if runErr != nil && !errors.Is(runErr, inference.ErrNotDispatched) && !errors.Is(runErr, inference.ErrResultExpired) && !errors.Is(runErr, inference.ErrStopped) {
		return runErr
	}
	defer clear(body)
	if runErr == nil {
		result, runErr = promptassistant.ParseEnhancementResponse(snapshot.Request, saved.ResponseStatus, body)
	}
	if runErr != nil {
		message = "Не удалось подготовить вариант. Можно отправить новый запрос."
		if errors.Is(runErr, promptassistant.ErrMissingAudioPrompt) {
			message = "Модель подготовила описание видео, но не вернула озвучку LTX. Повторите запрос или выберите другую модель ассистента."
		}
	}
	var latency time.Duration
	if saved.StartedAt != nil && saved.FinishedAt != nil {
		latency = max(0, saved.FinishedAt.Sub(*saved.StartedAt))
	}
	return a.completePromptAssistantJob(ctx, job, snapshot, result, message, latency, runErr)
}
