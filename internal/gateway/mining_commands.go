package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
)

func (a *App) miningCommand(ctx context.Context, action string, request mining.Request, update *mining.UpdateRequest) (mining.CommandResult, error) {
	if action == "restart" {
		result, err := a.miningCommand(ctx, "stop", request, nil)
		if err != nil || !result.State.Available || result.State.Running {
			if err == nil {
				err = errors.New("остановка не подтверждена")
			}
			return result, fmt.Errorf("перезапуск отменён: %w", err)
		}
		result, err = a.miningCommand(ctx, "start", request, nil)
		if err != nil {
			return result, fmt.Errorf("майнер остановлен, но повторный запуск не подтверждён: %w", err)
		}
		if !result.State.Available || !result.State.Running {
			return result, errors.New("майнер остановлен, но повторный запуск не подтверждён")
		}
		return result, nil
	}
	if a.cfg.MiningFencedCommands {
		revision, err := a.store.NextMiningCommandRevision(ctx)
		if err != nil {
			return mining.CommandResult{}, err
		}
		return a.mining.Fenced(ctx, mining.FencedCommand{Revision: revision, Action: action, Miner: request, Update: update})
	}
	var result mining.CommandResult
	var err error
	switch action {
	case "start":
		result.State, err = a.mining.Start(ctx, request)
	case "stop":
		result.State, err = a.mining.Stop(ctx, request)
	case "update":
		var value mining.UpdateResult
		value, err = a.mining.Update(ctx, *update)
		result.Update = &value
	default:
		err = errors.New("mining command fencing is not enabled")
	}
	return result, err
}

func (a *App) stopMining(ctx context.Context, request mining.Request) (mining.State, error) {
	result, err := a.miningCommand(ctx, "stop", request, nil)
	return result.State, err
}

// Caller already owns the admission guard for the automatic last-lease resume.
func (a *App) resumeMiningCommand(ctx context.Context, request mining.Request) (mining.State, error) {
	result, err := a.miningCommand(ctx, "start", request, nil)
	if errors.Is(err, mining.ErrCommandNeedsStop) {
		// The agent rejected Start before execution. Reconcile the same profile
		// under the caller's GPU admission guard, never another miner or lease.
		result, err = a.miningCommand(ctx, "restart", request, nil)
	}
	return result.State, err
}

func (a *App) manualMiningCommand(ctx context.Context, action string, request mining.Request, update *mining.UpdateRequest) (mining.CommandResult, error) {
	var result mining.CommandResult
	run := func() error {
		miners, err := a.store.ListMiners(ctx, true)
		if err != nil {
			return err
		}
		found := false
		for _, miner := range miners {
			if strings.EqualFold(miner.ScriptPath, request.ScriptPath) && strings.EqualFold(miner.ProcessName, request.ProcessName) && ((action != "start" && action != "restart") || miner.Enabled) {
				found = true
				break
			}
		}
		if !found {
			return errors.New("профиль майнинга удалён или отключён")
		}
		if _, err := a.store.ActiveQuickGenerationMiningLease(ctx); !errors.Is(err, sql.ErrNoRows) {
			if err != nil {
				return err
			}
			return errors.New("GPU зарезервирован для нейросетевой задачи")
		}
		if _, err := a.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
			if err != nil {
				return err
			}
			return errors.New("GPU занят нейросетевой задачей")
		}
		if a.gpuCoordinator != nil {
			if err := a.gpuCoordinator.observe(ctx); err != nil {
				return err
			}
			resource, err := a.store.GPUResource(ctx, domain.GPUPrimaryResource)
			if err != nil {
				return err
			}
			if resource.Observation != "idle" {
				return errors.New("свободное состояние GPU не подтверждено")
			}
		}
		result, err = a.miningCommand(ctx, action, request, update)
		if err == nil && (action == "start" || action == "restart") && result.State.Available && result.State.Running && a.manualMiningResumeEnabled() {
			if clearErr := a.clearManualMiningResume(); clearErr != nil {
				log.Printf("clear manual mining resume after confirmed start: %v", clearErr)
			}
		}
		return err
	}
	err := a.store.WithGPUAdmissionGuard(ctx, run)
	return result, err
}

func (a *App) changeStoppedMiner(ctx context.Context, request mining.Request, change func() error) error {
	if !a.cfg.MiningFencedCommands {
		return change()
	}
	return a.store.WithGPUAdmissionGuard(ctx, func() error {
		result, err := a.miningCommand(ctx, "barrier", request, nil)
		if err != nil {
			return err
		}
		if result.State.Running {
			return errors.New("сначала остановите майнинг")
		}
		return change()
	})
}

// A newer acknowledged barrier closes delayed network Start/Update requests.
// It does not stop a running miner or change the user's mining-pause policy.
func (a *App) fenceMiningBeforeGPU(ctx context.Context, kind domain.GPUWorkKind, user *User) error {
	if !a.cfg.MiningFencedCommands {
		return nil
	}
	miners, err := a.store.ListMiners(ctx, true)
	if err != nil {
		return err
	}
	if len(miners) > 0 && !a.mining.Configured() {
		return &gpuAdmissionWait{message: "Windows-agent не подтвердил состояние майнинга."}
	}
	for _, miner := range miners {
		result, err := a.miningCommand(ctx, "barrier", mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}, nil)
		if err != nil {
			return &gpuAdmissionWait{message: "Ожидаем подтверждения завершения команд майнинга."}
		}
		if result.State.Running && kind != domain.GPUWorkTraining && !user.PauseMiningForQuickGeneration {
			return &gpuAdmissionWait{message: "GPU занят майнингом. Для этой учётной записи автоматическая остановка выключена."}
		}
	}
	return nil
}
