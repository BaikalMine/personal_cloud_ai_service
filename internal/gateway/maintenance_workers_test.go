package gateway

import (
	"context"
	"testing"
	"time"
)

func TestMaintenanceWorkersRunIndependentlyAndStop(t *testing.T) {
	registry := newMaintenanceRegistry(time.Now)
	slowStarted := make(chan struct{})
	fastFinished := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	specs := []maintenanceWorkerSpec{
		{
			Key: "slow", Name: "Slow", Interval: time.Hour, Timeout: time.Second,
			Run: func(ctx context.Context) (int64, error) {
				close(slowStarted)
				<-ctx.Done()
				return 0, ctx.Err()
			},
		},
		{
			Key: "fast", Name: "Fast", Interval: time.Hour, Timeout: time.Second,
			Run: func(context.Context) (int64, error) {
				close(fastFinished)
				return 3, nil
			},
		},
	}
	go func() {
		runMaintenanceWorkers(ctx, registry, specs)
		close(done)
	}()

	waitForSignal(t, slowStarted, "slow worker did not start")
	waitForSignal(t, fastFinished, "fast worker was blocked by slow worker")
	cancel()
	waitForSignal(t, done, "workers did not stop after context cancellation")

	states := registry.snapshot()
	if len(states) != 2 || states[0].Key != "slow" || states[1].Key != "fast" {
		t.Fatalf("worker order = %#v", states)
	}
	for _, state := range states {
		if state.Status != "stopped" || state.Running {
			t.Fatalf("worker %s did not stop: %+v", state.Key, state)
		}
	}
	if states[0].LastSuccessAt != nil {
		t.Fatalf("cancelled slow worker recorded a false success: %+v", states[0])
	}
	if states[1].LastSuccessAt == nil || states[1].LastItems != 3 {
		t.Fatalf("successful fast worker state was lost: %+v", states[1])
	}
}

func TestMaintenanceWorkerRecordsTimeoutAndBackoff(t *testing.T) {
	registry := newMaintenanceRegistry(time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	spec := maintenanceWorkerSpec{
		Key: "timeout", Name: "Timeout", Interval: time.Hour, Timeout: 20 * time.Millisecond,
		RetryDelay: time.Hour, MaxBackoff: time.Hour,
		Run: func(ctx context.Context) (int64, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		},
	}
	go func() {
		runMaintenanceWorkers(ctx, registry, []maintenanceWorkerSpec{spec})
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		states := registry.snapshot()
		if len(states) == 1 && states[0].Status == "retrying" {
			state := states[0]
			if state.ConsecutiveFailures != 1 || state.LastError != context.DeadlineExceeded.Error() {
				t.Fatalf("unexpected timeout state: %+v", state)
			}
			if state.NextRunAt == nil || time.Until(*state.NextRunAt) < 50*time.Minute {
				t.Fatalf("backoff was not recorded: %+v", state)
			}
			cancel()
			waitForSignal(t, done, "timed out worker did not stop")
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	waitForSignal(t, done, "timed out worker did not stop")
	t.Fatal("worker timeout was not recorded")
}

func TestMaintenanceRegistryRejectsOverlappingRun(t *testing.T) {
	registry := newMaintenanceRegistry(time.Now)
	registry.register(normalizedMaintenanceWorkerSpec(maintenanceWorkerSpec{Key: "only", Name: "Only"}))
	started, ok := registry.begin("only")
	if !ok {
		t.Fatal("first run was not claimed")
	}
	if _, ok := registry.begin("only"); ok {
		t.Fatal("overlapping run was claimed")
	}
	registry.finish("only", started, 1, nil, time.Now().Add(time.Minute))
	if _, ok := registry.begin("only"); !ok {
		t.Fatal("worker stayed locked after finishing")
	}
}

func TestMaintenanceWorkerBackoffIsBounded(t *testing.T) {
	spec := normalizedMaintenanceWorkerSpec(maintenanceWorkerSpec{RetryDelay: time.Second, MaxBackoff: 5 * time.Second})
	wants := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for index, want := range wants {
		if got := maintenanceWorkerBackoff(spec, index+1); got != want {
			t.Fatalf("attempt %d backoff = %s, want %s", index+1, got, want)
		}
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}
