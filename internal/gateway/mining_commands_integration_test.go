package gateway

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/miningagent"
	"ai-access-gateway/internal/store"
)

type fencedTestMiner struct {
	entered, release chan struct{}
	starts           atomic.Int32
	running          atomic.Bool
}

type recoverableTestMiner struct {
	fencedTestMiner
	fail        bool
	actions     []string
	stopUnknown bool
}

func (c *recoverableTestMiner) Start(ctx context.Context, request mining.Request) (mining.State, error) {
	c.actions = append(c.actions, "start")
	if c.fail {
		c.fail = false
		return mining.State{}, errors.New("fixture failed start")
	}
	return c.fencedTestMiner.Start(ctx, request)
}

func (c *recoverableTestMiner) Stop(ctx context.Context, request mining.Request) (mining.State, error) {
	c.actions = append(c.actions, "stop")
	if c.stopUnknown {
		return mining.State{}, errors.New("fixture stop unconfirmed")
	}
	return c.fencedTestMiner.Stop(ctx, request)
}

func TestMiningResumeReconcilesOnlyExplicitFailedStart(t *testing.T) {
	for _, stopUnknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "unknown-stop"}[stopUnknown], func(t *testing.T) {
			f := gpuDispatchFixture(t)
			f.app.cfg.MiningFencedCommands = true
			c := &recoverableTestMiner{fail: true, stopUnknown: stopUnknown}
			const token = "mining-recovery-fixture-not-production"
			agent, _ := miningagent.NewFencedServer(token, c)
			server := httptest.NewServer(agent.Handler())
			defer server.Close()
			base, _ := url.Parse(server.URL)
			f.app.mining = mining.NewClient(base, token)
			ctx := context.Background()
			request := mining.Request{ScriptPath: `C:\Mining\recovery.bat`, ProcessName: "recovery.exe"}
			if _, err := f.app.miningCommand(ctx, "start", request, nil); err == nil {
				t.Fatal("fixture start succeeded")
			}
			var state mining.State
			err := f.app.store.WithGPUAdmissionGuard(ctx, func() error {
				var err error
				state, err = f.app.resumeMiningCommand(ctx, request)
				return err
			})
			if stopUnknown {
				if err == nil || state.Running || len(c.actions) != 2 {
					t.Fatal("started without confirmed stop", c.actions, err)
				}
			} else if err != nil || !state.Running || len(c.actions) != 3 || c.actions[1] != "stop" || c.actions[2] != "start" {
				t.Fatal("recovery failed", c.actions, state, err)
			}
		})
	}
}

func TestLegacyMiningManualStartUsesGPUAdmissionGuard(t *testing.T) {
	f := gpuDispatchFixture(t)
	f.app.cfg.MiningFencedCommands = false
	c := &fencedTestMiner{}
	const token = "legacy-mining-fixture-token-not-production"
	agent, _ := miningagent.NewServer(token, c)
	server := httptest.NewServer(agent.Handler())
	defer server.Close()
	base, _ := url.Parse(server.URL)
	f.app.mining = mining.NewClient(base, token)
	ctx := context.Background()
	request := mining.Request{ScriptPath: `C:\Mining\legacy.bat`, ProcessName: "legacy.exe"}
	_, err := f.app.store.CreateMiner(ctx, store.CreateMinerParams{Name: "legacy fixture", ScriptPath: request.ScriptPath, ProcessName: request.ProcessName, Enabled: true, Default: true, CreatedByUserID: f.user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.manualMiningCommand(ctx, "start", request, nil); err != nil {
		t.Fatal(err)
	}
	if c.starts.Load() != 1 {
		t.Fatal("start not sent")
	}
	if _, err = f.app.stopMining(ctx, request); err != nil {
		t.Fatal(err)
	}
	work, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, "legacy-block-fixture", &f.user, "fixture-lease", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if work.ID == "" {
		t.Fatal("GPU fixture missing")
	}
	if _, err = f.app.manualMiningCommand(ctx, "start", request, nil); err == nil {
		t.Fatal("GPU work ignored")
	}
	if c.starts.Load() != 1 {
		t.Fatal("start sent while GPU reserved")
	}
}

func (c *fencedTestMiner) Start(context.Context, mining.Request) (mining.State, error) {
	c.starts.Add(1)
	if c.entered != nil {
		close(c.entered)
		<-c.release
	}
	c.running.Store(true)
	return mining.State{Running: true}, nil
}
func (c *fencedTestMiner) Stop(context.Context, mining.Request) (mining.State, error) {
	c.running.Store(false)
	return mining.State{}, nil
}
func (c *fencedTestMiner) State(context.Context, string) (mining.State, error) {
	return mining.State{Running: c.running.Load()}, nil
}
func (*fencedTestMiner) Script(context.Context, string) (mining.Script, error) {
	return mining.Script{}, nil
}
func (*fencedTestMiner) Update(context.Context, mining.UpdateRequest) (mining.UpdateResult, error) {
	return mining.UpdateResult{Success: true}, nil
}
func (*fencedTestMiner) System(context.Context) (mining.SystemMetrics, error) {
	return mining.SystemMetrics{}, nil
}

func TestFencedMiningDelayedStartBlocksGPUUntilReconciled(t *testing.T) {
	f := gpuDispatchFixture(t)
	f.app.cfg.MiningFencedCommands = true
	c := &fencedTestMiner{entered: make(chan struct{}), release: make(chan struct{})}
	const token = "mining-integration-token-not-production"
	agent, _ := miningagent.NewFencedServer(token, c)
	server := httptest.NewServer(agent.Handler())
	defer server.Close()
	base, _ := url.Parse(server.URL)
	f.app.mining = mining.NewClient(base, token)
	request := mining.Request{ScriptPath: `C:\Mining\test.bat`, ProcessName: "test.exe"}
	_, err := f.app.store.CreateMiner(context.Background(), store.CreateMinerParams{Name: "fenced fixture", ScriptPath: request.ScriptPath, ProcessName: request.ProcessName, Enabled: true, Default: true, CreatedByUserID: f.user.ID})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := f.app.manualMiningCommand(ctx, "start", request, nil); result <- err }()
	select {
	case <-c.entered:
	case <-time.After(3 * time.Second):
		close(c.release)
		t.Fatal("start not dispatched")
	}
	released := false
	defer func() {
		if !released {
			close(c.release)
		}
	}()
	if err = <-result; err == nil {
		t.Fatal("lost HTTP completion treated as confirmed")
	}
	// A replacement repository must allocate a newer database revision as well.
	f.app.store = store.New(f.db)
	admitCtx, stop := context.WithTimeout(context.Background(), 60*time.Millisecond)
	_, err = f.app.gpuCoordinator.admit(admitCtx, domain.GPUWorkAssistant, "fenced-assistant", &f.user, "fenced-token", time.Now())
	stop()
	if err == nil {
		t.Fatal("GPU admitted while start still executes")
	}
	if _, err = f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("GPU was granted", err)
	}
	close(c.release)
	released = true
	f.user.PauseMiningForQuickGeneration = false
	_, err = f.app.gpuCoordinator.admit(context.Background(), domain.GPUWorkAssistant, "fenced-assistant", &f.user, "fenced-token", time.Now())
	if err == nil {
		t.Fatal("running miner ignored for non-pausing user")
	}
	f.user.PauseMiningForQuickGeneration = true
	work, err := f.app.gpuCoordinator.admit(context.Background(), domain.GPUWorkAssistant, "fenced-assistant", &f.user, "fenced-token", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.app.pauseMiningForInference(context.Background(), &f.user, work); err != nil {
		t.Fatal(err)
	}
	if c.running.Load() {
		t.Fatal("miner was not stopped before inference")
	}
	if _, err = f.app.manualMiningCommand(context.Background(), "start", request, nil); err == nil {
		t.Fatal("manual start bypassed GPU lease")
	}
	if c.starts.Load() != 1 {
		t.Fatal("unexpected repeated start")
	}
}

func TestFencedMiningRejectsStartForRemovedProfile(t *testing.T) {
	f := gpuDispatchFixture(t)
	f.app.cfg.MiningFencedCommands = true
	c := &fencedTestMiner{}
	const token = "mining-integration-token-not-production"
	agent, _ := miningagent.NewFencedServer(token, c)
	server := httptest.NewServer(agent.Handler())
	defer server.Close()
	base, _ := url.Parse(server.URL)
	f.app.mining = mining.NewClient(base, token)
	ctx := context.Background()
	request := mining.Request{ScriptPath: `C:\Mining\test.bat`, ProcessName: "test.exe"}
	id, err := f.app.store.CreateMiner(ctx, store.CreateMinerParams{Name: "delete fixture", ScriptPath: request.ScriptPath, ProcessName: request.ProcessName, Enabled: true, Default: false, CreatedByUserID: f.user.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.app.manualMiningCommand(ctx, "start", request, nil)
	if err != nil || !result.State.Running {
		t.Fatal("start result lost", result, err)
	}
	if err = f.app.changeStoppedMiner(ctx, request, func() error { t.Fatal("running profile mutated"); return nil }); err == nil {
		t.Fatal("running miner passed mutation guard")
	}
	if _, err = f.app.stopMining(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err = f.app.changeStoppedMiner(ctx, request, func() error {
		deleted, err := f.app.store.DeleteMiner(ctx, id)
		if err == nil && !deleted {
			return errors.New("fixture profile was not deleted")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.manualMiningCommand(ctx, "start", request, nil); err == nil {
		t.Fatal("stale profile accepted")
	}
	if c.starts.Load() != 1 {
		t.Fatal("stale profile restarted miner")
	}
}
