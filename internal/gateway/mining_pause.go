package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
)

// pauseMiningForQuickGeneration reserves the active mining profile before a
// priority-pool user submits work to ComfyUI. Multiple leases keep the miner
// paused until the final related generation finishes.
func (a *App) pauseMiningForQuickGeneration(ctx context.Context, user *User) (*domain.QuickGenerationMiningLease, string, error) {
	if user == nil || !user.PauseMiningForQuickGeneration {
		return nil, "", nil
	}
	a.miningPauseMu.Lock()
	defer a.miningPauseMu.Unlock()

	existing, err := a.store.ActiveQuickGenerationMiningLease(ctx)
	if err == nil {
		overview := a.miningOverview(ctx, true)
		if !overview.Available {
			return nil, "Агент майнинга недоступен: генерация запущена, но остановка майнинга не подтверждена.", nil
		}
		wasRunning := overview.Running
		if wasRunning && overview.Active != nil {
			state, stopErr := a.mining.Stop(ctx, mining.Request{ScriptPath: overview.Active.ScriptPath, ProcessName: overview.Active.ProcessName})
			if stopErr != nil {
				return nil, "", fmt.Errorf("остановить майнинг: %w", stopErr)
			}
			if state.Running {
				return nil, "", errors.New("майнинг не остановился")
			}
		}
		lease := domain.QuickGenerationMiningLease{
			ID: newRequestID(), UserID: user.ID, MinerID: existing.MinerID,
			ScriptPath: existing.ScriptPath, ProcessName: existing.ProcessName, ResumeMining: existing.ResumeMining || wasRunning,
		}
		if err := a.store.CreateQuickGenerationMiningLease(ctx, lease); err != nil {
			return nil, "", fmt.Errorf("создать резервирование майнинга: %w", err)
		}
		return &lease, "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("проверить резервирования майнинга: %w", err)
	}

	overview := a.miningOverview(ctx, true)
	if !overview.Available {
		return nil, "Агент майнинга недоступен: генерация запущена, но остановка майнинга не подтверждена.", nil
	}
	if !overview.Running || overview.Active == nil {
		return nil, "", nil
	}
	target := overview.Active
	lease := domain.QuickGenerationMiningLease{
		ID: newRequestID(), UserID: user.ID, MinerID: target.ID,
		ScriptPath: target.ScriptPath, ProcessName: target.ProcessName, ResumeMining: true,
	}
	if err := a.store.CreateQuickGenerationMiningLease(ctx, lease); err != nil {
		return nil, "", fmt.Errorf("создать резервирование майнинга: %w", err)
	}
	state, stopErr := a.mining.Stop(ctx, mining.Request{ScriptPath: target.ScriptPath, ProcessName: target.ProcessName})
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
	a.audit(ctx, &user.ID, "mining_paused_for_quick_generation", "miner", &target.ID, "", "", map[string]any{
		"lease_id": lease.ID, "username": user.Username, "process_name": target.ProcessName,
	})
	return &lease, "", nil
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

func (a *App) releaseMiningPauseForGeneration(ctx context.Context, promptID string) {
	lease, err := a.store.QuickGenerationMiningLeaseByPrompt(ctx, promptID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		log.Printf("load mining-pause lease for %s: %v", promptID, err)
		return
	}
	a.releaseMiningPause(ctx, lease.ID)
}

func (a *App) releaseMiningPause(ctx context.Context, leaseID string) {
	a.miningPauseMu.Lock()
	defer a.miningPauseMu.Unlock()
	lease, remaining, err := a.store.DeleteQuickGenerationMiningLease(ctx, leaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		log.Printf("remove mining-pause lease %s: %v", leaseID, err)
		return
	}
	if remaining > 0 || !lease.ResumeMining {
		return
	}
	overview := a.miningOverview(ctx, true)
	if !overview.Available {
		log.Printf("resume mining after quick generation: %s", overview.Message)
		a.restoreMiningPauseLease(ctx, lease)
		return
	}
	if overview.Running {
		return
	}
	state, err := a.mining.Start(ctx, mining.Request{ScriptPath: lease.ScriptPath, ProcessName: lease.ProcessName})
	if err != nil || !state.Running {
		if err != nil {
			log.Printf("resume mining after quick generation: %v", err)
		} else {
			log.Printf("resume mining after quick generation: miner did not start")
		}
		a.restoreMiningPauseLease(ctx, lease)
		return
	}
	a.audit(ctx, &lease.UserID, "mining_resumed_after_quick_generation", "miner", &lease.MinerID, "", "", map[string]any{
		"lease_id": lease.ID, "prompt_id": lease.PromptID, "process_name": lease.ProcessName,
	})
}

// restoreMiningPauseLease keeps a terminal lease durable when the agent cannot
// restart mining yet. The maintenance loop will retry without needing a user action.
func (a *App) restoreMiningPauseLease(ctx context.Context, lease domain.QuickGenerationMiningLease) {
	if err := a.store.CreateQuickGenerationMiningLease(ctx, lease); err != nil {
		log.Printf("restore mining-pause lease %s for retry: %v", lease.ID, err)
	}
}

// refreshQuickGenerationMiningLeases also covers a Gateway restart: leases
// are in PostgreSQL, so completed ComfyUI tasks still release the miner.
func (a *App) refreshQuickGenerationMiningLeases(ctx context.Context) {
	leases, err := a.store.ListQuickGenerationMiningLeases(ctx)
	if err != nil {
		log.Printf("list mining-pause leases: %v", err)
		return
	}
	for _, lease := range leases {
		if lease.PromptID == "" {
			if time.Since(lease.CreatedAt) > 2*time.Minute {
				a.releaseMiningPause(ctx, lease.ID)
			}
			continue
		}
		status, statusErr := a.fetchGenerationStatus(ctx, lease.PromptID, lease.UserID)
		if statusErr != nil {
			continue
		}
		if status.State == "completed" || status.State == "error" {
			a.releaseMiningPauseForGeneration(ctx, lease.PromptID)
		}
	}
}
