package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestComfyInputCleanupReceiptsIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	r := store.New(db)
	due := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	makeItem := func(n int, state string) domain.ComfyInputCleanupResult {
		t.Helper()
		i := domain.ExpiredComfyInputAsset{ID: fmt.Sprintf("%032d", n), Filename: fmt.Sprintf("%d.png", n), Subfolder: "owner", StorageType: "input", SizeBytes: 42, ContentHash: strings.Repeat("a", 64), State: state}
		if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,filename,subfolder,size_bytes,content_hash,state,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, i.ID, i.Filename, i.Subfolder, i.SizeBytes, i.ContentHash, i.State, due); err != nil {
			t.Fatal(err)
		}
		return domain.ComfyInputCleanupResult{ExpiredComfyInputAsset: i, ObservedSizeBytes: i.SizeBytes, ObservedHash: i.ContentHash, Outcome: "deleted"}
	}
	item := makeItem(1, "reserved")
	item.ObservedSizeBytes = 35
	item.ObservedHash = strings.Repeat("b", 64)
	for index, status := range []string{"host_unavailable", "mismatched", "rejected", "unconfirmed"} {
		attempt := item
		attempt.Outcome = status
		got, err := r.RecordComfyInputCleanup(ctx, []domain.ComfyInputCleanupResult{attempt}, time.Hour)
		if err != nil || got.Deferred != 1 || got.Completed != 0 {
			t.Fatal(got, err)
		}
		var attempts int
		var outcome string
		var expires, last, next time.Time
		if err := db.QueryRow(`SELECT cleanup_attempts,cleanup_last_outcome,expires_at,cleanup_last_attempt_at,cleanup_retry_at FROM comfy_input_assets WHERE id=$1`, item.ID).Scan(&attempts, &outcome, &expires, &last, &next); err != nil || attempts != index+1 || outcome != status || !expires.Equal(due) || !next.After(last) {
			t.Fatal(attempts, outcome, expires, last, next, err)
		}
	}
	wrong := item
	wrong.SizeBytes++
	if _, err := r.RecordComfyInputCleanup(ctx, []domain.ComfyInputCleanupResult{wrong}, time.Hour); !errors.Is(err, store.ErrComfyCleanupResult) {
		t.Fatal("stale identity", err)
	}
	if _, err := r.RecordComfyInputCleanup(ctx, []domain.ComfyInputCleanupResult{item, item}, time.Hour); !errors.Is(err, store.ErrComfyCleanupResult) {
		t.Fatal("duplicate identity", err)
	}
	if _, err := db.Exec(`CREATE FUNCTION reject_input_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test receipt failure'; END $$;
	 CREATE TRIGGER reject_input_receipt BEFORE INSERT ON comfy_input_cleanup_receipts FOR EACH ROW EXECUTE FUNCTION reject_input_receipt()`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordComfyInputCleanup(ctx, []domain.ComfyInputCleanupResult{item}, time.Hour); err == nil {
		t.Fatal("ignored receipt failure")
	}
	if _, err := db.Exec(`DROP TRIGGER reject_input_receipt ON comfy_input_cleanup_receipts; DROP FUNCTION reject_input_receipt()`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM comfy_input_assets WHERE id=$1`, item.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("lost quota reservation", count, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.RecordComfyInputCleanup(ctx, []domain.ComfyInputCleanupResult{item}, time.Hour)
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
	receipts, err := r.ComfyInputCleanupReceipts(ctx, 100)
	if err != nil || len(receipts) != 1 || receipts[0].FreedBytes != 35 || receipts[0].SizeBytes != 42 || receipts[0].ObservedHash != item.ObservedHash || receipts[0].Attempts != 5 || !receipts[0].EligibleAt.Equal(due) {
		t.Fatal(receipts, err)
	}
	for n, status := range []string{"missing", "legacy_confirmed"} {
		other := makeItem(n+2, "stored")
		changed := other
		changed.ObservedSizeBytes++
		if _, err := r.RecordComfyInputCleanup(ctx, []domain.ComfyInputCleanupResult{changed}, time.Hour); !errors.Is(err, store.ErrComfyCleanupResult) {
			t.Fatal("stored identity changed", err)
		}
		other.Outcome = status
		if _, err := r.RecordComfyInputCleanup(ctx, []domain.ComfyInputCleanupResult{other}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	receipts, err = r.ComfyInputCleanupReceipts(ctx, 100)
	var total int64
	for _, receipt := range receipts {
		total += receipt.FreedBytes
	}
	if err != nil || len(receipts) != 3 || total != 35 {
		t.Fatal(receipts, err)
	}
	if _, err := db.Exec(`UPDATE comfy_input_cleanup_receipts SET confirmed_at=now()-interval '91 days' WHERE outcome='missing'`); err != nil {
		t.Fatal(err)
	}
	report, err := r.CleanupDatabaseRetention(ctx, domain.DatabaseRetentionCutoffs{}, 100, 1)
	if err != nil || report.DeletedRows["comfy_input_cleanup_receipts"] != 1 {
		t.Fatal(report, err)
	}
}

func TestComfyInputCleanupLargeInputIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	r := store.New(db)
	item := domain.ComfyInputCleanupResult{ExpiredComfyInputAsset: domain.ExpiredComfyInputAsset{
		ID: strings.Repeat("f", 32), Filename: "postprocess.mp4", StorageType: "input", SizeBytes: 512 << 20, ContentHash: strings.Repeat("a", 64), State: "stored",
	}, ObservedSizeBytes: 512 << 20, ObservedHash: strings.Repeat("a", 64), Outcome: "deleted"}
	if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,filename,size_bytes,content_hash,state,expires_at) VALUES($1,$2,$3,$4,'stored',now()-interval '1 hour')`, item.ID, item.Filename, item.SizeBytes, item.ContentHash); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := r.RecordComfyInputCleanup(ctx, []domain.ComfyInputCleanupResult{item}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM comfy_input_assets`).Scan(&count); err != nil || count != 0 {
		t.Fatal("large input still occupies quota", count, err)
	}
	receipts, err := r.ComfyInputCleanupReceipts(ctx, 100)
	if err != nil || len(receipts) != 1 || receipts[0].FreedBytes != 512<<20 {
		t.Fatal(receipts, err)
	}
	item.SizeBytes++
	item.ObservedSizeBytes++
	if _, err := r.RecordComfyInputCleanup(ctx, []domain.ComfyInputCleanupResult{item}, time.Hour); !errors.Is(err, store.ErrComfyCleanupResult) {
		t.Fatal("accepted oversized input", err)
	}
}
