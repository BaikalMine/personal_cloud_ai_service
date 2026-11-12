package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

const comfyOperationHost = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func comfyOperationStatus() updates.ComfyStorageStatus {
	return updates.ComfyStorageStatus{Protocol: updates.ComfyStorageProtocol, ExecutorID: strings.Repeat("c", 64), GuardJournalID: strings.Repeat("d", 32), GuardEnforced: true}
}

func comfyOperationFixture(t *testing.T) (*sql.DB, *store.Store, int64, []store.ComfyStorageRegistrySelection) {
	t.Helper()
	db, r, actor := comfyStorageDBFixture(t)
	referenceInput(t, db, actor, 1, "expired.png", "stored", time.Now().Add(-time.Hour))
	if _, err := db.Exec(`INSERT INTO comfy_output_cleanup_tombstones(filename,subfolder,storage_type,size_bytes,content_hash) VALUES('expired-output.png','owner','output',64,repeat('a',64))`); err != nil {
		t.Fatal(err)
	}
	return db, r, actor, comfyOperationSelection(t, r)
}

func comfyOperationSelection(t *testing.T, r *store.Store) []store.ComfyStorageRegistrySelection {
	t.Helper()
	view, err := r.ComfyStorageInventory(context.Background(), store.ComfyStorageQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	result := make([]store.ComfyStorageRegistrySelection, 0, len(view.Items))
	for _, item := range view.Items {
		if item.Pending {
			result = append(result, store.ComfyStorageRegistrySelection{Key: item.Key, Version: item.Version})
		}
	}
	return result
}

func createComfyOperation(t *testing.T, r *store.Store, actor int64, n int, selection []store.ComfyStorageRegistrySelection) store.ComfyStorageOperation {
	t.Helper()
	op, err := r.CreateComfyStorageOperation(context.Background(), actor, fmt.Sprintf("%032d", n), comfyOperationHost, comfyOperationStatus(), selection)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func comfyOperationResult(op store.ComfyStorageOperation) updates.ComfyStorageResult {
	now := time.Now().UTC()
	result := updates.ComfyStorageResult{Protocol: updates.ComfyStorageProtocol, ID: op.ID, ExecutorID: op.Request.ExecutorID, GuardJournalID: op.Request.GuardJournalID, RequestHash: op.RequestHash, State: "completed", MutationStarted: true, ExecutorFenced: true, GuardClosed: true, StartedAt: now, FinishedAt: &now}
	for _, item := range op.Request.Items {
		result.Items = append(result.Items, updates.ComfyStorageOutcome{Selection: item, Outcome: "deleted", FreedBytes: item.File.SizeBytes})
	}
	return result
}

func TestComfyStorageOperationAdmissionAndAtomicSettlement(t *testing.T) {
	db, r, actor, selection := comfyOperationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	op := createComfyOperation(t, r, actor, 1, selection)
	if op.State != "preview" || op.ItemCount != 2 || op.ExpectedBytes != 106 || !op.MatchesSelection(selection) {
		t.Fatal(op)
	}
	if op.Selection[0].Key != "cleanup:1" || op.Selection[0].HostKey != "output:1" || op.Selection[1].HostKey != "input:2" {
		t.Fatal(op.Selection)
	}
	if again := createComfyOperation(t, r, actor, 1, selection); again.RequestHash != op.RequestHash {
		t.Fatal(again)
	}
	var grants atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, granted, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus())
			if granted {
				grants.Add(1)
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
	if grants.Load() != 1 {
		t.Fatal("execute grants", grants.Load())
	}
	if blocked, err := r.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || !blocked {
		t.Fatal(blocked, err)
	}
	if due, err := r.ExpiredComfyInputAssets(ctx, 50); err != nil || len(due) != 0 {
		t.Fatal("claimed input offered to legacy worker", due, err)
	}
	if due, err := r.DueComfyOutputCleanup(ctx, 50); err != nil || len(due) != 0 {
		t.Fatal("claimed output offered to legacy worker", due, err)
	}
	if _, err := r.RecordComfyInputCleanup(ctx, []domain.ComfyInputCleanupResult{{ExpiredComfyInputAsset: domain.ExpiredComfyInputAsset{ID: fmt.Sprintf("%032d", 1), Filename: "expired.png", Subfolder: "owner", StorageType: "input", SizeBytes: 42, ContentHash: strings.Repeat("a", 64), State: "stored"}, Outcome: "unconfirmed"}}, time.Hour); !errors.Is(err, store.ErrComfyStorageBusy) {
		t.Fatal("legacy input settled active selection", err)
	}
	if _, err := r.RecordComfyOutputCleanup(ctx, []domain.ComfyOutputCleanupResult{{ComfyOutputCleanupTombstone: domain.ComfyOutputCleanupTombstone{ID: 1, Filename: "expired-output.png", Subfolder: "owner", StorageType: "output", SizeBytes: 64, ContentHash: strings.Repeat("a", 64)}, Outcome: "unconfirmed"}}, time.Hour); !errors.Is(err, store.ErrComfyStorageBusy) {
		t.Fatal("legacy output settled active selection", err)
	}
	permit := domain.GPUExecutorPermitRequest{ID: strings.Repeat("e", 32), InstanceID: strings.Repeat("f", 32), Token: strings.Repeat("a", 64)}
	if _, err := r.ReserveGPUExecutorPermit(ctx, permit); !errors.Is(err, store.ErrGPUExecutorBusy) {
		t.Fatal("permit passed cleanup", err)
	}
	work, err := r.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "comfy-storage-waiter", ResourceID: domain.GPUPrimaryResource, Kind: domain.GPUWorkTraining, JobKey: "comfy-storage-waiter", UserID: actor})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := r.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ObserveGPUResource(ctx, resource.ID, resource.Revision, "idle", "not proof", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if admission, err := r.AcquireGPUWork(ctx, work.ID, "lease", time.Minute); err != nil || admission.Granted || admission.WaitCode != "storage_cleanup" {
		t.Fatal(admission, err)
	}
	if err := r.MarkComfyStorageUnconfirmed(ctx, op.ID, comfyOperationHost, "host_unavailable"); err != nil {
		t.Fatal(err)
	}
	result := comfyOperationResult(op)
	result.GuardClosed = false
	if _, err := r.SettleComfyStorageOperation(ctx, op.ID, comfyOperationHost, result); !errors.Is(err, store.ErrComfyStorageInput) {
		t.Fatal("released before guard close", err)
	}
	result = comfyOperationResult(op)
	result.GuardJournalID = strings.Repeat("a", 32)
	if _, err := r.SettleComfyStorageOperation(ctx, op.ID, comfyOperationHost, result); err == nil {
		t.Fatal("accepted different journal")
	}
	if _, err := db.Exec(`CREATE FUNCTION reject_comfy_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='storage.comfy.settled' THEN RAISE EXCEPTION 'test rollback'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER reject_comfy_settlement BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_comfy_settlement()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DROP TRIGGER IF EXISTS reject_comfy_settlement ON audit_log;DROP FUNCTION IF EXISTS reject_comfy_settlement()`)
	})
	if _, err := r.SettleComfyStorageOperation(ctx, op.ID, comfyOperationHost, comfyOperationResult(op)); err == nil {
		t.Fatal("ignored persistence failure")
	}
	var count int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM comfy_input_cleanup_receipts)+(SELECT count(*) FROM comfy_output_cleanup_receipts)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial receipt commit", count, err)
	}
	if blocked, err := r.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || !blocked {
		t.Fatal("gate lost on rollback", blocked, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_comfy_settlement ON audit_log;DROP FUNCTION reject_comfy_settlement()`); err != nil {
		t.Fatal(err)
	}
	errs = make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.SettleComfyStorageOperation(ctx, op.ID, comfyOperationHost, comfyOperationResult(op))
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
	if blocked, err := r.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || blocked {
		t.Fatal(blocked, err)
	}
	settled, err := r.ComfyStorageOperation(ctx, op.ID)
	if err != nil || settled.State != "completed" || settled.FreedBytes != 106 || settled.DeletedFiles != 2 {
		t.Fatal(settled, err)
	}
	view, err := r.ComfyStorageInventory(ctx, store.ComfyStorageQuery{Limit: 50})
	if err != nil || view.Total != 0 || view.Summary.ReceiptCount != 2 || view.Summary.ConfirmedFreedBytes != 106 {
		t.Fatal(view, err)
	}
	if replay := createComfyOperation(t, r, actor, 1, selection); replay.State != "completed" {
		t.Fatal(replay)
	}
}

func TestComfyStorageOperationPartialAndExpiredEvidence(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			db, r, actor, selection := comfyOperationFixture(t)
			ctx := context.Background()
			op := createComfyOperation(t, r, actor, 1, selection)
			if _, granted, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); err != nil || !granted {
				t.Fatal(granted, err)
			}
			result := comfyOperationResult(op)
			wantFreed := int64(42)
			if expired {
				result.State = "expired"
				result.Items = nil
				wantFreed = 0
			} else {
				result.State = "partial"
				for i := range result.Items {
					if result.Items[i].Selection.File.StorageType == "output" {
						result.Items[i].Outcome = "changed"
						result.Items[i].FreedBytes = 0
					}
				}
			}
			settled, err := r.SettleComfyStorageOperation(ctx, op.ID, comfyOperationHost, result)
			if err != nil || settled.FreedBytes != wantFreed || settled.Active() {
				t.Fatal(settled, err)
			}
			var outcome string
			if err := db.QueryRow(`SELECT last_outcome FROM comfy_output_cleanup_tombstones WHERE id=1`).Scan(&outcome); err != nil || outcome == "pending" {
				t.Fatal(outcome, err)
			}
			if expired {
				var bytes int64
				if err := db.QueryRow(`SELECT sum(size_bytes) FROM comfy_input_assets`).Scan(&bytes); err != nil || bytes != 42 {
					t.Fatal("expired evidence released quota", bytes, err)
				}
				if settled.State != "closed_unconfirmed" {
					t.Fatal(settled.State)
				}
			}
		})
	}
}

func TestComfyStorageOperationTrainingMutualExclusion(t *testing.T) {
	for _, trainingFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(trainingFirst), func(t *testing.T) {
			_, r, actor, selection := comfyOperationFixture(t)
			ctx := context.Background()
			op := createComfyOperation(t, r, actor, 1, selection)
			preview := loratraining.StorageInventory{ProtocolVersion: 1, CheckedAt: time.Now(), Complete: true, TotalItems: 1, Items: []loratraining.StorageItem{{ID: "training_cache:fixture", Kind: "training_cache", Label: "cache", State: "present", CanCleanup: true, Version: strings.Repeat("a", 64), SizeBytes: 42, Files: 1}}}
			training, err := r.CreateTrainingStorageOperation(ctx, actor, strings.Repeat("2", 32), comfyOperationHost, preview)
			if err != nil {
				t.Fatal(err)
			}
			if trainingFirst {
				if _, granted, err := r.DispatchTrainingStorageOperation(ctx, actor, training.ID, comfyOperationHost, training.RequestHash); err != nil || !granted {
					t.Fatal(granted, err)
				}
				if _, _, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); !errors.Is(err, store.ErrComfyStorageBusy) {
					t.Fatal(err)
				}
			} else {
				if _, granted, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); err != nil || !granted {
					t.Fatal(granted, err)
				}
				if _, _, err := r.DispatchTrainingStorageOperation(ctx, actor, training.ID, comfyOperationHost, training.RequestHash); !errors.Is(err, store.ErrTrainingStorageBusy) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestComfyStorageOperationRevalidatesSelection(t *testing.T) {
	for _, change := range []string{"fingerprint", "expiry", "reference", "unknown_manifest", "ambiguous_path", "retained_output", "reserved"} {
		t.Run(change, func(t *testing.T) {
			db, r, actor, selection := comfyOperationFixture(t)
			ctx := context.Background()
			op := createComfyOperation(t, r, actor, 1, selection)
			switch change {
			case "fingerprint":
				if _, err := db.Exec(`UPDATE comfy_input_assets SET content_hash=repeat('f',64)`); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				if _, err := db.Exec(`UPDATE comfy_input_assets SET expires_at=now()+interval '1 hour'`); err != nil {
					t.Fatal(err)
				}
			case "reserved":
				if _, err := db.Exec(`UPDATE comfy_input_assets SET state='reserved'`); err != nil {
					t.Fatal(err)
				}
			case "ambiguous_path":
				referenceInput(t, db, actor, 2, "EXPIRED.png", "stored", time.Now().Add(time.Hour))
			case "unknown_manifest":
				referenceJob(t, r, actor, "unknown")
			case "reference":
				j := referenceJob(t, r, actor, "known")
				if _, err := db.Exec(`UPDATE generation_jobs SET input_references_verified=true WHERE id=$1;`, j.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO generation_input_references(job_id,input_id,input_name) VALUES($1,$2,'owner/expired.png')`, j.ID, fmt.Sprintf("%032d", 1)); err != nil {
					t.Fatal(err)
				}
			case "retained_output":
				var event int64
				if err := db.QueryRow(`INSERT INTO content_events(user_id,service,kind,prompt_cipher,response_cipher,metadata_cipher) VALUES($1,'comfyui','comfyui_prompt','x','x','x') RETURNING id`, actor).Scan(&event); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,subfolder,storage_type,payload_cipher,size_bytes,content_hash,expires_at) VALUES($1,'image','image/png','EXPIRED-OUTPUT.png','owner','output','x',64,repeat('a',64),now()+interval '1 hour')`, event); err != nil {
					t.Fatal(err)
				}
			}
			if _, granted, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); !errors.Is(err, store.ErrComfyStorageConflict) || granted {
				t.Fatal(granted, err)
			}
			if current, err := r.ComfyStorageOperation(ctx, op.ID); err != nil || current.State != "preview" {
				t.Fatal(current, err)
			}
		})
	}
}

func TestComfyStorageOperationOwnershipRetentionAndLateEvidence(t *testing.T) {
	db, r, actor, selection := comfyOperationFixture(t)
	ctx := context.Background()
	op := createComfyOperation(t, r, actor, 1, selection)
	var other int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('other-storage-admin','hash','admin') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchComfyStorageOperation(ctx, other, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); !errors.Is(err, store.ErrComfyStorageDenied) {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, actor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); !errors.Is(err, store.ErrComfyStorageDenied) {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET disabled=false WHERE id=$1`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE comfy_storage_operations SET expires_at=now()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); !errors.Is(err, store.ErrComfyStorageExpired) {
		t.Fatal(err)
	}
	if _, err := r.PruneComfyStorageOperations(ctx); err != nil {
		t.Fatal(err)
	}
	op = createComfyOperation(t, r, actor, 2, selection)
	if _, err := r.CancelComfyStoragePreview(ctx, actor, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, granted, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); err != nil || granted {
		t.Fatal(granted, err)
	}
	op = createComfyOperation(t, r, actor, 3, selection)
	if _, granted, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); err != nil || !granted {
		t.Fatal(granted, err)
	}
	if _, err := r.CancelComfyStoragePreview(ctx, actor, op.ID); !errors.Is(err, store.ErrComfyStorageConflict) {
		t.Fatal("cancel released admission", err)
	}
	if _, err := db.Exec(`UPDATE comfy_storage_operations SET created_at=now()-interval '100 days',dispatched_at=now()-interval '100 days' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PruneComfyStorageOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, err := r.PendingComfyStorageOperations(ctx); err != nil || len(pending) != 1 || pending[0].Request == nil {
		t.Fatal(pending, err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id=$1`, actor); err != nil {
		t.Fatal(err)
	}
	if blocked, err := r.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || !blocked {
		t.Fatal("deleted actor released gate", blocked, err)
	}
	if _, err := r.SettleComfyStorageOperation(ctx, op.ID, comfyOperationHost, comfyOperationResult(op)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE comfy_storage_operations SET finished_at=now()-interval '91 days' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PruneComfyStorageOperations(ctx); err != nil {
		t.Fatal(err)
	}
	pruned, err := r.ComfyStorageOperation(ctx, op.ID)
	if err != nil || pruned.PrunedAt == nil || pruned.Request != nil || pruned.Selection != nil || pruned.Result != nil {
		t.Fatal(pruned, err)
	}
	if _, err := r.CreateComfyStorageOperation(ctx, other, op.ID, comfyOperationHost, comfyOperationStatus(), selection); err == nil {
		t.Fatal("reused a closed ID")
	}
}

func TestComfyStorageOperationClaimBlocksNewReferences(t *testing.T) {
	db, r, actor, selection := comfyOperationFixture(t)
	ctx := context.Background()
	op := createComfyOperation(t, r, actor, 1, selection)
	if _, granted, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); err != nil || !granted {
		t.Fatal(granted, err)
	}
	// Even a renewed row cannot be adopted while its saved cleanup is active.
	if _, err := db.Exec(`UPDATE comfy_input_assets SET expires_at=now()+interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	j := referenceJob(t, r, actor, "during-cleanup")
	for _, names := range [][]string{nil, {"owner/expired.png"}} {
		if _, err := r.PrepareGenerationJob(ctx, j.ID, domain.PreparedGenerationJob{InputNames: names}); !errors.Is(err, store.ErrGenerationInputUnavailable) {
			t.Fatal(names, err)
		}
	}
	if _, err := r.PrepareGenerationJob(ctx, j.ID, domain.PreparedGenerationJob{InputNames: []string{}}); err != nil {
		t.Fatal("input-free work cannot wait in queue", err)
	}
}

func TestComfyStorageOperationConcurrentTrainingClaim(t *testing.T) {
	_, r, actor, selection := comfyOperationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	op := createComfyOperation(t, r, actor, 1, selection)
	preview := loratraining.StorageInventory{ProtocolVersion: 1, CheckedAt: time.Now(), Complete: true, TotalItems: 1, Items: []loratraining.StorageItem{{ID: "training_cache:race", Kind: "training_cache", Label: "cache", State: "present", CanCleanup: true, Version: strings.Repeat("a", 64), SizeBytes: 1, Files: 1}}}
	training, err := r.CreateTrainingStorageOperation(ctx, actor, strings.Repeat("3", 32), comfyOperationHost, preview)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type outcome struct {
		granted bool
		err     error
	}
	results := make(chan outcome, 2)
	go func() {
		<-start
		_, granted, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus())
		results <- outcome{granted, err}
	}()
	go func() {
		<-start
		_, granted, err := r.DispatchTrainingStorageOperation(ctx, actor, training.ID, comfyOperationHost, training.RequestHash)
		results <- outcome{granted, err}
	}()
	close(start)
	grants, busy := 0, 0
	for range 2 {
		result := <-results
		if result.granted {
			grants++
		}
		if errors.Is(result.err, store.ErrComfyStorageBusy) || errors.Is(result.err, store.ErrTrainingStorageBusy) {
			busy++
		} else if result.err != nil {
			t.Fatal(result.err)
		}
	}
	if grants != 1 || busy != 1 {
		t.Fatal("cross-kind admissions", grants, busy)
	}
}

func TestComfyStorageOperationWaitsForReferencePublication(t *testing.T) {
	db, r, actor, selection := comfyOperationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	op := createComfyOperation(t, r, actor, 1, selection)
	j := referenceJob(t, r, actor, "comfy-operation-race")
	if _, err := db.Exec(`UPDATE comfy_input_assets SET expires_at=now()+interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE FUNCTION pause_comfy_operation_reference() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF NEW.input_references_verified THEN PERFORM pg_advisory_xact_lock(743629837); END IF; RETURN NEW; END $$;
	 CREATE TRIGGER pause_comfy_operation_reference BEFORE UPDATE OF input_references_verified ON generation_jobs FOR EACH ROW EXECUTE FUNCTION pause_comfy_operation_reference()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DROP TRIGGER IF EXISTS pause_comfy_operation_reference ON generation_jobs;DROP FUNCTION IF EXISTS pause_comfy_operation_reference()`)
	})
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(743629837)`); err != nil {
		t.Fatal(err)
	}
	prepared := make(chan error, 1)
	go func() {
		_, err := r.PrepareGenerationJob(ctx, j.ID, domain.PreparedGenerationJob{InputNames: []string{"owner/expired.png"}})
		prepared <- err
	}()
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=743629837 AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	dispatched := make(chan error, 1)
	go func() {
		_, granted, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus())
		if granted {
			err = errors.New("cleanup crossed uncommitted reference")
		}
		dispatched <- err
	}()
	select {
	case err := <-dispatched:
		t.Fatal("dispatch escaped reference lock", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-prepared; err != nil {
		t.Fatal(err)
	}
	if err := <-dispatched; !errors.Is(err, store.ErrComfyStorageConflict) {
		t.Fatal(err)
	}
}
