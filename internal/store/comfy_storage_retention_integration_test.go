package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestComfyStorageRetentionAtomicClaimAndReplay(t *testing.T) {
	db, r, actor, _ := comfyOperationFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan store.ComfyStorageOperation, 16)
	errs := make(chan error, 16)
	for n := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			op, claimed, err := r.ClaimComfyStorageRetention(ctx, fmt.Sprintf("%032d", n+1), comfyOperationHost, comfyOperationStatus())
			if claimed {
				results <- op
			}
			if err != nil && !errors.Is(err, store.ErrComfyStorageBusy) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatal("execute grants", len(results))
	}
	op := <-results
	if op.Origin != "retention" || op.ActorID != nil || op.ActorName != "" || op.State != "dispatching" || op.ItemCount != 2 {
		t.Fatal(op)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM comfy_storage_operations`).Scan(&count); err != nil || count != 1 {
		t.Fatal("abandoned previews", count, err)
	}
	if _, claimed, err := r.ClaimComfyStorageRetention(ctx, op.ID, comfyOperationHost, comfyOperationStatus()); err != nil || claimed {
		t.Fatal("duplicate granted Execute", claimed, err)
	}
	if _, claimed, err := r.DispatchComfyStorageOperation(ctx, actor, op.ID, comfyOperationHost, op.RequestHash, comfyOperationStatus()); !errors.Is(err, store.ErrComfyStorageDenied) || claimed {
		t.Fatal("manual confirmation adopted system operation", claimed, err)
	}
	if _, err := r.SettleComfyStorageOperation(ctx, op.ID, comfyOperationHost, comfyOperationResult(op)); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := r.ClaimComfyStorageRetention(ctx, op.ID, comfyOperationHost, comfyOperationStatus()); err != nil || claimed {
		t.Fatal("completed ID replay", claimed, err)
	}
	if _, claimed, err := r.ClaimComfyStorageRetention(ctx, strings.Repeat("z", 32), comfyOperationHost, comfyOperationStatus()); err != nil || claimed {
		t.Fatal("empty registry claim", claimed, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM comfy_storage_operations`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestComfyStorageRetentionYieldsAndRollsBack(t *testing.T) {
	db, r, actor, _ := comfyOperationFixture(t)
	ctx := context.Background()
	work, err := r.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "retention-ready-work", Kind: domain.GPUWorkTraining, JobKey: "retention-ready-work", ResourceID: domain.GPUPrimaryResource, UserID: actor})
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("r", 32)
	if _, claimed, err := r.ClaimComfyStorageRetention(ctx, id, comfyOperationHost, comfyOperationStatus()); !errors.Is(err, store.ErrComfyStorageBusy) || claimed {
		t.Fatal("maintenance passed ready work", claimed, err)
	}
	if _, err := db.Exec(`UPDATE gpu_work SET ready_until=now()-interval '1 minute' WHERE id=$1`, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE FUNCTION fail_retention_confirm() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='storage.comfy.confirm' THEN RAISE EXCEPTION 'test rollback'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER fail_retention_confirm BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_retention_confirm()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DROP TRIGGER IF EXISTS fail_retention_confirm ON audit_log;DROP FUNCTION IF EXISTS fail_retention_confirm()`)
	})
	if _, claimed, err := r.ClaimComfyStorageRetention(ctx, id, comfyOperationHost, comfyOperationStatus()); err == nil || claimed {
		t.Fatal("claim despite failed audit", claimed, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM comfy_storage_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial preview survived rollback", count, err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_retention_confirm ON audit_log;DROP FUNCTION fail_retention_confirm()`); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := r.ClaimComfyStorageRetention(ctx, id, comfyOperationHost, comfyOperationStatus()); err != nil || !claimed {
		t.Fatal("expired queue intent blocked maintenance", claimed, err)
	}
}

func TestComfyStorageRetentionCandidateProtectionAndBound(t *testing.T) {
	db, r, actor, _ := comfyOperationFixture(t)
	ctx := context.Background()
	// Older ineligible rows must not crowd eligible rows out of the batch.
	for n := 2; n <= 61; n++ {
		referenceInput(t, db, actor, n, fmt.Sprintf("reserved-%d.png", n), "reserved", time.Now().Add(-2*time.Hour))
	}
	for n := 62; n <= 121; n++ {
		referenceInput(t, db, actor, n, fmt.Sprintf("stored-%d.png", n), "stored", time.Now().Add(-time.Hour))
	}
	if _, err := db.Exec(`UPDATE comfy_input_assets SET cleanup_retry_at=now()+interval '6 hours' WHERE filename='expired.png';
	 UPDATE comfy_output_cleanup_tombstones SET next_attempt_at=now()+interval '6 hours'`); err != nil {
		t.Fatal(err)
	}
	op, claimed, err := r.ClaimComfyStorageRetention(ctx, strings.Repeat("b", 32), comfyOperationHost, comfyOperationStatus())
	if err != nil || !claimed || op.ItemCount != 50 {
		t.Fatal(op, claimed, err)
	}
	for _, item := range op.Request.Items {
		if !strings.HasPrefix(item.File.Filename, "stored-") {
			t.Fatal("selected protected or deferred file", item)
		}
	}
	if _, err := r.SettleComfyStorageOperation(ctx, op.ID, comfyOperationHost, comfyOperationResult(op)); err != nil {
		t.Fatal(err)
	}
	op, claimed, err = r.ClaimComfyStorageRetention(ctx, strings.Repeat("c", 32), comfyOperationHost, comfyOperationStatus())
	if err != nil || !claimed || op.ItemCount != 10 {
		t.Fatal(op, claimed, err)
	}
}

func TestComfyStorageRetentionAmbiguousAndRetainedPaths(t *testing.T) {
	db, r, actor, _ := comfyOperationFixture(t)
	referenceInput(t, db, actor, 2, "EXPIRED.png", "stored", time.Now().Add(-time.Hour))
	var event int64
	if err := db.QueryRow(`INSERT INTO content_events(user_id,service,kind,prompt_cipher,response_cipher,metadata_cipher)
	 VALUES($1,'comfyui','comfyui_prompt','x','x','x') RETURNING id`, actor).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,subfolder,storage_type,payload_cipher,size_bytes,content_hash,expires_at)
	 VALUES($1,'image','image/png','EXPIRED-OUTPUT.png','OWNER','output','x',64,repeat('a',64),now()+interval '1 day')`, event); err != nil {
		t.Fatal(err)
	}
	if op, claimed, err := r.ClaimComfyStorageRetention(context.Background(), strings.Repeat("p", 32), comfyOperationHost, comfyOperationStatus()); err != nil || claimed || op.ID != "" {
		t.Fatal("selected ambiguous input or retained output", op, claimed, err)
	}
}

func TestComfyStorageRetentionReferencesAndEligibility(t *testing.T) {
	for _, mode := range []string{"reference", "unknown_manifest", "future_expiry"} {
		t.Run(mode, func(t *testing.T) {
			db, r, actor, _ := comfyOperationFixture(t)
			referenceInput(t, db, actor, 2, "unrelated.png", "stored", time.Now().Add(-time.Hour))
			want := 2
			if mode == "future_expiry" {
				if _, err := db.Exec(`UPDATE comfy_input_assets SET expires_at=now()+interval '1 hour' WHERE filename='expired.png'`); err != nil {
					t.Fatal(err)
				}
			} else {
				job := referenceJob(t, r, actor, mode)
				if mode == "unknown_manifest" {
					want = 1
				} else {
					if _, err := db.Exec(`UPDATE generation_jobs SET input_references_verified=true WHERE id=$1`, job.ID); err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec(`INSERT INTO generation_input_references(job_id,input_id,input_name) VALUES($1,$2,'owner/expired.png')`, job.ID, fmt.Sprintf("%032d", 1)); err != nil {
						t.Fatal(err)
					}
				}
			}
			op, claimed, err := r.ClaimComfyStorageRetention(context.Background(), strings.Repeat("t", 32), comfyOperationHost, comfyOperationStatus())
			if err != nil || !claimed || op.ItemCount != want {
				t.Fatal(op, claimed, err)
			}
			for _, item := range op.Request.Items {
				if item.File.Filename == "expired.png" || mode == "unknown_manifest" && item.File.StorageType == "input" {
					t.Fatal("protected input selected", item)
				}
			}
		})
	}
}
