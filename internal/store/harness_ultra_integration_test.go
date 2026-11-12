package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestHarnessUltraPhysicalAdmission(t *testing.T) {
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
	if _, err = db.ExecContext(ctx, `TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	s := store.New(db)
	var user int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('ultra-owner','disabled','admin') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	work, err := s.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "ultra-work", Kind: domain.GPUWorkHarnessUltra, JobKey: "ultra-job", ResourceID: domain.GPUPrimaryResource, UserID: user})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.GPUResource(ctx, work.ResourceID)
	if _, err = s.ObserveGPUResource(ctx, r.ID, r.Revision, "idle", "fixture", time.Second*10); err != nil {
		t.Fatal(err)
	}
	admission, err := s.AcquireGPUWork(ctx, work.ID, "ultra-owner-token", time.Minute)
	if err != nil || !admission.Granted {
		t.Fatal(admission, err)
	}
	work = admission.Work
	for _, ready := range []bool{false, true} {
		if err = s.SetGPUInferencePreparation(ctx, work.ID, work.LeaseToken, ready); err != nil {
			t.Fatal(err)
		}
	}
	work, err = s.BeginHarnessUltraDispatch(ctx, work.ID, work.LeaseToken, "10000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginHarnessUltraDispatch(ctx, work.ID, work.LeaseToken, work.ExternalID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("duplicate dispatch not fenced", err)
	}
	permit := domain.GPUExecutorPermitRequest{ID: strings.Repeat("a", 32), InstanceID: strings.Repeat("b", 32), Token: strings.Repeat("c", 64), WorkID: work.ID, WorkLease: work.LeaseToken}
	if _, err = s.ReserveGPUExecutorPermit(ctx, permit); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.StartGPUExecutorPermit(ctx, permit); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if settled, err := s.HarnessUltraPermitSettled(ctx, work); err != nil || settled {
		t.Fatal("running permit considered settled", err)
	}
	r, _ = s.GPUResource(ctx, work.ResourceID)
	if _, err = s.ReleaseGPUWork(ctx, work.ID, work.LeaseToken, domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: r.Revision}); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatal("active GPU released", err)
	}
	if err = s.FinishGPUExecutorPermit(ctx, permit, "quiesced"); err != nil {
		t.Fatal(err)
	}
	if settled, err := s.HarnessUltraPermitSettled(ctx, work); err != nil || !settled {
		t.Fatal("closed permit not settled", err)
	}
	r, _ = s.GPUResource(ctx, work.ResourceID)
	if _, err = s.ReleaseGPUWork(ctx, work.ID, work.LeaseToken, domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: r.Revision}); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM generation_jobs`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("ordinary generation history polluted", jobs, err)
	}
}
