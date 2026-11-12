package inference

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestOwnedWorkerRecoversNativeOllamaPermits(t *testing.T) {
	for _, kind := range []string{"same owner", "foreign owner", "current generation", "drain failed", "ComfyUI"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			resource, err := f.store.GPUResource(ctx, domain.GPUPrimaryResource)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.ReleaseGPUWork(ctx, f.work.ID, f.work.LeaseToken, domain.GPUReleaseEvidence{Code: "not_dispatched", ResourceRevision: resource.Revision}); err != nil {
				t.Fatal(err)
			}
			resource, err = f.store.GPUResource(ctx, domain.GPUPrimaryResource)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.ObserveGPUResource(ctx, resource.ID, resource.Revision, "idle", "native fixture", 10*time.Second); err != nil {
				t.Fatal(err)
			}
			old := ownedIdentity(1)
			if err = f.store.RegisterOwnedInferenceExecutor(ctx, old); err != nil {
				t.Fatal(err)
			}
			request := domain.GPUExecutorPermitRequest{Executor: domain.GPUExecutorOllama, ID: strings.Repeat("c", 32), InstanceID: strings.Repeat("d", 32), Token: strings.Repeat("e", 64), OwnerID: old.ID, OwnerGeneration: old.Generation, OwnerToken: old.Token}
			if kind == "ComfyUI" {
				request.Executor = domain.GPUExecutorComfyUI
				request.OwnerID, request.OwnerToken, request.OwnerGeneration = "", "", 0
			}
			if _, err = f.store.ReserveGPUExecutorPermit(ctx, request); err != nil {
				t.Fatal(err)
			}
			if started, err := f.store.StartGPUExecutorPermit(ctx, request); err != nil || !started {
				t.Fatalf("native start: %v %v", started, err)
			}
			r := &ownedFixture{owner: ownedIdentity(2)}
			switch kind {
			case "foreign owner":
				r.owner.ID = strings.Repeat("f", 64)
			case "current generation":
				r.owner = old
			case "drain failed":
				r.advanceErr = errors.New("native tree drain unconfirmed")
			}
			w := ownedWorker(t, f, r)
			worked, err := w.RunOne(ctx)
			if worked || (err != nil) != (kind == "drain failed") {
				t.Fatalf("recovery run: %v %v", worked, err)
			}
			permit, err := f.store.GPUExecutorPermit(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "same owner" {
				if permit.Active() || permit.CompletionCode != "owner_stopped" {
					t.Fatalf("owned permit not recovered: %+v", permit)
				}
			} else if !permit.Active() || permit.CompletionCode != "" {
				t.Fatalf("unrelated permit changed: %+v", permit)
			}
			if r.calls != 0 || r.advances != 1 || w.ownedReady() == (kind == "drain failed") {
				t.Fatalf("readiness or execution: %+v ready=%v", r, w.ownedReady())
			}
		})
	}
}
