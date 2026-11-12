package inference

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
)

type cancellingRuntime struct {
	started       chan struct{}
	stopConfirmed bool
}

func (*cancellingRuntime) Advance(context.Context) (domain.GPUInferenceExecutor, error) {
	return ownedIdentity(1), nil
}
func (r *cancellingRuntime) Execute(ctx context.Context, _ []byte) (modelruntime.Result, error) {
	close(r.started)
	<-ctx.Done()
	return modelruntime.Result{Dispatched: true, Stopped: r.stopConfirmed}, ctx.Err()
}
func (*cancellingRuntime) Close() error { return nil }

func TestOwnedInferenceCancellationInterruptsExecution(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		name := "unconfirmed stop"
		if confirmed {
			name = "confirmed stop"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			job := f.submit(t)
			runtime := &cancellingRuntime{started: make(chan struct{}), stopConfirmed: confirmed}
			worker, err := NewOwnedWorker(f.store, f.cipher, runtime, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := worker.RunOne(ctx); done <- err }()
			await(t, runtime.started)
			if _, err = f.store.CancelGPUInference(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation waited for model completion")
			}
			saved, err := f.store.GPUInference(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if confirmed {
				if saved.State != "stopped" || saved.CompletionCode != "executor_stopped" {
					t.Fatalf("missing physical stop evidence: %+v", saved)
				}
				_, _, err = NewClient(f.store, f.cipher).Wait(ctx, job.ID, job.LeaseToken)
				if !errors.Is(err, ErrStopped) {
					t.Fatalf("client outcome: %v", err)
				}
			} else if saved.State != "uncertain" || saved.Settled() || worker.ownedReady() {
				t.Fatalf("unconfirmed cancellation released execution: %+v", saved)
			}
		})
	}
}

func TestInferenceCancellationClaimFence(t *testing.T) {
	f := newFixture(t)
	job := f.submit(t)
	ctx := context.Background()
	claimed, err := f.store.ClaimGPUInference(ctx, "cancellation-fence")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.CancelGPUInference(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := f.store.GPUInferenceCancellation(ctx, job.ID, claimed.RunToken); err != nil || !cancelled {
		t.Fatal(cancelled, err)
	}
	if _, err = f.store.GPUInferenceCancellation(ctx, job.ID, "stale-token"); err == nil {
		t.Fatal("stale claim read cancellation")
	}
}
