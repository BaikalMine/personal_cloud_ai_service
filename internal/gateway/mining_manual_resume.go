package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/store"
)

const manualMiningResumeInterval = 10 * time.Minute
const manualMiningResumeStartupMessage = "manual mining auto-resume enabled; idle check interval=10m"

type manualMiningResume struct {
	MinerID     int64
	ScriptPath  string
	ProcessName string
	CheckAfter  time.Time
}

func (a *App) manualMiningResumeEnabled() bool {
	return !a.cfg.RestoreDrillMode && filepath.IsAbs(a.cfg.MediaSpoolDir) &&
		a.cfg.MiningFencedCommands &&
		a.gpuCoordinator != nil && a.systemMonitor != nil && a.systemMonitor.Configured() &&
		a.mining != nil && a.mining.Configured()
}

func (a *App) manualMiningResumePending(overview MiningOverview) bool {
	if overview.Running || !a.manualMiningResumeEnabled() {
		return false
	}
	_, err := a.loadManualMiningResume()
	return err == nil
}

func (a *App) manualMiningResumePath() string {
	return filepath.Join(a.cfg.MediaSpoolDir, "manual-mining-resume.json")
}

func (a *App) loadManualMiningResume() (manualMiningResume, error) {
	var pending manualMiningResume
	data, err := os.ReadFile(a.manualMiningResumePath())
	if err != nil {
		return pending, err
	}
	if len(data) > 8192 || json.Unmarshal(data, &pending) != nil ||
		pending.MinerID <= 0 || pending.ScriptPath == "" || pending.ProcessName == "" || pending.CheckAfter.IsZero() {
		return manualMiningResume{}, errors.New("invalid manual mining resume record")
	}
	return pending, nil
}

func (a *App) saveManualMiningResume(pending manualMiningResume) error {
	file, err := os.CreateTemp(a.cfg.MediaSpoolDir, ".manual-mining-resume-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(pending)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return os.Rename(file.Name(), a.manualMiningResumePath())
}

func (a *App) clearManualMiningResume() error {
	err := os.Remove(a.manualMiningResumePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Report read-only and settled errors without leaving an uncertain GPU fence.
func (a *App) withManualMiningResumeGuard(ctx context.Context, run func(func(error) error) error) error {
	var definite error
	err := a.store.WithGPUAdmissionGuard(ctx, func() error {
		return run(func(err error) error { definite = err; return nil })
	})
	if err != nil {
		return err
	}
	return definite
}

func (a *App) logManualMiningResumeStartup() {
	if a.manualMiningResumeEnabled() {
		log.Print(manualMiningResumeStartupMessage)
	}
}

func (a *App) stopMiningManually(ctx context.Context, miner domain.Miner) (mining.State, error) {
	req := mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}
	if !a.manualMiningResumeEnabled() {
		return a.stopMining(ctx, req)
	}
	var state mining.State
	err := a.withManualMiningResumeGuard(ctx, func(definite func(error) error) error {
		overview := a.miningOverview(ctx, true, false)
		if !overview.Available || !overview.InventoryKnown || overview.Active == nil ||
			overview.Active.ID != miner.ID || !sameMiningScript(overview.Active.State.ScriptPath, miner.ScriptPath) {
			return definite(errors.New("активный профиль майнинга не подтверждён"))
		}
		var err error
		state, err = a.stopMining(ctx, req)
		if err != nil {
			return err
		}
		if !state.Available || state.Running {
			return definite(errors.New("остановка майнинга не подтверждена"))
		}
		if !overview.Active.Enabled {
			return definite(a.clearManualMiningResume())
		}
		return definite(a.saveManualMiningResume(manualMiningResume{
			MinerID: miner.ID, ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName,
			CheckAfter: time.Now().Add(manualMiningResumeInterval),
		}))
	})
	return state, err
}

func idleMiningGPU(metrics mining.SystemMetrics, now time.Time) bool {
	age := now.Sub(metrics.CollectedAt)
	return metrics.GPUAvailable && age >= -5*time.Second && age <= 15*time.Second && metrics.GPUPercent == 0
}

func (a *App) refreshManualMiningResume(ctx context.Context) (int64, error) {
	if !a.manualMiningResumeEnabled() {
		return 0, nil
	}
	pending, err := a.loadManualMiningResume()
	if errors.Is(err, os.ErrNotExist) || err == nil && time.Now().Before(pending.CheckAfter) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var started int64
	err = a.withManualMiningResumeGuard(ctx, func(definite func(error) error) error {
		// A second instance or a manual command may have replaced the timer.
		pending, err := a.loadManualMiningResume()
		if errors.Is(err, os.ErrNotExist) || err == nil && time.Now().Before(pending.CheckAfter) {
			return nil
		}
		if err != nil {
			return definite(err)
		}
		miner, err := a.store.MinerByID(ctx, pending.MinerID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (!miner.Enabled ||
			!sameMiningScript(miner.ScriptPath, pending.ScriptPath) || !strings.EqualFold(miner.ProcessName, pending.ProcessName)) {
			return definite(a.clearManualMiningResume())
		}
		if err != nil {
			return definite(err)
		}
		pending.CheckAfter = time.Now().Add(manualMiningResumeInterval)
		if err := a.saveManualMiningResume(pending); err != nil {
			return definite(err)
		}
		overview := a.miningOverview(ctx, true, false)
		if !overview.Available || !overview.InventoryKnown {
			return nil
		}
		if overview.Running {
			return definite(a.clearManualMiningResume())
		}
		blocked, err := a.store.ManualMiningResumeBlocked(ctx)
		if err != nil || blocked {
			return definite(err)
		}
		metrics, err := a.systemMonitor.System(ctx)
		if err != nil || !idleMiningGPU(metrics, time.Now()) {
			return definite(err)
		}
		if err := a.gpuCoordinator.observe(ctx); err != nil {
			return definite(err)
		}
		resource, err := a.store.GPUResource(ctx, domain.GPUPrimaryResource)
		if err != nil || resource.Observation != "idle" || !resource.ValidUntil.After(time.Now()) || !idleMiningGPU(metrics, time.Now()) {
			return definite(err)
		}
		req := mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}
		result, err := a.miningCommand(ctx, "start", req, nil)
		if err != nil {
			if errors.Is(err, store.ErrGPUExecutorBusy) || errors.Is(err, store.ErrGPUWorkConflict) {
				return nil
			}
			return err
		}
		if !result.State.Available || !result.State.Running || !sameMiningScript(result.State.ScriptPath, req.ScriptPath) {
			return errors.New("manual mining auto-resume outcome is unconfirmed")
		}
		started = 1
		a.audit(ctx, nil, "mining_resumed_after_manual_stop", "miner", &miner.ID, "", "", nil)
		return definite(a.clearManualMiningResume())
	})
	if errors.Is(err, store.ErrGPUWorkConflict) {
		return 0, nil
	}
	return started, err
}
