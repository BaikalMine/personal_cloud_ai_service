package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/store"
)

func trainingStorageFixture(t *testing.T) (*sql.DB, *store.Store, int64, loratraining.StorageInventory) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, err := database.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	if _, err := db.Exec(`TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	var actor int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('storage-admin','hash','admin') RETURNING id`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	preview := loratraining.StorageInventory{ProtocolVersion: 1, CheckedAt: time.Now().UTC(), Complete: true, TotalItems: 1, Items: []loratraining.StorageItem{{ID: "training_cache:fixture", Kind: "training_cache", Label: "cache", State: "present", CanCleanup: true, Version: strings.Repeat("a", 64), SizeBytes: 42, Files: 2}}}
	return db, store.New(db), actor, preview
}

func trainingStorageResult(op store.TrainingStorageOperation) loratraining.StorageCleanupResult {
	now := time.Now().UTC()
	result := loratraining.StorageCleanupResult{ProtocolVersion: 1, ID: op.ID, RequestHash: op.RequestHash, State: "completed", StartedAt: now, FinishedAt: &now}
	for _, item := range op.Preview.Items {
		result.Items = append(result.Items, loratraining.StorageCleanupOutcome{ID: item.ID, Kind: item.Kind, Label: item.Label, Version: item.Version, Outcome: "deleted", FreedBytes: item.SizeBytes, DeletedFiles: item.Files})
	}
	return result
}

func TestTrainingStorageOperationAdmissionAndSettlement(t *testing.T) {
	db, r, actor, preview := trainingStorageFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	host := strings.Repeat("b", 64)
	op, err := r.CreateTrainingStorageOperation(ctx, actor, strings.Repeat("1", 32), host, preview)
	if err != nil || op.State != "preview" || !op.MatchesSelection([]string{"training_cache:fixture"}) {
		t.Fatal(op, err)
	}
	if replay, err := r.CreateTrainingStorageOperation(ctx, actor, op.ID, host, preview); err != nil || replay.RequestHash != op.RequestHash {
		t.Fatal(replay, err)
	}
	if _, _, err := r.DispatchTrainingStorageOperation(ctx, actor, op.ID, strings.Repeat("c", 64), op.RequestHash); !errors.Is(err, store.ErrTrainingStorageConflict) {
		t.Fatal("changed host", err)
	}
	var admitted atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, granted, err := r.DispatchTrainingStorageOperation(ctx, actor, op.ID, host, op.RequestHash)
			if granted {
				admitted.Add(1)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if admitted.Load() != 1 {
		t.Fatal("dispatch count", admitted.Load())
	}
	if active, err := r.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || !active {
		t.Fatal(active, err)
	}
	second, err := r.CreateTrainingStorageOperation(ctx, actor, strings.Repeat("2", 32), host, preview)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchTrainingStorageOperation(ctx, actor, second.ID, host, second.RequestHash); !errors.Is(err, store.ErrTrainingStorageBusy) {
		t.Fatal("overlapping cleanup", err)
	}
	work, err := r.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "storage-waiter", ResourceID: domain.GPUPrimaryResource, Kind: domain.GPUWorkTraining, JobKey: "storage-waiter", UserID: actor})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := r.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ObserveGPUResource(ctx, resource.ID, resource.Revision, "idle", "stale host observation", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if admission, err := r.AcquireGPUWork(ctx, work.ID, "lease", time.Minute); err != nil || admission.Granted || admission.WaitCode != "storage_cleanup" {
		t.Fatal(admission, err)
	}
	permit := domain.GPUExecutorPermitRequest{ID: strings.Repeat("d", 32), InstanceID: strings.Repeat("e", 32), Token: strings.Repeat("f", 64)}
	if _, err := r.ReserveGPUExecutorPermit(ctx, permit); !errors.Is(err, store.ErrGPUExecutorBusy) {
		t.Fatal("external permit crossed storage claim", err)
	}
	if err := r.MarkTrainingStorageUnconfirmed(ctx, op.ID, host, "host_unavailable"); err != nil {
		t.Fatal(err)
	}
	op, err = r.TrainingStorageOperation(ctx, op.ID)
	if err != nil || op.State != "unconfirmed" {
		t.Fatal(op, err)
	}
	result := trainingStorageResult(op)
	result.Items[0].FreedBytes++
	if _, err := r.SettleTrainingStorageOperation(ctx, op.ID, host, result); !errors.Is(err, store.ErrTrainingStorageInput) {
		t.Fatal("credited excess bytes", err)
	}
	result = trainingStorageResult(op)
	if _, err := db.Exec(`CREATE FUNCTION reject_storage_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='storage.training.settled' THEN RAISE EXCEPTION 'test failure'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER reject_storage_settlement BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_storage_settlement()`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SettleTrainingStorageOperation(ctx, op.ID, host, result); err == nil {
		t.Fatal("ignored settlement failure")
	}
	if _, err := db.Exec(`DROP TRIGGER reject_storage_settlement ON audit_log; DROP FUNCTION reject_storage_settlement()`); err != nil {
		t.Fatal(err)
	}
	if active, err := r.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || !active {
		t.Fatal("released gate without receipt", active, err)
	}
	errs = make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.SettleTrainingStorageOperation(ctx, op.ID, host, trainingStorageResult(op))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	history, err := r.TrainingStorageHistory(ctx, 0)
	if err != nil || history.Total != 2 || history.FreedBytes != 42 {
		t.Fatal(history, err)
	}
	var audits int
	if err := db.QueryRow(`SELECT count(*) FROM audit_log WHERE metadata->>'operation_id'=$1 AND action='storage.training.settled'`, op.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("duplicate audit", audits, err)
	}
	if active, err := r.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || active {
		t.Fatal(active, err)
	}
	resource, err = r.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ObserveGPUResource(ctx, resource.ID, resource.Revision, "idle", "fresh host observation", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if admission, err := r.AcquireGPUWork(ctx, work.ID, "lease", time.Minute); err != nil || !admission.Granted {
		t.Fatal("work did not resume after settlement", admission, err)
	}
	if _, _, err := r.DispatchTrainingStorageOperation(ctx, actor, second.ID, host, second.RequestHash); !errors.Is(err, store.ErrTrainingStorageBusy) {
		t.Fatal("cleanup crossed active GPU work", err)
	}
}

func TestTrainingStorageAdmissionRace(t *testing.T) {
	_, r, actor, preview := trainingStorageFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	host := strings.Repeat("b", 64)
	for i := 1; i <= 20; i++ {
		op, err := r.CreateTrainingStorageOperation(ctx, actor, fmt.Sprintf("%032d", i), host, preview)
		if err != nil {
			t.Fatal(err)
		}
		work, err := r.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: fmt.Sprintf("storage-race-%d", i), ResourceID: domain.GPUPrimaryResource, Kind: domain.GPUWorkTraining, JobKey: fmt.Sprintf("storage-race-%d", i), UserID: actor})
		if err != nil {
			t.Fatal(err)
		}
		resource, err := r.GPUResource(ctx, domain.GPUPrimaryResource)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.ObserveGPUResource(ctx, resource.ID, resource.Revision, "idle", "race fixture", 30*time.Second); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var claimed bool
		var admitted domain.GPUAdmission
		var dispatchErr, admitErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, claimed, dispatchErr = r.DispatchTrainingStorageOperation(ctx, actor, op.ID, host, op.RequestHash)
		}()
		go func() {
			defer wg.Done()
			<-start
			admitted, admitErr = r.AcquireGPUWork(ctx, work.ID, "race-lease", time.Minute)
		}()
		close(start)
		wg.Wait()
		if admitErr != nil || dispatchErr != nil && !errors.Is(dispatchErr, store.ErrTrainingStorageBusy) {
			t.Fatal(admitErr, dispatchErr)
		}
		if claimed == admitted.Granted {
			t.Fatal("admission did not choose exactly one owner", claimed, admitted, dispatchErr)
		}
		if claimed {
			if admitted.WaitCode != "storage_cleanup" {
				t.Fatal(admitted)
			}
			if _, err := r.SettleTrainingStorageOperation(ctx, op.ID, host, trainingStorageResult(op)); err != nil {
				t.Fatal(err)
			}
			if _, err := r.RequestGPUWorkCancellation(ctx, work.ID); err != nil {
				t.Fatal(err)
			}
		} else {
			resource, err := r.GPUResource(ctx, domain.GPUPrimaryResource)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.ReleaseGPUWork(ctx, work.ID, "race-lease", domain.GPUReleaseEvidence{ResourceRevision: resource.Revision, Code: "not_dispatched"}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.CancelTrainingStoragePreview(ctx, actor, op.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestTrainingStorageOperationOwnershipExpiryAndRetention(t *testing.T) {
	db, r, actor, preview := trainingStorageFixture(t)
	ctx := context.Background()
	host := strings.Repeat("b", 64)
	create := func(n int) store.TrainingStorageOperation {
		t.Helper()
		op, err := r.CreateTrainingStorageOperation(ctx, actor, fmt.Sprintf("%032d", n), host, preview)
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	op := create(1)
	var other int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('storage-other','hash','admin') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchTrainingStorageOperation(ctx, other, op.ID, host, op.RequestHash); !errors.Is(err, store.ErrTrainingStorageDenied) {
		t.Fatal("foreign confirmation", err)
	}
	if _, err := db.Exec(`UPDATE users SET role='user' WHERE id=$1`, actor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchTrainingStorageOperation(ctx, actor, op.ID, host, op.RequestHash); !errors.Is(err, store.ErrTrainingStorageDenied) {
		t.Fatal("revoked admin confirmation", err)
	}
	if _, err := db.Exec(`UPDATE users SET role='admin' WHERE id=$1`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE training_storage_operations SET expires_at=now()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchTrainingStorageOperation(ctx, actor, op.ID, host, op.RequestHash); !errors.Is(err, store.ErrTrainingStorageExpired) {
		t.Fatal("expired preview", err)
	}
	if _, err := r.PruneTrainingStorageOperations(ctx); err != nil {
		t.Fatal(err)
	}
	op, err := r.TrainingStorageOperation(ctx, op.ID)
	if err != nil || op.State != "expired" {
		t.Fatal(op, err)
	}
	op = create(2)
	if _, err := r.CancelTrainingStoragePreview(ctx, actor, op.ID); err != nil {
		t.Fatal(err)
	}
	if op, claimed, err := r.DispatchTrainingStorageOperation(ctx, actor, op.ID, host, op.RequestHash); err != nil || claimed || op.State != "cancelled" {
		t.Fatal(op, claimed, err)
	}
	op = create(3)
	op, claimed, err := r.DispatchTrainingStorageOperation(ctx, actor, op.ID, host, op.RequestHash)
	if err != nil || !claimed {
		t.Fatal(op, err)
	}
	if _, err := r.CancelTrainingStoragePreview(ctx, actor, op.ID); !errors.Is(err, store.ErrTrainingStorageConflict) {
		t.Fatal("cancel released a dispatched request", err)
	}
	if _, err := db.Exec(`UPDATE training_storage_operations SET created_at=now()-interval '100 days',dispatched_at=now()-interval '100 days' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PruneTrainingStorageOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, err := r.PendingTrainingStorageOperations(ctx); err != nil || len(pending) != 1 || pending[0].Request == nil {
		t.Fatal("pruned unresolved request", pending, err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id=$1`, actor); err != nil {
		t.Fatal(err)
	}
	if active, err := r.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || !active {
		t.Fatal("user deletion removed active gate", active, err)
	}
	if _, err := r.SettleTrainingStorageOperation(ctx, op.ID, host, trainingStorageResult(op)); err != nil {
		t.Fatal(err)
	}
	if history, err := r.TrainingStorageHistory(ctx, 0); err != nil || history.FreedBytes != 42 {
		t.Fatal("old operation recently settled was omitted", history, err)
	}
	if _, err := db.Exec(`UPDATE training_storage_operations SET finished_at=now()-interval '91 days' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PruneTrainingStorageOperations(ctx); err != nil {
		t.Fatal(err)
	}
	pruned, err := r.TrainingStorageOperation(ctx, op.ID)
	if err != nil || pruned.PrunedAt == nil || pruned.Request != nil || pruned.Result != nil || pruned.Preview != nil {
		t.Fatal(pruned, err)
	}
	if _, _, err := r.DispatchTrainingStorageOperation(ctx, other, op.ID, host, op.RequestHash); err == nil {
		t.Fatal("pruned operation reopened")
	}
	if _, err := r.CreateTrainingStorageOperation(ctx, other, op.ID, host, preview); err == nil {
		t.Fatal("retention allowed operation ID reuse")
	}
}
