package database

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestComfyInputCleanupSizeMigration(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 86); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO comfy_input_cleanup_receipts(input_id,filename,subfolder,storage_type,size_bytes,content_hash,state,observed_size_bytes,observed_hash,outcome,freed_bytes,attempt_count,queued_at,eligible_at)
	 VALUES($1,'video.mp4','','input',$2,repeat('a',64),'stored',$2,repeat('a',64),'deleted',$2,1,now(),now())`
	if _, err := db.Exec(insert, strings.Repeat("a", 32), int64(320<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, strings.Repeat("b", 32), int64(512<<20)); err == nil {
		t.Fatal("old limit unexpectedly accepted 512 MiB")
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(insert, strings.Repeat("b", 32), int64(512<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, strings.Repeat("c", 32), int64(512<<20)+1); err == nil {
		t.Fatal("accepted size above input limit")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM comfy_input_cleanup_receipts`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}
