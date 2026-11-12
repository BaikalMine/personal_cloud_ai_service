package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestGPUExecutorPermits(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	if _, err = db.ExecContext(ctx, `TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	s := store.New(db)
	if err = s.EnsurePrimaryGPUResource(ctx); err != nil {
		t.Fatal(err)
	}
	observe := func() {
		t.Helper()
		r, err := s.GPUResource(ctx, domain.GPUPrimaryResource)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ObserveGPUResource(ctx, r.ID, r.Revision, "idle", "fixture", 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	permit := domain.GPUExecutorPermitRequest{ID: strings.Repeat("a", 32), InstanceID: strings.Repeat("b", 32), Token: strings.Repeat("c", 64)}
	if _, err = s.ReserveGPUExecutorPermit(ctx, permit); !errors.Is(err, store.ErrGPUExecutorBusy) {
		t.Fatalf("unobserved external admitted: %v", err)
	}
	observe()
	if _, err = s.ReserveGPUExecutorPermit(ctx, permit); err != nil {
		t.Fatal(err)
	}
	if p, err := s.ReserveGPUExecutorPermit(ctx, permit); err != nil || p.State != "reserved" {
		t.Fatalf("repeat reserve: %+v %v", p, err)
	}
	wrong := permit
	wrong.Token = strings.Repeat("d", 64)
	if _, err = s.ReserveGPUExecutorPermit(ctx, wrong); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("changed identity: %v", err)
	}
	if started, err := s.StartGPUExecutorPermit(ctx, permit); err != nil || !started {
		t.Fatalf("start: %v %v", started, err)
	}
	if started, err := s.StartGPUExecutorPermit(ctx, permit); err != nil || started {
		t.Fatalf("duplicate started: %v %v", started, err)
	}
	var user int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('gate-user','disabled','admin') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	work, err := s.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "gate-work", ResourceID: domain.GPUPrimaryResource, Kind: domain.GPUWorkGeneration, JobKey: "gate-work", UserID: user})
	if err != nil {
		t.Fatal(err)
	}
	observe()
	admission, err := s.AcquireGPUWork(ctx, work.ID, "managed", time.Minute)
	if err != nil || admission.Granted || admission.WaitCode != "external_work" {
		t.Fatalf("stale idle bypassed gate: %+v %v", admission, err)
	}
	if err = s.FinishGPUExecutorPermit(ctx, wrong, "quiesced"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("foreign completion: %v", err)
	}
	if err = s.FinishGPUExecutorPermit(ctx, permit, "quiesced"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishGPUExecutorPermit(ctx, permit, "quiesced"); err != nil {
		t.Fatalf("repeat completion: %v", err)
	}
	if p, err := s.ReserveGPUExecutorPermit(ctx, permit); err != nil || p.Active() {
		t.Fatalf("closed permit reopened: %+v %v", p, err)
	}
	if started, err := s.StartGPUExecutorPermit(ctx, permit); err != nil || started {
		t.Fatalf("late start: %v %v", started, err)
	}
	observe()
	other := permit
	other.ID = strings.Repeat("e", 32)
	if _, err = s.ReserveGPUExecutorPermit(ctx, other); !errors.Is(err, store.ErrGPUExecutorBusy) {
		t.Fatalf("external overtook waiting Gateway job: %v", err)
	}
	admission, err = s.AcquireGPUWork(ctx, work.ID, "managed", time.Minute)
	if err != nil || !admission.Granted {
		t.Fatalf("managed claim: %+v %v", admission, err)
	}
	if _, err = s.SetGPUWorkPhase(ctx, work.ID, "managed", "dispatching", ""); err != nil {
		t.Fatal(err)
	}
	other.WorkID, other.WorkLease = work.ID, "managed"
	if _, err = s.ReserveGPUExecutorPermit(ctx, other); err != nil {
		t.Fatal(err)
	}
	if started, err := s.StartGPUExecutorPermit(ctx, other); err != nil || !started {
		t.Fatalf("managed start: %v %v", started, err)
	}
	r, _ := s.GPUResource(ctx, domain.GPUPrimaryResource)
	if _, err = s.ReleaseGPUWork(ctx, work.ID, "managed", domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: r.Revision}); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("history alone released active native gate: %v", err)
	}
	if err = s.FinishGPUExecutorPermit(ctx, other, "quiesced"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.GPUResource(ctx, domain.GPUPrimaryResource)
	if _, err = s.ReleaseGPUWork(ctx, work.ID, "managed", domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: r.Revision}); err != nil {
		t.Fatal(err)
	}
	late := other
	late.ID = strings.Repeat("f", 32)
	if _, err = s.ReserveGPUExecutorPermit(ctx, late); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("late native adoption: %v", err)
	}
}

func TestGPUExecutorPermitClaimRace(t *testing.T) {
	testGPUExecutorPermitClaimRace(t, domain.GPUExecutorComfyUI)
}

func TestOwnedOllamaGPUExecutorPermitClaimRace(t *testing.T) {
	testGPUExecutorPermitClaimRace(t, domain.GPUExecutorOllama)
}

func testGPUExecutorPermitClaimRace(t *testing.T, executor string) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	var user int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('permit-race','disabled','user') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	s := store.New(db)
	permit := domain.GPUExecutorPermitRequest{Executor: executor, ID: strings.Repeat("a", 32), InstanceID: strings.Repeat("b", 32), Token: strings.Repeat("c", 64)}
	if executor == domain.GPUExecutorOllama {
		owner := domain.GPUInferenceExecutor{ID: strings.Repeat("d", 64), Generation: 1, Token: strings.Repeat("e", 64)}
		if err = s.RegisterOwnedInferenceExecutor(ctx, owner); err != nil {
			t.Fatal(err)
		}
		permit.OwnerID, permit.OwnerGeneration, permit.OwnerToken = owner.ID, owner.Generation, owner.Token
	}
	type outcome struct {
		granted bool
		err     error
	}
	for i := 0; i < 30; i++ {
		if _, err = db.ExecContext(ctx, `TRUNCATE gpu_resources CASCADE`); err != nil {
			t.Fatal(err)
		}
		if err = s.EnsurePrimaryGPUResource(ctx); err != nil {
			t.Fatal(err)
		}
		r, err := s.GPUResource(ctx, domain.GPUPrimaryResource)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ObserveGPUResource(ctx, r.ID, r.Revision, "idle", "race fixture", 10*time.Second); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		native := make(chan outcome, 1)
		managed := make(chan outcome, 1)
		go func() {
			<-start
			_, err := s.ReserveGPUExecutorPermit(ctx, permit)
			native <- outcome{err == nil, err}
		}()
		go func() {
			<-start
			key := fmt.Sprintf("race-%d", i)
			work, err := s.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: key, Kind: domain.GPUWorkGeneration, JobKey: key, ResourceID: domain.GPUPrimaryResource, UserID: user})
			if err != nil {
				managed <- outcome{false, err}
				return
			}
			a, err := s.AcquireGPUWork(ctx, work.ID, "race-lease", time.Minute)
			managed <- outcome{a.Granted, err}
		}()
		close(start)
		n, m := <-native, <-managed
		if n.err != nil && !errors.Is(n.err, store.ErrGPUExecutorBusy) {
			t.Fatal(n.err)
		}
		if m.err != nil {
			t.Fatal(m.err)
		}
		if n.granted == m.granted {
			t.Fatalf("round %d: expected exactly one admission, native=%+v managed=%+v", i, n, m)
		}
	}
}
