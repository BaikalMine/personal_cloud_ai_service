package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/promptassistant"
)

// pauseMiningForQuickGeneration reserves the active mining profile before a
// user with this mining policy submits work to ComfyUI. Multiple leases keep the miner
// paused until the final related generation finishes.
func (a *App) pauseMiningForQuickGeneration(ctx context.Context, user *User, jobID int64) (*domain.QuickGenerationMiningLease, string, error) {
	if user == nil || !user.PauseMiningForQuickGeneration {
		return nil, "", nil
	}
	return a.reserveMiningForGPUWork(ctx, user, jobID, 0, nil, nil)
}

func (a *App) pauseMiningForInference(ctx context.Context, user *User, work domain.GPUWork) (*domain.QuickGenerationMiningLease, string, error) {
	if a.gpuCoordinator == nil || user == nil || !user.PauseMiningForQuickGeneration {
		return a.pauseMiningForQuickGeneration(ctx, user, 0)
	}
	if err := a.store.SetGPUInferencePreparation(ctx, work.ID, work.LeaseToken, false); err != nil {
		return nil, "", err
	}
	lease, warning, err := a.reserveMiningForGPUWork(ctx, user, 0, 0, &work, nil)
	if err != nil {
		return lease, warning, err
	}
	if err = a.store.SetGPUInferencePreparation(ctx, work.ID, work.LeaseToken, true); err != nil {
		return lease, warning, err
	}
	return lease, warning, nil
}

func (a *App) pauseMiningForLoraTraining(ctx context.Context, user *User, jobID int64) (*domain.QuickGenerationMiningLease, string, error) {
	if user == nil {
		return nil, "", nil
	}
	if jobID > 0 {
		existing, err := a.store.QuickGenerationMiningLeaseByLoraTrainingJobID(ctx, jobID)
		if err == nil {
			return &existing, "", nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, "", fmt.Errorf("проверить резервирование GPU для LoRA: %w", err)
		}
	}
	return a.reserveMiningForGPUWork(ctx, user, 0, jobID, nil, nil)
}

func (a *App) reserveMiningForGPUWork(ctx context.Context, user *User, generationJobID, loraTrainingJobID int64, gpuWork *domain.GPUWork, permit *domain.GPUExecutorPermitRequest) (*domain.QuickGenerationMiningLease, string, error) {
	a.miningPauseMu.Lock()
	defer a.miningPauseMu.Unlock()
	createLease := func(lease *domain.QuickGenerationMiningLease) error {
		if permit != nil {
			lease.ExecutorPermitID, lease.ExecutorOwnerID, lease.ExecutorGeneration = permit.ID, permit.OwnerID, permit.OwnerGeneration
			return a.store.CreateHarnessMiningLease(ctx, *lease, *permit)
		}
		if gpuWork != nil {
			lease.GPUWorkID = gpuWork.ID
			return a.store.CreateGPUInferenceMiningLease(ctx, *lease, gpuWork.LeaseToken)
		}
		return a.store.CreateQuickGenerationMiningLease(ctx, *lease)
	}

	existing, err := a.store.ActiveQuickGenerationMiningLease(ctx)
	if err == nil {
		overview := a.miningOverview(ctx, true, false)
		if miningUncontrolled(overview) {
			return nil, "", errors.New("работает внешний майнер или несколько майнеров; сначала добавьте точный BAT в профиль и выберите один запуск")
		}
		wasRunning := overview.Available && overview.Running && overview.Active != nil
		if wasRunning && overview.Active != nil {
			state, stopErr := a.stopMining(ctx, mining.Request{ScriptPath: overview.Active.ScriptPath, ProcessName: overview.Active.ProcessName})
			if stopErr != nil {
				return nil, "", fmt.Errorf("остановить майнинг: %w", stopErr)
			}
			if state.Running {
				return nil, "", errors.New("майнинг не остановился")
			}
		}
		lease := domain.QuickGenerationMiningLease{
			ID: newRequestID(), CorrelationID: correlationIDFromContext(ctx), GenerationJobID: generationJobID, LoraTrainingJobID: loraTrainingJobID, UserID: user.ID, MinerID: existing.MinerID,
			ScriptPath: existing.ScriptPath, ProcessName: existing.ProcessName, ResumeMining: existing.ResumeMining || wasRunning,
		}
		if err := createLease(&lease); err != nil {
			return nil, "", fmt.Errorf("создать резервирование майнинга: %w", err)
		}
		if !overview.Available {
			if a.cfg.MiningFencedCommands {
				return &lease, "", errors.New("остановка майнинга не подтверждена")
			}
			return &lease, miningPriorityDegradationWarning(overview), nil
		}
		return &lease, "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("проверить резервирования майнинга: %w", err)
	}

	overview := a.miningOverview(ctx, true, false)
	if miningUncontrolled(overview) {
		return nil, "", errors.New("работает внешний майнер или несколько майнеров; автоматическая остановка не выполнялась")
	}
	target := overview.Active
	if target == nil {
		target = overview.Default
	}
	if target == nil || (!target.Enabled && !target.State.Running) {
		return nil, "", nil
	}
	wasRunning := overview.Available && overview.Running && overview.Active != nil
	lease := domain.QuickGenerationMiningLease{
		ID: newRequestID(), CorrelationID: correlationIDFromContext(ctx), GenerationJobID: generationJobID, LoraTrainingJobID: loraTrainingJobID, UserID: user.ID, MinerID: target.ID,
		ScriptPath: target.ScriptPath, ProcessName: target.ProcessName, ResumeMining: true,
	}
	if err := createLease(&lease); err != nil {
		return nil, "", fmt.Errorf("создать резервирование майнинга: %w", err)
	}
	if wasRunning {
		state, stopErr := a.stopMining(ctx, mining.Request{ScriptPath: target.ScriptPath, ProcessName: target.ProcessName})
		if stopErr != nil || state.Running {
			_, _, deleteErr := a.store.DeleteQuickGenerationMiningLease(ctx, lease.ID)
			if deleteErr != nil {
				log.Printf("remove failed mining-pause lease %s: %v", lease.ID, deleteErr)
			}
			if stopErr != nil {
				return nil, "", fmt.Errorf("остановить майнинг: %w", stopErr)
			}
			return nil, "", errors.New("майнинг не остановился")
		}
	}
	action := "mining_resume_reserved_for_quick_generation"
	if wasRunning {
		action = "mining_paused_for_quick_generation"
	}
	a.audit(ctx, &user.ID, action, "miner", &target.ID, "", "", map[string]any{
		"lease_id": lease.ID, "username": user.Username, "process_name": target.ProcessName, "was_running": wasRunning,
	})
	if !overview.Available {
		if a.cfg.MiningFencedCommands {
			return &lease, "", errors.New("остановка майнинга не подтверждена")
		}
		return &lease, miningPriorityDegradationWarning(overview), nil
	}
	if !wasRunning {
		return &lease, "Майнинг уже был остановлен. После завершения или отмены генерации он будет запущен автоматически.", nil
	}
	return &lease, "", nil
}

func miningPriorityDegradationWarning(overview MiningOverview) string {
	detail := strings.TrimSpace(overview.Agent.Detail)
	if detail == "" {
		detail = strings.TrimSpace(overview.Message)
	}
	if detail == "" {
		detail = "Windows-agent не отвечает"
	}
	warning := "Остановка майнинга не подтверждена. Генерация продолжена, а автоматический запуск майнинга после завершения или отмены сохранён. Причина: " + detail
	if overview.Agent.RetryInSeconds > 0 {
		warning += fmt.Sprintf(" Следующая проверка через %d сек.", overview.Agent.RetryInSeconds)
	}
	return warning
}

func (a *App) attachMiningPauseToGeneration(ctx context.Context, lease *domain.QuickGenerationMiningLease, promptID string) error {
	if lease == nil {
		return nil
	}
	attached, err := a.store.AttachQuickGenerationMiningLease(ctx, lease.ID, promptID)
	if err != nil {
		return err
	}
	if !attached {
		return errors.New("резервирование майнинга не найдено")
	}
	return nil
}

func (a *App) releaseMiningPauseForGeneration(ctx context.Context, promptID string) bool {
	lease, err := a.store.QuickGenerationMiningLeaseByPrompt(ctx, promptID)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil {
		log.Printf("load mining-pause lease for %s: %v", promptID, err)
		return false
	}
	return a.releaseMiningPause(ctx, lease.ID)
}

func (a *App) releaseMiningPauseForJob(ctx context.Context, jobID int64) bool {
	lease, err := a.store.QuickGenerationMiningLeaseByJobID(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil {
		log.Printf("load mining-pause lease for job %d: %v", jobID, err)
		return false
	}
	return a.releaseMiningPause(ctx, lease.ID)
}

func (a *App) releaseMiningPauseForLoraTraining(ctx context.Context, jobID int64) bool {
	if a.gpuCoordinator != nil {
		job, err := a.store.LoraTrainingJobByID(ctx, jobID)
		if err != nil {
			return false
		}
		if !job.State.Terminal() && job.AgentSubmissionStartedAt != nil {
			return false
		}
		code := "executor_terminal"
		if job.AgentSubmissionStartedAt == nil {
			code = "dispatch_fenced"
		}
		if err := a.gpuCoordinator.release(ctx, domain.GPUWorkTraining, job.PublicID, code); err != nil {
			return false
		}
	}
	lease, err := a.store.QuickGenerationMiningLeaseByLoraTrainingJobID(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil {
		log.Printf("load mining-pause lease for LoRA training job %d: %v", jobID, err)
		return false
	}
	return a.releaseMiningPause(ctx, lease.ID)
}

func (a *App) releaseMiningPause(ctx context.Context, leaseID string) bool {
	a.miningPauseMu.Lock()
	defer a.miningPauseMu.Unlock()
	if err := a.store.MarkQuickGenerationMiningLeaseReady(ctx, leaseID); err != nil {
		log.Printf("record mining-pause completion %s: %v", leaseID, err)
		return false
	}
	leases, err := a.store.ListQuickGenerationMiningLeases(ctx)
	if err != nil {
		log.Printf("read mining-pause leases before resume %s: %v", leaseID, err)
		return false
	}
	var lease domain.QuickGenerationMiningLease
	for _, candidate := range leases {
		if candidate.ID == leaseID {
			lease = candidate
			break
		}
	}
	if lease.ID == "" {
		return true
	}
	remove := func() bool {
		_, _, err := a.store.DeleteQuickGenerationMiningLease(ctx, leaseID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("remove completed mining-pause lease %s: %v", leaseID, err)
			return false
		}
		return true
	}
	if len(leases) > 1 || !lease.ResumeMining {
		return remove()
	}
	resume := func() bool {
		if a.gpuCoordinator != nil {
			if _, err := a.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
				return false
			}
			if err := a.gpuCoordinator.observe(ctx); err != nil {
				return false
			}
			resource, err := a.store.GPUResource(ctx, domain.GPUPrimaryResource)
			if err != nil || resource.Observation != "idle" || !resource.ValidUntil.After(time.Now()) {
				return false
			}
		}
		// Keep the final durable lease until Start is confirmed. A process exit
		// during network I/O must leave a retryable resume request in PostgreSQL.
		overview := a.miningOverview(ctx, true, false)
		if !overview.Available {
			log.Printf("resume mining after quick generation: %s", overview.Message)
			return false
		}
		if overview.Running {
			return remove()
		}
		state, err := a.resumeMiningCommand(ctx, mining.Request{ScriptPath: lease.ScriptPath, ProcessName: lease.ProcessName})
		if err != nil || !state.Running {
			if err != nil {
				log.Printf("resume mining after quick generation: %v", err)
			} else {
				log.Printf("resume mining after quick generation: miner did not start")
			}
			return false
		}
		a.audit(ctx, &lease.UserID, "mining_resumed_after_quick_generation", "miner", &lease.MinerID, "", "", map[string]any{
			"lease_id": lease.ID, "prompt_id": lease.PromptID, "process_name": lease.ProcessName,
		})
		return remove()
	}
	if a.gpuCoordinator == nil {
		return resume()
	}
	var resumed bool
	err = a.store.WithGPUAdmissionGuard(ctx, func() error {
		resumed = resume()
		return nil
	})
	return err == nil && resumed
}

func (a *App) finishInferenceMiningPause(lease *domain.QuickGenerationMiningLease, execution promptassistant.ExecutionOutcome) {
	if lease == nil {
		return
	}
	if !execution.Settled() {
		log.Printf("retain mining-pause lease %s: local inference completion is unconfirmed", lease.ID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a.releaseMiningPause(ctx, lease.ID)
}

// refreshQuickGenerationMiningLeases also covers a Gateway restart: leases
// are in PostgreSQL, so completed ComfyUI tasks still release the miner.
func (a *App) refreshQuickGenerationMiningLeases(ctx context.Context) (int64, error) {
	leases, err := a.store.ListQuickGenerationMiningLeases(ctx)
	if err != nil {
		log.Printf("list mining-pause leases: %v", err)
		return 0, err
	}
	var processed int64
	var refreshErrors []error
	for _, lease := range leases {
		if ctx.Err() != nil {
			return processed, errors.Join(append(refreshErrors, ctx.Err())...)
		}
		processed++
		if lease.ResumeReady {
			a.releaseMiningPause(ctx, lease.ID)
			continue
		}
		if lease.ExecutorPermitID != "" {
			settled, err := a.store.HarnessMiningLeaseSettled(ctx, lease)
			if err != nil {
				refreshErrors = append(refreshErrors, err)
			} else if settled {
				a.releaseMiningPause(ctx, lease.ID)
			}
			continue
		}
		if lease.LoraTrainingJobID > 0 {
			job, jobErr := a.store.LoraTrainingJobByID(ctx, lease.LoraTrainingJobID)
			if jobErr != nil {
				if !errors.Is(jobErr, sql.ErrNoRows) {
					refreshErrors = append(refreshErrors, fmt.Errorf("lease %s LoRA training job: %w", lease.ID, jobErr))
				}
				continue
			}
			if job.State.Terminal() {
				a.releaseMiningPauseForLoraTraining(ctx, job.ID)
			}
			continue
		}
		if lease.GenerationJobID > 0 {
			job, jobErr := a.store.GenerationJobByID(ctx, lease.GenerationJobID)
			if jobErr != nil {
				if !errors.Is(jobErr, sql.ErrNoRows) {
					refreshErrors = append(refreshErrors, fmt.Errorf("lease %s job: %w", lease.ID, jobErr))
				}
				continue
			}
			if job.State.Terminal() {
				_, complete, releaseErr := a.releaseGenerationJobResources(ctx, job)
				if releaseErr != nil {
					refreshErrors = append(refreshErrors, fmt.Errorf("lease %s terminal job release: %w", lease.ID, releaseErr))
				} else if !complete {
					refreshErrors = append(refreshErrors, fmt.Errorf("lease %s terminal job resources are still reserved", lease.ID))
				}
			}
			continue
		}
		if lease.PromptID == "" {
			// Age alone cannot prove that an unbound inference request stopped.
			continue
		}
		status, statusErr := a.fetchGenerationStatus(ctx, lease.PromptID, lease.UserID)
		if statusErr != nil {
			refreshErrors = append(refreshErrors, fmt.Errorf("lease %s status: %w", lease.ID, statusErr))
			continue
		}
		if status.State == "completed" || status.State == "error" {
			a.syncGenerationAuditState(ctx, lease.UserID, lease.PromptID, status.State)
			a.releaseMiningPauseForGeneration(ctx, lease.PromptID)
		} else if status.State == "running" {
			a.syncGenerationAuditState(ctx, lease.UserID, lease.PromptID, status.State)
		}
	}
	return processed, errors.Join(refreshErrors...)
}
