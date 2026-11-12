package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestComfyInputCleanupReceiptsMigration(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 84); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)
	if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,filename,size_bytes,content_hash,cleanup_attempts,expires_at,cleanup_retry_at)
	 VALUES($1,'before.png',42,$2,3,$3,$3)`, strings.Repeat("a", 32), strings.Repeat("b", 64), due); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var attempts, receipts int
	var status string
	var expires, next time.Time
	if err := db.QueryRow(`SELECT cleanup_attempts,cleanup_last_outcome,expires_at,cleanup_retry_at FROM comfy_input_assets`).Scan(&attempts, &status, &expires, &next); err != nil || attempts != 3 || status != "legacy_retry" || !expires.Equal(due) || !next.Equal(due) {
		t.Fatal(attempts, status, expires, next, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM comfy_input_cleanup_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal(receipts, err)
	}
	for _, status := range []string{"missing", "legacy_confirmed"} {
		if _, err := db.Exec(`INSERT INTO comfy_input_cleanup_receipts(input_id,filename,subfolder,storage_type,size_bytes,content_hash,state,observed_size_bytes,observed_hash,outcome,freed_bytes,attempt_count,queued_at,eligible_at)
		 VALUES($1,'a.png','','input',42,$2,'stored',42,$2,$3,42,1,now(),now())`, strings.Repeat("c", 32), strings.Repeat("d", 64), status); err == nil {
			t.Fatal("credited unproven bytes", status)
		}
	}
}
