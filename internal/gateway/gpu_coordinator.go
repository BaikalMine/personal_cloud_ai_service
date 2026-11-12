package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/inference"
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/store"
)

const managedGPULeaseDuration = 5 * time.Minute

type gpuCoordinator struct{ app *App }
type gpuAdmissionWait struct{ message string }

func (e *gpuAdmissionWait) Error() string { return e.message }

type gpuAdmissionDeferred struct{ cause error }

func (e *gpuAdmissionDeferred) Error() string {
	return "Ожидание GPU продолжится автоматически."
}
func (e *gpuAdmissionDeferred) Unwrap() error { return e.cause }

func gpuWaitMessage(code string, position int) string {
	switch code {
	case "waiting_turn":
		return fmt.Sprintf("Ожидаем GPU. Место в очереди: %d.", position)
	case "executor_unconfirmed":
		return "Проверяем завершение предыдущей работы GPU. Новый запуск пока отложен."
	case "storage_cleanup":
		return "Ожидаем подтверждения завершения очистки хранилища."
	case "external_work":
		return "GPU занят внешней задачей. Ожидаем её завершения."
	case "executor_unavailable":
		return "Состояние исполнителей GPU не подтверждено. Повторяем проверку."
	default:
		return "GPU занят другой задачей. Ожидаем завершения."
	}
}

// Observations can prevent admission but never settle a previously sent request.
// User-launched work outside Gateway can still begin after this point-in-time
// probe; executor-side reservations are required to close that external race.
func (g *gpuCoordinator) observe(ctx context.Context) error {
	return g.observeExceptMiningLease(ctx, "")
}

// Only the admission-guard owner preparing this exact request may exclude its
// own freshly created mining reservation from the external-work observation.
func (g *gpuCoordinator) observeExceptMiningLease(ctx context.Context, preparingLeaseID string) error {
	a := g.app
	if err := a.store.EnsurePrimaryGPUResource(ctx); err != nil {
		return err
	}
	resource, err := a.store.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil {
		return err
	}
	observation, message := "idle", "Исполнители не сообщили об активной работе"
	if blocked, err := a.store.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil {
		return err
	} else if blocked {
		observation, message = "busy", "Очистка хранилища ещё не подтверждена"
	}
	if _, err := a.store.ActiveGPUExecutorPermit(ctx, domain.GPUPrimaryResource); err == nil {
		observation, message = "busy", "Внешний исполнитель удерживает допуск GPU"
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if observation == "idle" && a.cfg.ComfyGPUGuardRequired {
		if err := a.probeComfyGPUGuard(ctx); err != nil {
			observation, message = "unknown", "Исполнитель ComfyUI не подтвердил общий допуск GPU"
		}
	}
	if active, err := a.store.UncoordinatedGPUWork(ctx); err != nil || active {
		observation, message = "unknown", "Проверяем ранее переданные задания Gateway"
	}
	leases, err := a.store.ListQuickGenerationMiningLeases(ctx)
	if err != nil {
		return err
	}
	for _, lease := range leases {
		if preparingLeaseID != "" && lease.ID == preparingLeaseID {
			continue
		}
		if !lease.ResumeReady && lease.GenerationJobID == 0 && lease.LoraTrainingJobID == 0 && lease.PromptID == "" {
			observation, message = "unknown", "Завершение прежнего запроса ассистента не подтверждено"
		}
	}
	if observation == "idle" && a.cfg.ComfyUIUpstream != nil {
		queue, err := a.fetchGenerationQueue(ctx)
		if err != nil || queue.Running == nil || queue.Pending == nil {
			observation, message = "unknown", "Не удалось проверить очередь ComfyUI"
		} else if len(queue.Running)+len(queue.Pending) > 0 {
			observation, message = "busy", "В ComfyUI есть выполняемая или ожидающая задача"
		}
	}
	if observation == "idle" && a.loraTraining.Configured() {
		activity, err := a.loraTraining.Activity(ctx)
		if err != nil || activity.State == "uncertain" {
			observation, message = "unknown", "Windows-agent не подтвердил состояние обучения"
		} else if activity.State != "idle" {
			observation, message = "busy", "Windows-agent подготавливает или выполняет обучение"
		}
	}
	if observation == "idle" && a.cfg.Harness.Enabled() {
		if err := inference.ProbeOwnedOllamaReadiness(ctx, a.cfg.InferenceWorkerURL); err != nil {
			observation, message = "unknown", "Исполнитель Harness не подтвердил общий допуск GPU"
		}
	}
	if observation == "idle" && a.cfg.OllamaGPUGuardRequired {
		if err := inference.ProbeOwnedOllamaReadiness(ctx, a.cfg.InferenceWorkerURL); err != nil {
			observation, message = "unknown", "Управляемый исполнитель Ollama не подтвердил общий допуск GPU"
		}
	} else if observation == "idle" && (a.promptAssistant.Configured() || a.promptAssistant.VisionConfigured()) {
		loaded, err := a.promptAssistant.LoadedModels(ctx)
		if err != nil {
			observation, message = "unknown", "Не удалось проверить состояние локальной модели"
		} else if loaded > 0 {
			observation, message = "busy", "Локальная модель ещё загружена. Ожидаем освобождения памяти"
		}
	}
	_, err = a.store.ObserveGPUResource(ctx, resource.ID, resource.Revision, observation, message, 3*time.Second)
	return err
}

func (g *gpuCoordinator) admit(ctx context.Context, kind domain.GPUWorkKind, key string, user *User, token string, queuedAt time.Time) (domain.GPUWork, error) {
	if g == nil {
		return domain.GPUWork{}, nil
	}
	var work domain.GPUWork
	err := g.app.store.WithGPUAdmissionGuard(ctx, func() error {
		var err error
		work, err = g.admitLocked(ctx, kind, key, user, token, queuedAt)
		return err
	})
	if errors.Is(err, store.ErrGPUWorkConflict) {
		err = &gpuAdmissionWait{message: "Повторяем проверку доступа к GPU."}
	}
	return work, err
}

func (g *gpuCoordinator) admitLocked(ctx context.Context, kind domain.GPUWorkKind, key string, user *User, token string, queuedAt time.Time) (domain.GPUWork, error) {
	if user == nil {
		return domain.GPUWork{}, errors.New("GPU work owner is required")
	}
	work, err := g.app.store.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: newRequestID(), ResourceID: domain.GPUPrimaryResource, Kind: kind, JobKey: key, UserID: user.ID, Priority: user.QueuePriority, QueuedAt: queuedAt})
	if err != nil {
		return work, err
	}
	if work.State == domain.GPUWorkReleased && (work.Phase == "reserved" || kind == domain.GPUWorkTraining) {
		work, err = g.app.store.RetryGPUWork(ctx, work.ID)
		if err != nil {
			return work, err
		}
	}
	if (kind == domain.GPUWorkGeneration || kind == domain.GPUWorkTraining) &&
		(work.State == domain.GPUWorkHeld || work.State == domain.GPUWorkUncertain) && work.LeaseToken != token {
		work, err = g.app.store.RequeueUnsubmittedGPUWork(ctx, work.ID, token)
		if err != nil {
			return work, err
		}
	}
	if _, err = g.app.store.ActiveGPUWork(ctx, work.ResourceID); errors.Is(err, sql.ErrNoRows) {
		if err = g.app.fenceMiningBeforeGPU(ctx, kind, user); err != nil {
			return work, err
		}
		if err = g.observe(ctx); err != nil && !errors.Is(err, store.ErrGPUWorkConflict) {
			return work, err
		}
	} else if err != nil {
		return work, err
	}
	admission, err := g.app.store.AcquireGPUWork(ctx, work.ID, token, managedGPULeaseDuration)
	if err != nil {
		return work, err
	}
	if !admission.Granted {
		return admission.Work, &gpuAdmissionWait{message: gpuWaitMessage(admission.WaitCode, admission.Position)}
	}
	return admission.Work, nil
}

func (g *gpuCoordinator) maintain(parent context.Context, work domain.GPUWork) (context.Context, func()) {
	if g == nil {
		return parent, func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				probe, release := context.WithTimeout(ctx, 5*time.Second)
				ok, err := g.app.store.HeartbeatGPUWork(probe, work.ID, work.LeaseToken, managedGPULeaseDuration)
				release()
				if err != nil || !ok {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }
}

func (a *App) refreshManagedGPUWork(ctx context.Context) (int64, error) {
	if a.gpuCoordinator == nil {
		return 0, nil
	}
	work, err := a.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if work.Kind == domain.GPUWorkHarnessUltra {
		return 1, a.reconcileHarnessUltraWork(ctx, work)
	}
	if work.Kind == domain.GPUWorkAssistant || work.Kind == domain.GPUWorkCaption {
		if work.Kind == domain.GPUWorkAssistant && work.JobKey == domain.PromptAssistantGPUKey(work.ID) && work.InferenceCompletion == "" && work.ExternalID == domain.GPUInferenceExternalID(work.ID) {
			request, err := a.store.CancelInactiveAssistantInference(ctx, work.ID)
			if err != nil {
				return 0, err
			}
			work.InferenceCompletion = request.CompletionCode
		}
		if work.Kind == domain.GPUWorkCaption && work.InferenceCompletion == "" && work.ExternalID == domain.GPUInferenceExternalID(work.ID) {
			request, err := a.store.CancelInactiveCaptionInference(ctx, work.ID)
			if err != nil {
				return 0, err
			}
			work.InferenceCompletion = request.CompletionCode
		}
		if work.InferenceCompletion == "" {
			fenced, changed, err := a.store.FenceExpiredGPUInferenceReservation(ctx, work.ID)
			if err != nil || !changed {
				return 0, err
			}
			work = fenced
		}
		return 1, a.gpuCoordinator.release(ctx, work.Kind, work.JobKey, work.InferenceCompletion)
	}
	if work.Kind == domain.GPUWorkGeneration {
		job, err := a.store.GenerationJobForGPUWork(ctx, work.ID)
		if err != nil {
			return 0, err
		}
		if job.State.Terminal() || job.DispatchClosedAt != nil {
			_, _, err := a.releaseGenerationJobResources(ctx, job)
			return 1, err
		}
		if job.SubmissionStartedAt == nil && job.DispatchToken != work.LeaseToken {
			return 0, nil
		}
	} else {
		job, err := a.store.LoraTrainingJobByPublicID(ctx, work.JobKey, 0, true)
		if err != nil {
			return 0, err
		}
		if job.State.Terminal() || job.State == domain.LoraTrainingQueued {
			if !a.releaseMiningPauseForLoraTraining(ctx, job.ID) {
				return 0, errors.New("training GPU release is pending")
			}
			return 1, nil
		}
		if job.AgentSubmissionStartedAt == nil && job.DispatchToken != work.LeaseToken {
			return 0, nil
		}
	}
	// Per-source reconciliation supplies terminal evidence and releases. This
	// heartbeat only retains ownership; it cannot prove that execution stopped.
	if work.State == domain.GPUWorkHeld {
		_, err = a.store.HeartbeatGPUWork(ctx, work.ID, work.LeaseToken, managedGPULeaseDuration)
	}
	return 1, err
}

func (g *gpuCoordinator) dispatch(ctx context.Context, work domain.GPUWork) error {
	if g == nil {
		return nil
	}
	_, err := g.app.store.SetGPUWorkPhase(ctx, work.ID, work.LeaseToken, "dispatching", "")
	return err
}

func (g *gpuCoordinator) release(ctx context.Context, kind domain.GPUWorkKind, key, code string) error {
	if g == nil {
		return nil
	}
	work, err := g.app.store.GPUWorkBySource(ctx, kind, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if work.State == domain.GPUWorkReleased || work.State == domain.GPUWorkCancelled {
		return nil
	}
	if work.State == domain.GPUWorkWaiting {
		if code == "dispatch_fenced" {
			return g.app.store.CancelFencedWaitingGPUWork(ctx, work.ID)
		}
		_, err = g.app.store.RequestGPUWorkCancellation(ctx, work.ID)
		return err
	}
	resource, err := g.app.store.GPUResource(ctx, work.ResourceID)
	if err != nil {
		return err
	}
	_, err = g.app.store.ReleaseGPUWork(ctx, work.ID, work.LeaseToken, domain.GPUReleaseEvidence{Code: code, Detail: "Source lifecycle confirmed completion or prevented submission", ResourceRevision: resource.Revision})
	return err
}

func (g *gpuCoordinator) finishInference(kind domain.GPUWorkKind, key string, execution promptassistant.ExecutionOutcome) {
	if g == nil || !execution.Settled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := "executor_terminal"
	if execution == promptassistant.ExecutionNotDispatched {
		code = "request_not_dispatched"
	} else if execution == promptassistant.ExecutionStopped {
		code = "executor_stopped"
	}
	if err := g.app.store.RecordGPUInferenceCompletion(ctx, kind, key, code); err != nil {
		log.Printf("record GPU inference completion %s: %v", key, err)
		return
	}
	if err := g.release(ctx, kind, key, code); err != nil {
		log.Printf("release GPU inference %s: %v", key, err)
	}
}

func (g *gpuCoordinator) wait(ctx context.Context, kind domain.GPUWorkKind, key string, user *User, queuedAt time.Time, onWait func(string)) (domain.GPUWork, error) {
	if g == nil {
		return domain.GPUWork{}, nil
	}
	token := newRequestID()
	for {
		work, err := g.admit(ctx, kind, key, user, token, queuedAt)
		var waiting *gpuAdmissionWait
		if !errors.As(err, &waiting) {
			return work, err
		}
		if onWait != nil {
			onWait(waiting.Error())
		}
		select {
		case <-ctx.Done():
			g.finishInference(kind, key, promptassistant.ExecutionNotDispatched)
			return work, &gpuAdmissionDeferred{cause: ctx.Err()}
		case <-time.After(2 * time.Second):
		}
	}
}
