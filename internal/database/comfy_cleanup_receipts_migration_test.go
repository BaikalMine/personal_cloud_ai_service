package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestComfyCleanupReceiptsMigration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	resetMigrationIntegrationSchema(t, ctx, db)
	defer resetMigrationIntegrationSchema(t, ctx, db)
	if err := applyMigrationsThroughForTest(ctx, db, 83); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)
	if _, err := db.Exec(`INSERT INTO comfy_output_cleanup_tombstones(filename,storage_type,size_bytes,content_hash,next_attempt_at,attempt_count)
	 VALUES('before.png','output',42,$1,$2,3)`, strings.Repeat("a", 64), due); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var attempts, receipts int
	var status string
	var next, eligible time.Time
	if err := db.QueryRow(`SELECT attempt_count,last_outcome,next_attempt_at,eligible_at FROM comfy_output_cleanup_tombstones WHERE filename='before.png'`).Scan(&attempts, &status, &next, &eligible); err != nil || attempts != 3 || status != "legacy_retry" || !next.Equal(due) || !eligible.Equal(due) {
		t.Fatalf("migration changed pending cleanup: %d %s %v %v %v", attempts, status, next, eligible, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM comfy_output_cleanup_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("invented historical receipts", receipts, err)
	}
	for _, outcome := range []string{"missing", "legacy_confirmed"} {
		if _, err := db.Exec(`INSERT INTO comfy_output_cleanup_receipts(tombstone_id,filename,subfolder,storage_type,size_bytes,content_hash,outcome,cleanup_reason,freed_bytes,attempt_count,queued_at,eligible_at)
		 VALUES(1,'x','','output',42,$1,$2,'retention',42,1,now(),now())`, strings.Repeat("a", 64), outcome); err == nil {
			t.Fatal("credited bytes without a deletion", outcome)
		}
	}
}
