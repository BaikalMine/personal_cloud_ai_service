package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func comfyStorageDBFixture(t *testing.T) (*sql.DB, *store.Store, int64) {
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
	var user int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('storage-owner','hash','admin') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	return db, store.New(db), user
}

func TestComfyStorageInventoryIntegration(t *testing.T) {
	db, r, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	view, err := r.ComfyStorageInventory(ctx, store.ComfyStorageQuery{Limit: 50})
	if err != nil || view.Total != 0 || len(view.Items) != 0 || view.Summary.ReceiptCount != 0 {
		t.Fatal(view, err)
	}
	due := time.Now().UTC().Truncate(time.Microsecond).Add(-24 * time.Hour)
	for i, input := range []struct {
		name, state string
		size        int64
		expires     time.Time
	}{{"old-input.png", "stored", 42, due}, {"pending-upload.png", "reserved", 64, due.Add(time.Hour)}, {"kept-input.png", "stored", 100, due.Add(48 * time.Hour)}} {
		if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,user_id,filename,subfolder,size_bytes,content_hash,state,expires_at) VALUES($1,$2,$3,'owner',$4,repeat('a',64),$5,$6)`, fmt.Sprintf("%032d", i+1), user, input.name, input.size, input.state, input.expires); err != nil {
			t.Fatal(err)
		}
	}
	addOutput := func(owner int64, name string, archive bool) int64 {
		t.Helper()
		var event, ownership int64
		if err := db.QueryRow(`INSERT INTO content_events(user_id,service,kind,prompt_cipher,response_cipher,metadata_cipher) VALUES($1,'comfyui','comfyui_prompt','x','x','x') RETURNING id`, owner).Scan(&event); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`INSERT INTO comfy_output_ownership(event_id,user_id,prompt_id,filename,subfolder,storage_type,media_type) VALUES($1,$2,'prompt',$3,'owner','output','image') RETURNING id`, event, owner, name).Scan(&ownership); err != nil {
			t.Fatal(err)
		}
		if archive {
			if _, err := db.Exec(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,subfolder,storage_type,payload_cipher,size_bytes,content_hash) VALUES($1,'image','image/png',$2,'owner','output','x',128,repeat('b',64))`, event, name); err != nil {
				t.Fatal(err)
			}
		}
		return ownership
	}
	unknownID := addOutput(user, "unarchived.png", false)
	archivedID := addOutput(user, "archived.png", true)
	var cleanupID, jobID int64
	if err := db.QueryRow(`INSERT INTO comfy_output_cleanup_tombstones(filename,subfolder,storage_type,size_bytes,content_hash,eligible_at,next_attempt_at,attempt_count,last_outcome,cleanup_reason) VALUES('pending.png','owner','output',64,repeat('c',64),$1,now()+interval '6 hours',2,'mismatched','profile_hidden') RETURNING id`, due.Add(-time.Hour)).Scan(&cleanupID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO generation_jobs(user_id,public_id,correlation_id,request_id,state,template_id) VALUES($1,repeat('a',32),repeat('a',32),repeat('a',32),'completed','text-to-image') RETURNING id`, user).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO generation_postprocess_inputs(input_id,job_id,source_position,dispatch_token) VALUES($1,$2,0,repeat('d',32))`, fmt.Sprintf("%032d", 1), jobID); err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"deleted", "missing", "legacy_confirmed", "deleted"} {
		freed := 0
		if status == "deleted" {
			freed = 10
		}
		confirmed := time.Now().UTC()
		if i == 3 {
			confirmed = confirmed.Add(-91 * 24 * time.Hour)
		}
		if _, err := db.Exec(`INSERT INTO comfy_output_cleanup_receipts(tombstone_id,filename,subfolder,storage_type,size_bytes,content_hash,outcome,cleanup_reason,freed_bytes,attempt_count,queued_at,eligible_at,confirmed_at) VALUES($1,'past.png','owner','output',10,repeat('a',64),$2,'retention',$3,1,now(),now(),$4)`, 1000+i, status, freed, confirmed); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO comfy_input_cleanup_receipts(input_id,filename,subfolder,storage_type,size_bytes,content_hash,state,observed_size_bytes,observed_hash,outcome,freed_bytes,attempt_count,queued_at,eligible_at) VALUES(repeat('e',32),'past.png','owner','input',5,repeat('a',64),'stored',5,repeat('a',64),'deleted',5,1,now(),now())`); err != nil {
		t.Fatal(err)
	}
	view, err = r.ComfyStorageInventory(ctx, store.ComfyStorageQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	s := view.Summary
	if view.Total != 6 || len(view.Items) != 6 || s.InputRecords != 3 || s.InputExpectedBytes != 206 || s.OutputRecords != 3 || s.OutputExpectedBytes != 192 || s.UnknownSizeRecords != 1 || s.PendingRecords != 3 || s.PendingExpectedBytes != 170 || s.MismatchedRecords != 1 || s.RetryRecords != 1 || s.UnconfirmedUploads != 1 || s.ProtectedReferences != 1 || s.ReceiptCount != 4 || s.ConfirmedFreedBytes != 15 || s.MissingReceipts != 1 || s.LegacyReceipts != 1 || s.GenerationActive || s.ExecutionUnconfirmed {
		t.Fatalf("unexpected inventory: %+v", view)
	}
	if s.OldestEligibleAt == nil || !s.OldestEligibleAt.Equal(due.Add(-time.Hour)) {
		t.Fatal(s.OldestEligibleAt)
	}
	byKey := map[string]domain.ComfyStorageItem{}
	for _, item := range view.Items {
		byKey[item.Key] = item
	}
	unknown := byKey["output:"+strconv.FormatInt(unknownID, 10)]
	if unknown.SizeKnown || unknown.CanInspect || unknown.OwnerID == nil || *unknown.OwnerID != user {
		t.Fatal(unknown)
	}
	archived := byKey["output:"+strconv.FormatInt(archivedID, 10)]
	if !archived.CanInspect || archived.Pending || archived.SizeBytes != 128 || archived.OwnerName != "storage-owner" {
		t.Fatal(archived)
	}
	queued := byKey["cleanup:"+strconv.FormatInt(cleanupID, 10)]
	if queued.OwnerID != nil || queued.OwnerCount != 0 || !queued.ProfileHidden || !queued.CanInspect || queued.LastOutcome != "mismatched" {
		t.Fatal(queued)
	}
	for _, q := range []struct {
		query store.ComfyStorageQuery
		count int64
	}{{store.ComfyStorageQuery{Limit: 50, Kind: "input"}, 3}, {store.ComfyStorageQuery{Limit: 50, State: "pending"}, 3}, {store.ComfyStorageQuery{Limit: 50, State: "retained"}, 3}, {store.ComfyStorageQuery{Limit: 50, State: "retry"}, 1}, {store.ComfyStorageQuery{Limit: 50, State: "referenced"}, 1}, {store.ComfyStorageQuery{Limit: 50, State: "unconfirmed"}, 1}, {store.ComfyStorageQuery{Limit: 50, Search: "ARCHIVED.png"}, 2}, {store.ComfyStorageQuery{Limit: 50, Search: "%"}, 0}, {store.ComfyStorageQuery{Limit: 50, Keys: []string{queued.Key}}, 1}} {
		got, err := r.ComfyStorageInventory(ctx, q.query)
		if err != nil || got.Total != q.count || int64(len(got.Items)) != q.count || got.Summary.PendingRecords != 3 {
			t.Fatal(q, got, err)
		}
	}
	seen := map[string]bool{}
	for offset := 0; offset < 6; offset += 2 {
		page, err := r.ComfyStorageInventory(ctx, store.ComfyStorageQuery{Limit: 2, Offset: offset})
		if err != nil || page.Total != 6 || len(page.Items) != 2 {
			t.Fatal(page, err)
		}
		for _, item := range page.Items {
			if seen[item.Key] || byKey[item.Key].Version != item.Version {
				t.Fatal("unstable page", item)
			}
			seen[item.Key] = true
		}
	}
	expired, err := r.ExpiredComfyInputAssets(ctx, 50)
	if err != nil || len(expired) != 1 || expired[0].Filename != "pending-upload.png" {
		t.Fatal("reference inventory differs from cleanup policy", expired, err)
	}
	if _, err := db.Exec(`UPDATE generation_jobs SET state='running' WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	view, err = r.ComfyStorageInventory(ctx, store.ComfyStorageQuery{Limit: 50})
	if err != nil || !view.Summary.GenerationActive {
		t.Fatal(view, err)
	}
	for _, item := range view.Items {
		if !slices.Contains(item.Protection, "generation_active") {
			t.Fatal(item)
		}
	}
	if _, err := db.Exec(`UPDATE generation_jobs SET state='failed',submission_started_at=now() WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE generation_postprocess_inputs SET cleanup_blocked=false`); err != nil {
		t.Fatal(err)
	}
	view, err = r.ComfyStorageInventory(ctx, store.ComfyStorageQuery{Limit: 50})
	if err != nil || view.Summary.GenerationActive || !view.Summary.ExecutionUnconfirmed || view.Summary.ProtectedReferences != 3 {
		t.Fatal(view, err)
	}
	if _, err := db.Exec(`UPDATE generation_jobs SET submission_rejected_at=now() WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	expired, err = r.ExpiredComfyInputAssets(ctx, 50)
	if err != nil || len(expired) != 2 {
		t.Fatal(expired, err)
	}
	var other int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('other-owner','hash','user') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	newID := addOutput(other, "archived.png", false)
	view, err = r.ComfyStorageInventory(ctx, store.ComfyStorageQuery{Limit: 50, Keys: []string{"output:" + strconv.FormatInt(newID, 10)}})
	if err != nil || len(view.Items) != 1 || view.Items[0].OwnerID != nil || view.Items[0].OwnerCount != 2 || view.Items[0].OwnerName != "" {
		t.Fatal(view, err)
	}
	// Reusing a path with new bytes must not hide its current retained result
	// merely because a tombstone still refers to an older file at that path.
	if err := r.ScheduleComfyOutputCleanup(ctx, domain.ComfyOutputCleanupTombstone{Filename: "archived.png", Subfolder: "owner", StorageType: "output", SizeBytes: 50, ContentHash: strings.Repeat("d", 64)}, due); err != nil {
		t.Fatal(err)
	}
	view, err = r.ComfyStorageInventory(ctx, store.ComfyStorageQuery{Limit: 50, Search: "archived.png"})
	if err != nil || view.Total != 3 {
		t.Fatal(view, err)
	}
	if _, err := db.Exec(`INSERT INTO gpu_resources(id) VALUES($1)`, domain.GPUPrimaryResource); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gpu_executor_permits(id,resource_id,executor,instance_id,token_hash) VALUES(repeat('a',32),$1,'comfyui',repeat('b',32),repeat('c',64))`, domain.GPUPrimaryResource); err != nil {
		t.Fatal(err)
	}
	view, err = r.ComfyStorageInventory(ctx, store.ComfyStorageQuery{Limit: 50})
	if err != nil || !view.Summary.ExecutorOccupied || view.Summary.ExecutionUnconfirmed || view.Summary.GenerationActive {
		t.Fatal(view, err)
	}
	for _, item := range view.Items {
		if !slices.Contains(item.Protection, "executor_occupied") {
			t.Fatal(item)
		}
	}
}

func TestComfyStorageLongListIntegration(t *testing.T) {
	db, r, user := comfyStorageDBFixture(t)
	if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,user_id,filename,subfolder,size_bytes,content_hash,state,created_at,expires_at)
	 SELECT lpad(i::text,32,'0'),$1,'input-'||lpad(i::text,4,'0')||'.png','owner',i,repeat('a',64),'stored',now()-interval '2 days',now()-interval '1 day' FROM generate_series(1,225) i`, user); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for offset := 0; offset < 250; offset += 50 {
		view, err := r.ComfyStorageInventory(context.Background(), store.ComfyStorageQuery{Limit: 50, Offset: offset})
		want := 50
		if offset == 200 {
			want = 25
		}
		if err != nil || view.Total != 225 || len(view.Items) != want || view.Summary.PendingRecords != 225 || view.Summary.PendingExpectedBytes != 25425 {
			t.Fatal(view, err)
		}
		for _, item := range view.Items {
			if seen[item.Key] {
				t.Fatal("duplicate across pages", item.Key)
			}
			seen[item.Key] = true
		}
	}
	if len(seen) != 225 {
		t.Fatal(len(seen))
	}
	view, err := r.ComfyStorageInventory(context.Background(), store.ComfyStorageQuery{Limit: 50, Offset: 250})
	if err != nil || view.Total != 225 || len(view.Items) != 0 {
		t.Fatal(view, err)
	}
}
