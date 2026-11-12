package inference

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
	"ai-access-gateway/internal/store"
)

type ownedFixture struct {
	owner           domain.GPUInferenceExecutor
	result          modelruntime.Result
	err             error
	advanceErr      error
	calls, advances int
}

func (r *ownedFixture) Advance(context.Context) (domain.GPUInferenceExecutor, error) {
	r.advances++
	return r.owner, r.advanceErr
}
func (r *ownedFixture) Execute(_ context.Context, body []byte) (modelruntime.Result, error) {
	r.calls++
	if !bytes.Equal(body, testRequest) {
		return modelruntime.Result{}, errors.New("changed model request")
	}
	result := r.result
	result.Body = bytes.Clone(result.Body)
	return result, r.err
}
func (*ownedFixture) Close() error { return nil }

func ownedIdentity(generation int64) domain.GPUInferenceExecutor {
	return domain.GPUInferenceExecutor{ID: strings.Repeat("a", 64), Generation: generation, Token: strings.Repeat("b", 64)}
}

func ownedWorker(t *testing.T, f *fixture, r *ownedFixture) *Worker {
	t.Helper()
	w, err := NewOwnedWorker(f.store, f.cipher, r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	return w
}

func TestOwnedExecutionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result modelruntime.Result
		err    error
		code   string
	}{
		{"complete", modelruntime.Result{Status: 200, Body: testResponse, Dispatched: true, Stopped: true}, nil, "executor_terminal"},
		{"stopped", modelruntime.Result{Dispatched: true, Stopped: true}, errors.New("lost connection"), "executor_stopped"},
		{"invalid", modelruntime.Result{Status: 200, Body: []byte(`{}`), Dispatched: true, Stopped: true}, nil, "executor_stopped"},
		{"not sent", modelruntime.Result{Stopped: true}, errors.New("startup failed"), "request_not_dispatched"},
		{"unconfirmed", modelruntime.Result{Status: 200, Body: testResponse, Dispatched: true}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ownedFixture{result: tc.result, err: tc.err}
			w := &Worker{owned: &ownedExecution{runtime: r, executor: ownedIdentity(1)}}
			w.owned.ready.Store(true)
			_, body, code := w.executeOwned(context.Background(), testRequest)
			if code != tc.code || (code != "executor_terminal" && len(body) != 0) {
				t.Fatalf("result %q %s", code, body)
			}
			if code == "" && (w.ownedReady() || w.owned.executor.ID != "") {
				t.Fatal("unconfirmed stop left executor ready")
			}
		})
	}
}

func TestOwnedInferenceCompletion(t *testing.T) {
	f := newFixture(t)
	job := f.submit(t)
	r := &ownedFixture{owner: ownedIdentity(1), result: modelruntime.Result{Status: 200, Body: testResponse, Dispatched: true, Stopped: true}}
	w := ownedWorker(t, f, r)
	if worked, err := w.RunOne(context.Background()); err != nil || !worked {
		t.Fatalf("run: %v %v", worked, err)
	}
	saved, body, err := NewClient(f.store, f.cipher).Wait(context.Background(), job.ID, job.LeaseToken)
	if err != nil || !bytes.Equal(body, testResponse) || saved.ExecutorID != r.owner.ID || saved.ExecutorGeneration != 1 {
		t.Fatalf("saved: %+v %s %v", saved, body, err)
	}
	if worked, err := w.RunOne(context.Background()); err != nil || worked || r.calls != 1 || r.advances != 1 {
		t.Fatalf("duplicate: %v %v %+v", worked, err, r)
	}
}

func TestOwnedInferenceRecoveryScope(t *testing.T) {
	for _, kind := range []string{"same owner", "direct", "different owner", "current generation"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			job := f.submit(t)
			old := ownedIdentity(1)
			if kind == "different owner" {
				old.ID = strings.Repeat("c", 64)
			}
			if kind == "current generation" {
				old.Generation = 2
			}
			var claimed domain.GPUInference
			var err error
			if kind == "direct" {
				claimed, err = f.store.ClaimGPUInference(ctx, "old-request")
			} else {
				if err = f.store.RegisterOwnedInferenceExecutor(ctx, old); err != nil {
					t.Fatal(err)
				}
				claimed, err = f.store.ClaimOwnedGPUInference(ctx, "old-request", old)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.store.FinishGPUInference(ctx, job.ID, claimed.RunToken, "", 0, nil); err != nil {
				t.Fatal(err)
			}
			r := &ownedFixture{owner: ownedIdentity(2)}
			w := ownedWorker(t, f, r)
			if worked, err := w.RunOne(ctx); err != nil || worked {
				t.Fatalf("recovery run: %v %v", worked, err)
			}
			saved, err := f.store.GPUInference(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "same owner" {
				if saved.State != "stopped" || saved.CompletionCode != "executor_stopped" {
					t.Fatalf("not recovered: %+v", saved)
				}
				if _, _, err = NewClient(f.store, f.cipher).Wait(ctx, job.ID, job.LeaseToken); !errors.Is(err, ErrStopped) {
					t.Fatalf("client: %v", err)
				}
				work, _ := f.store.GPUWork(ctx, job.ID)
				resource, _ := f.store.GPUResource(ctx, work.ResourceID)
				if _, err = f.store.ReleaseGPUWork(ctx, job.ID, job.LeaseToken, domain.GPUReleaseEvidence{Code: "executor_stopped", ResourceRevision: resource.Revision}); err != nil {
					t.Fatal(err)
				}
			} else if saved.State != "uncertain" || saved.CompletionCode != "" {
				t.Fatalf("unrelated call settled: %+v", saved)
			}
			if r.calls != 0 || !w.ownedReady() {
				t.Fatalf("recovery dispatched or not ready: %+v", r)
			}
		})
	}
}

func TestOwnedInferenceGenerationFence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.submit(t)
	newOwner := ownedIdentity(20)
	if err := f.store.RegisterOwnedInferenceExecutor(ctx, newOwner); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RegisterOwnedInferenceExecutor(ctx, newOwner); err != nil {
		t.Fatalf("idempotent register: %v", err)
	}
	for _, owner := range []domain.GPUInferenceExecutor{ownedIdentity(19), {ID: newOwner.ID, Generation: 20, Token: strings.Repeat("d", 64)}} {
		if err := f.store.RegisterOwnedInferenceExecutor(ctx, owner); !errors.Is(err, store.ErrGPUWorkConflict) {
			t.Fatalf("stale register: %v", err)
		}
		if _, err := f.store.ClaimOwnedGPUInference(ctx, "stale", owner); !errors.Is(err, store.ErrGPUWorkConflict) {
			t.Fatalf("stale claim: %v", err)
		}
	}
	if _, err := f.store.ClaimOwnedGPUInference(ctx, "new", newOwner); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedInferenceRequiresDrain(t *testing.T) {
	f := newFixture(t)
	f.submit(t)
	r := &ownedFixture{owner: ownedIdentity(30), advanceErr: errors.New("drain unavailable")}
	w := ownedWorker(t, f, r)
	if _, err := w.RunOne(context.Background()); err == nil || w.ownedReady() || r.calls != 0 {
		t.Fatalf("unsafe readiness: %v %+v", err, r)
	}
	saved, err := f.store.GPUInference(context.Background(), f.work.ID)
	if err != nil || saved.State != "queued" {
		t.Fatalf("claimed without drain: %+v %v", saved, err)
	}
}

func TestOwnedInferenceRestoresReceiptBeforeStopped(t *testing.T) {
	for _, code := range []string{"executor_terminal", "executor_stopped"} {
		t.Run(code, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			job := f.submit(t)
			if err := f.store.RegisterOwnedInferenceExecutor(ctx, ownedIdentity(1)); err != nil {
				t.Fatal(err)
			}
			claimed, err := f.store.ClaimOwnedGPUInference(ctx, "old-receipt", ownedIdentity(1))
			if err != nil {
				t.Fatal(err)
			}
			r := &ownedFixture{owner: ownedIdentity(2)}
			w := ownedWorker(t, f, r)
			outcome := observedOutcome{Version: 1, ID: job.ID, RunToken: claimed.RunToken, LeaseToken: job.LeaseToken, RequestHash: job.RequestHash, Code: code, ObservedAt: time.Now().UTC()}
			if code == "executor_terminal" {
				outcome.Status = 200
				outcome.Response, err = f.cipher.EncryptBytesWithAAD(testResponse, responseAAD(job.ID))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = w.journal.save(outcome); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err = w.RunOne(ctx); err != nil {
					t.Fatal(err)
				}
				saved, err := f.store.GPUInference(ctx, job.ID)
				if err != nil || saved.CompletionCode != code || r.calls != 0 {
					t.Fatalf("receipt replaced: %+v %v calls=%d", saved, err, r.calls)
				}
			}
			if code == "executor_terminal" {
				_, body, err := NewClient(f.store, f.cipher).Wait(ctx, job.ID, job.LeaseToken)
				if err != nil || !bytes.Equal(body, testResponse) {
					t.Fatalf("lost receipt body: %s %v", body, err)
				}
			}
		})
	}
}
