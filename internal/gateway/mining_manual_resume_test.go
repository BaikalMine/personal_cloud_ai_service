package gateway

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/miningagent"
	"ai-access-gateway/internal/store"
)

func TestManualMiningResumeRequiresFreshIdleTelemetry(t *testing.T) {
	now := time.Now()
	metrics := mining.SystemMetrics{GPUAvailable: true, CollectedAt: now}
	if !idleMiningGPU(metrics, now) {
		t.Fatal("fresh idle sample rejected")
	}
	for _, sample := range []mining.SystemMetrics{
		{GPUAvailable: false, CollectedAt: now},
		{GPUAvailable: true, CollectedAt: now, GPUPercent: 1},
		{GPUAvailable: true, CollectedAt: now.Add(-time.Minute)},
		{GPUAvailable: true, CollectedAt: now.Add(time.Minute)},
	} {
		if idleMiningGPU(sample, now) {
			t.Fatal("busy, missing or stale sample allowed mining")
		}
	}
}

type manualResumeMinerFixture struct {
	fencedTestMiner
	request mining.Request
	load    atomic.Int64
}

func (c *manualResumeMinerFixture) State(context.Context, string) (mining.State, error) {
	state := mining.State{Available: true, Running: c.running.Load(), ScriptPath: c.request.ScriptPath, ProcessName: c.request.ProcessName, CollectedAt: time.Now()}
	if state.Running {
		state.PIDs = []int{12345}
	}
	return state, nil
}

func (c *manualResumeMinerFixture) Start(ctx context.Context, req mining.Request) (mining.State, error) {
	if req != c.request {
		return mining.State{}, errors.New("unexpected mining profile")
	}
	c.starts.Add(1)
	c.running.Store(true)
	return c.State(ctx, req.ProcessName)
}

func (c *manualResumeMinerFixture) Stop(ctx context.Context, req mining.Request) (mining.State, error) {
	if req != c.request {
		return mining.State{}, errors.New("unexpected mining profile")
	}
	c.running.Store(false)
	return c.State(ctx, req.ProcessName)
}

func (c *manualResumeMinerFixture) Inventory(ctx context.Context) (mining.Inventory, error) {
	state, _ := c.State(ctx, c.request.ProcessName)
	return mining.Inventory{CollectedAt: time.Now(), States: []mining.State{state}}, nil
}

func (c *manualResumeMinerFixture) System(context.Context) (mining.SystemMetrics, error) {
	return mining.SystemMetrics{CollectedAt: time.Now(), GPUAvailable: true, GPUPercent: float64(c.load.Load())}, nil
}

func TestManualMiningResumeAfterBusyGPU(t *testing.T) {
	f := gpuDispatchFixture(t)
	a := f.app
	ctx := context.Background()
	a.cfg.MiningFencedCommands = true
	a.cfg.ComfyUIUpstream = nil
	req := mining.Request{ScriptPath: `C:\Synthetic\manual-resume.bat`, ProcessName: "synthetic.exe"}
	c := &manualResumeMinerFixture{request: req}
	c.running.Store(true)
	const token = "synthetic-manual-resume-token-1234"
	agent, err := miningagent.NewFencedServer(token, c)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(agent.Handler())
	defer server.Close()
	base, _ := url.Parse(server.URL)
	a.mining = mining.NewClient(base, token)
	a.systemMonitor = a.mining
	id, err := a.store.CreateMiner(ctx, store.CreateMinerParams{
		Name: "Synthetic manual resume", ScriptPath: req.ScriptPath, ProcessName: req.ProcessName, Enabled: true, CreatedByUserID: f.user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	miner, err := a.store.MinerByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.stopMiningManually(ctx, miner); err != nil {
		t.Fatal(err)
	}
	restarted := &App{cfg: a.cfg, store: a.store, mining: a.mining, systemMonitor: a.systemMonitor}
	restarted.gpuCoordinator = &gpuCoordinator{app: restarted}
	pending, err := restarted.loadManualMiningResume()
	if err != nil || pending.MinerID != id || time.Until(pending.CheckAfter) < 9*time.Minute {
		t.Fatalf("restart lost timer: %+v %v", pending, err)
	}
	check := func(expected int64) {
		t.Helper()
		if got, err := restarted.refreshManualMiningResume(ctx); err != nil || got != expected {
			t.Fatalf("resume = %d, %v; want %d", got, err, expected)
		}
	}
	due := func() {
		t.Helper()
		pending.CheckAfter = time.Now().Add(-time.Second)
		if err := restarted.saveManualMiningResume(pending); err != nil {
			t.Fatal(err)
		}
	}
	check(0)
	c.load.Store(25)
	due()
	check(0)
	if next, err := restarted.loadManualMiningResume(); err != nil || time.Until(next.CheckAfter) < 9*time.Minute {
		t.Fatalf("busy GPU did not defer retry: %+v %v", next, err)
	}
	c.load.Store(0)
	if err := a.store.EnsurePrimaryGPUResource(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := a.store.EnqueueGPUWork(ctx, domain.GPUWorkRequest{
		ID: "synthetic-mining-resume-work", Kind: domain.GPUWorkAssistant, ResourceID: domain.GPUPrimaryResource,
		JobKey: "synthetic-mining-resume-work", UserID: f.user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	due()
	check(0)
	if c.starts.Load() != 0 {
		t.Fatal("mining started while waiting for GPU")
	}
	if _, err := a.store.RequestGPUWorkCancellation(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	due()
	check(1)
	check(0)
	if !c.running.Load() || c.starts.Load() != 1 {
		t.Fatal("same mining profile did not resume exactly once")
	}
	if _, err := restarted.loadManualMiningResume(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("confirmed start did not clear the timer", err)
	}
}
