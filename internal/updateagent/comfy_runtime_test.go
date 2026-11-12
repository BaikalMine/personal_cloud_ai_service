package updateagent

import (
	"ai-access-gateway/internal/updates"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type runtimeFixtureBackend struct {
	sync.Mutex
	processes     []comfyProcess
	starts, stops int
	delay         time.Duration
}

func (b *runtimeFixtureBackend) Inspect(context.Context, ComfyTarget) ([]comfyProcess, error) {
	b.Lock()
	defer b.Unlock()
	time.Sleep(b.delay)
	return append([]comfyProcess{}, b.processes...), nil
}
func (b *runtimeFixtureBackend) Start(ComfyTarget, string, func(comfyProcess, uint32)) error {
	b.Lock()
	defer b.Unlock()
	b.starts++
	b.processes = []comfyProcess{{42, 123456}}
	return nil
}
func (b *runtimeFixtureBackend) Stop(context.Context, ComfyTarget, comfyProcess) error {
	b.Lock()
	defer b.Unlock()
	b.stops++
	b.processes = nil
	return nil
}
func (b *runtimeFixtureBackend) Watch(comfyProcess, func(uint32)) error { return nil }
func runtimeFixture(t *testing.T, queue string) (*ControllerImpl, *runtimeFixtureBackend) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/queue" {
			if queue == "unavailable" {
				http.Error(w, "offline", 503)
				return
			}
			w.Write([]byte(queue))
			return
		}
		http.Error(w, "not ready", 503)
	}))
	t.Cleanup(server.Close)
	b := &runtimeFixtureBackend{}
	return &ControllerImpl{config: Config{ComfyUI: ComfyTarget{WorkingDirectory: t.TempDir(), HealthURL: server.URL + "/"}}, client: server.Client(), runtimeBackend: b}, b
}
func runtimeRequest(action, version string) updates.ComfyRuntimeRequest {
	return updates.ComfyRuntimeRequest{ID: strings.Repeat("a", 32), Action: action, Version: version, ExpiresAt: time.Now().Add(20 * time.Second)}
}
func TestRuntimeReceiptsPreventReplayAndDoNotWaitForStartup(t *testing.T) {
	c, b := runtimeFixture(t, `{"queue_running":[],"queue_pending":[]}`)
	r := runtimeRequest("start", "stopped")
	result, err := c.ComfyRuntimeCommand(context.Background(), r)
	if err != nil || result.Receipt.State != "completed" || !result.Running || result.Ready || b.starts != 1 {
		t.Fatal(result, err)
	}
	result, err = c.ComfyRuntimeCommand(context.Background(), r)
	if err != nil || b.starts != 1 || result.Receipt.State != "completed" {
		 t.Fatal("command replayed", result, err)
	}
	c2, b2 := runtimeFixture(t, `{"queue_running":[],"queue_pending":[]}`)
	if err := c2.saveRuntimeJSON(r.ID+".json", updates.ComfyRuntimeReceipt{ID: r.ID, Action: r.Action, State: "unconfirmed"}); err != nil {
		t.Fatal(err)
	}
	result, err = c2.ComfyRuntimeCommand(context.Background(), r)
	if err != nil || b2.starts != 0 || result.Receipt.State != "unconfirmed" {
		t.Fatal("uncertain command replayed", result, err)
	}
}
func TestRuntimeRejectsBusyAndStaleProcessEvenWhenForced(t *testing.T) {
	c, b := runtimeFixture(t, `{"queue_running":[[]],"queue_pending":[]}`)
	b.processes = []comfyProcess{{42, 1}}
	r := runtimeRequest("restart", b.processes[0].version())
	r.Force = true
	if _, err := c.ComfyRuntimeCommand(context.Background(), r); !errors.Is(err, ErrComfyStorageBusy) || b.stops != 0 || b.starts != 0 {
		t.Fatal(err)
	}
	r.Version = strings.Repeat("b", 64)
	if _, err := c.ComfyRuntimeCommand(context.Background(), r); err == nil || b.stops != 0 {
		t.Fatal("stale identity accepted")
	}
}
func TestRuntimeUnavailableQueueNeedsExplicitForce(t *testing.T) {
	c, b := runtimeFixture(t, "unavailable")
	b.processes = []comfyProcess{{42, 1}}
	r := runtimeRequest("stop", b.processes[0].version())
	if _, err := c.ComfyRuntimeCommand(context.Background(), r); !errors.Is(err, ErrComfyStorageBusy) {
		t.Fatal(err)
	}
	r.Force = true
	result, err := c.ComfyRuntimeCommand(context.Background(), r)
	if err != nil || b.stops != 1 || result.Running || result.Receipt.State != "completed" {
		t.Fatal(result, err)
	}
}
func TestRuntimeDeadlinePreventsLateLaunch(t *testing.T) {
	c, b := runtimeFixture(t, `{"queue_running":[],"queue_pending":[]}`)
	b.delay = 10 * time.Millisecond
	r := runtimeRequest("start", "stopped")
	r.ExpiresAt = time.Now().Add(5 * time.Millisecond)
	_, _ = c.ComfyRuntimeCommand(context.Background(), r)
	if b.starts != 0 || b.stops != 0 {
		t.Fatal("late mutation")
	}
}
func TestRuntimeMultipleProcessesAndConcurrentUpdateFailClosed(t *testing.T) {
	c, b := runtimeFixture(t, `{"queue_running":[],"queue_pending":[]}`)
	b.processes = []comfyProcess{{42, 1}, {43, 2}}
	s, err := c.ComfyRuntimeStatus(context.Background(), "")
	if err != nil || s.State != "ambiguous" || s.Version != "" {
		t.Fatal(s, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.ComfyRuntimeCommand(context.Background(), runtimeRequest("start", "stopped")); !errors.Is(err, ErrComfyStorageBusy) {
		t.Fatal(err)
	}
}
