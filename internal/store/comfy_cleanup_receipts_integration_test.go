package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestComfyCleanupReceiptsIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	item := domain.ComfyOutputCleanupTombstone{Filename: "result.png", Subfolder: "owner", StorageType: "output", SizeBytes: 42, ContentHash: strings.Repeat("a", 64)}
	dueAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := r.ScheduleComfyOutputCleanup(ctx, item, dueAt); err != nil {
		t.Fatal(err)
	}
	due, err := r.DueComfyOutputCleanup(ctx, 100)
	if err != nil || len(due) != 1 {
		t.Fatal(due, err)
	}
	item = due[0]
	for index, outcome := range []string{"host_unavailable", "mismatched", "rejected", "unconfirmed"} {
		got, err := r.RecordComfyOutputCleanup(ctx, []domain.ComfyOutputCleanupResult{{ComfyOutputCleanupTombstone: item, Outcome: outcome}}, time.Hour)
		if err != nil || got.Deferred != 1 || got.Completed != 0 {
			t.Fatal("defer", got, err)
		}
		var attempts int
		var status string
		var eligible, last, next time.Time
		if err := db.QueryRow(`SELECT attempt_count,last_outcome,eligible_at,last_attempt_at,next_attempt_at FROM comfy_output_cleanup_tombstones WHERE id=$1`, item.ID).Scan(&attempts, &status, &eligible, &last, &next); err != nil || attempts != index+1 || status != outcome || !eligible.Equal(dueAt) || next.Before(last) {
			t.Fatalf("retry state %d %s %v %v %v %v", attempts, status, eligible, last, next, err)
		}
	}
	wrong := item
	wrong.SizeBytes++
	if _, err := r.RecordComfyOutputCleanup(ctx, []domain.ComfyOutputCleanupResult{{ComfyOutputCleanupTombstone: wrong, Outcome: "deleted"}}, time.Hour); !errors.Is(err, store.ErrComfyCleanupResult) {
		t.Fatal("accepted changed identity", err)
	}
	result := domain.ComfyOutputCleanupResult{ComfyOutputCleanupTombstone: item, Outcome: "deleted"}
	if _, err := r.RecordComfyOutputCleanup(ctx, []domain.ComfyOutputCleanupResult{result, result}, time.Hour); !errors.Is(err, store.ErrComfyCleanupResult) {
		t.Fatal("accepted duplicate target", err)
	}
	if _, err := db.Exec(`CREATE FUNCTION reject_cleanup_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'receipt test failure'; END $$;
	 CREATE TRIGGER reject_cleanup_receipt BEFORE INSERT ON comfy_output_cleanup_receipts FOR EACH ROW EXECUTE FUNCTION reject_cleanup_receipt()`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordComfyOutputCleanup(ctx, []domain.ComfyOutputCleanupResult{result}, time.Hour); err == nil {
		t.Fatal("receipt storage failure ignored")
	}
	if _, err := db.Exec(`DROP TRIGGER reject_cleanup_receipt ON comfy_output_cleanup_receipts; DROP FUNCTION reject_cleanup_receipt()`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM comfy_output_cleanup_tombstones WHERE id=$1`, item.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("lost intent after failed receipt", count, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.RecordComfyOutputCleanup(ctx, []domain.ComfyOutputCleanupResult{result}, time.Hour)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent receipt", err)
		}
	}
	receipts, err := r.ComfyOutputCleanupReceipts(ctx, 100)
	if err != nil || len(receipts) != 1 || receipts[0].Outcome != "deleted" || receipts[0].FreedBytes != 42 || receipts[0].Attempts != 5 || !receipts[0].EligibleAt.Equal(dueAt) {
		t.Fatal("receipt", receipts, err)
	}
	for _, outcome := range []string{"missing", "legacy_confirmed"} {
		item.ID = 0
		item.Filename = outcome + ".png"
		if err := r.ScheduleComfyOutputCleanup(ctx, item, dueAt); err != nil {
			t.Fatal(err)
		}
		due, err := r.DueComfyOutputCleanup(ctx, 100)
		if err != nil || len(due) != 1 {
			t.Fatal(due, err)
		}
		if _, err := r.RecordComfyOutputCleanup(ctx, []domain.ComfyOutputCleanupResult{{ComfyOutputCleanupTombstone: due[0], Outcome: outcome}}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	receipts, err = r.ComfyOutputCleanupReceipts(ctx, 100)
	var freed int64
	for _, receipt := range receipts {
		freed += receipt.FreedBytes
	}
	if err != nil || len(receipts) != 3 || freed != 42 {
		t.Fatal("inflated deleted bytes", receipts, err)
	}
	if _, err := db.Exec(`UPDATE comfy_output_cleanup_receipts SET confirmed_at=now()-interval '91 days' WHERE outcome='missing'`); err != nil {
		t.Fatal(err)
	}
	report, err := r.CleanupDatabaseRetention(ctx, domain.DatabaseRetentionCutoffs{}, 100, 1)
	if err != nil || report.DeletedRows["comfy_output_cleanup_receipts"] != 1 {
		t.Fatal("receipt retention", report, err)
	}
}
