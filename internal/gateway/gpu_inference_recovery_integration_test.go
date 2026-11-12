package gateway

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func inferenceRecoveryMiner(t *testing.T, f *dispatchFixture) int64 {
	t.Helper()
	id, err := f.app.store.CreateMiner(context.Background(), store.CreateMinerParams{
		Name: "Recovery fixture", ScriptPath: "fixture.cmd", ProcessName: "fixture.exe", CreatedByUserID: f.user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGPUInferenceReservationRecovery(t *testing.T) {
	for _, tc := range []struct {
		phase             string
		expired, released bool
	}{
		{"reserved", false, false}, {"reserved", true, true},
		{"preparing_resources", true, false},
		{"resources_ready", false, false}, {"resources_ready", true, true},
		{"dispatching", true, false}, {"running", true, false},
	} {
		name := tc.phase + "/live"
		if tc.expired {
			name = tc.phase + "/expired"
		}
		t.Run(name, func(t *testing.T) {
			f := gpuDispatchFixture(t)
			ctx := context.Background()
			work, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, "recovery", &f.user, "owner", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			minerID := inferenceRecoveryMiner(t, f)
			lease := domain.QuickGenerationMiningLease{ID: "linked", UserID: f.user.ID, MinerID: minerID, GPUWorkID: work.ID, ScriptPath: "fixture.cmd", ProcessName: "fixture.exe", ResumeMining: true}
			if tc.phase != "reserved" {
				if err = f.app.store.SetGPUInferencePreparation(ctx, work.ID, "owner", false); err != nil {
					t.Fatal(err)
				}
				if err = f.app.store.CreateGPUInferenceMiningLease(ctx, lease, "owner"); err != nil {
					t.Fatal(err)
				}
				if err = f.app.gpuCoordinator.dispatch(ctx, work); !errors.Is(err, store.ErrGPUWorkConflict) {
					t.Fatalf("dispatch entered incomplete resource preparation: %v", err)
				}
				if tc.phase != "preparing_resources" {
					if err = f.app.store.SetGPUInferencePreparation(ctx, work.ID, "owner", true); err != nil {
						t.Fatal(err)
					}
				}
				if tc.phase == "dispatching" || tc.phase == "running" {
					if err = f.app.gpuCoordinator.dispatch(ctx, work); err != nil {
						t.Fatal(err)
					}
				}
				if tc.phase == "running" {
					if _, err = f.app.store.SetGPUWorkPhase(ctx, work.ID, "owner", "running", "executor-id"); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Legacy anonymous leases cannot acquire evidence from an unrelated GPU job.
			legacy := lease
			legacy.ID, legacy.GPUWorkID = "legacy", ""
			if err = f.app.store.CreateQuickGenerationMiningLease(ctx, legacy); err != nil {
				t.Fatal(err)
			}
			if tc.expired {
				if _, err = f.db.Exec(`UPDATE gpu_work SET lease_until=now()-interval '1 minute' WHERE id=$1`, work.ID); err != nil {
					t.Fatal(err)
				}
			}
			restarted := &App{store: store.New(f.db)}
			restarted.gpuCoordinator = &gpuCoordinator{app: restarted}
			n, err := restarted.refreshManagedGPUWork(ctx)
			if err != nil || (n == 1) != tc.released {
				t.Fatalf("recovered=%d err=%v", n, err)
			}
			after, err := f.app.store.GPUWork(ctx, work.ID)
			if err != nil || (after.State == domain.GPUWorkReleased) != tc.released {
				t.Fatalf("reservation: %+v %v", after, err)
			}
			if tc.released {
				if after.InferenceCompletion != "request_not_dispatched" {
					t.Fatal("missing pre-dispatch proof", after)
				}
				if err = f.app.gpuCoordinator.dispatch(ctx, work); !errors.Is(err, store.ErrGPUWorkConflict) {
					t.Fatal("late dispatch accepted", err)
				}
				if err = f.app.store.SetGPUInferencePreparation(ctx, work.ID, "owner", false); !errors.Is(err, store.ErrGPUWorkConflict) {
					t.Fatal("late preparation accepted", err)
				}
			}
			leases, err := f.app.store.ListQuickGenerationMiningLeases(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, current := range leases {
				want := current.ID == "linked" && tc.released
				if current.ResumeReady != want {
					t.Fatalf("wrong mining evidence: %+v want=%v", current, want)
				}
			}
		})
	}
}

func TestGPUInferenceMiningLeaseRequiresLiveOwnership(t *testing.T) {
	f := gpuDispatchFixture(t)
	ctx := context.Background()
	work, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkCaption, "caption", &f.user, "owner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	lease := domain.QuickGenerationMiningLease{ID: "linked", UserID: f.user.ID, MinerID: inferenceRecoveryMiner(t, f), GPUWorkID: work.ID, ScriptPath: "fixture.cmd", ProcessName: "fixture.exe"}
	if err = f.app.store.CreateQuickGenerationMiningLease(ctx, lease); !errors.Is(err, store.ErrGPUWorkInput) {
		t.Fatal("unguarded creation allowed", err)
	}
	if err = f.app.store.CreateGPUInferenceMiningLease(ctx, lease, "owner"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatal("lease added outside preparation", err)
	}
	if err = f.app.store.SetGPUInferencePreparation(ctx, work.ID, "owner", false); err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.CreateGPUInferenceMiningLease(ctx, lease, "stale"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatal("stale token allowed", err)
	}
	foreign := lease
	foreign.UserID++
	if err = f.app.store.CreateGPUInferenceMiningLease(ctx, foreign, "owner"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatal("foreign owner allowed", err)
	}
	if _, err = f.db.Exec(`UPDATE gpu_work SET lease_until=now()-interval '1 minute' WHERE id=$1`, work.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.CreateGPUInferenceMiningLease(ctx, lease, "owner"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatal("expired token allowed", err)
	}
	leases, err := f.app.store.ListQuickGenerationMiningLeases(ctx)
	if err != nil || len(leases) != 0 {
		t.Fatalf("late lease survived: %+v %v", leases, err)
	}
}

func TestGPUInferenceAndMiningCompletionAreAtomic(t *testing.T) {
	f := gpuDispatchFixture(t)
	ctx := context.Background()
	work, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, "completed", &f.user, "owner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.SetGPUInferencePreparation(ctx, work.ID, "owner", false); err != nil {
		t.Fatal(err)
	}
	lease := domain.QuickGenerationMiningLease{ID: "linked", UserID: f.user.ID, MinerID: inferenceRecoveryMiner(t, f), GPUWorkID: work.ID, ScriptPath: "fixture.cmd", ProcessName: "fixture.exe"}
	if err = f.app.store.CreateGPUInferenceMiningLease(ctx, lease, "owner"); err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.SetGPUInferencePreparation(ctx, work.ID, "owner", true); err != nil {
		t.Fatal(err)
	}
	if err = f.app.gpuCoordinator.dispatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.RecordGPUInferenceCompletion(ctx, work.Kind, work.JobKey, "executor_terminal"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`CREATE FUNCTION reject_gpu_release_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='released' THEN RAISE EXCEPTION 'fixture release failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Exec(`DROP FUNCTION IF EXISTS reject_gpu_release_fixture() CASCADE`) })
	if _, err = f.db.Exec(`CREATE TRIGGER reject_gpu_release_fixture BEFORE UPDATE ON gpu_work FOR EACH ROW EXECUTE FUNCTION reject_gpu_release_fixture()`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.refreshManagedGPUWork(ctx); err == nil {
		t.Fatal("expected transactional release failure")
	}
	leases, err := f.app.store.ListQuickGenerationMiningLeases(ctx)
	if err != nil || len(leases) != 1 || leases[0].ResumeReady {
		t.Fatalf("partial mining completion: %+v %v", leases, err)
	}
	if _, err = f.db.Exec(`DROP FUNCTION reject_gpu_release_fixture() CASCADE`); err != nil {
		t.Fatal(err)
	}
	restarted := &App{store: store.New(f.db)}
	restarted.gpuCoordinator = &gpuCoordinator{app: restarted}
	if _, err = restarted.refreshManagedGPUWork(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("completed GPU still held", err)
	}
	leases, err = f.app.store.ListQuickGenerationMiningLeases(ctx)
	if err != nil || len(leases) != 1 || !leases[0].ResumeReady {
		t.Fatalf("mining completion not recovered: %+v %v", leases, err)
	}
}
