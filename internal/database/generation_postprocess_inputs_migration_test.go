package database

import (
	"context"
	"os"
	"testing"
)

func TestPostprocessInputsMigrationPreservesOldTracking(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 80); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,filename,size_bytes,content_hash,state) VALUES(repeat('a',32),'old.png',100,repeat('a',64),'stored')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var size int64
	var state string
	if err := db.QueryRow(`SELECT size_bytes,state FROM comfy_input_assets WHERE id=repeat('a',32)`).Scan(&size, &state); err != nil || size != 100 || state != "stored" {
		t.Fatal("old input changed", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM generation_postprocess_inputs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invented postprocess ownership", err)
	}
	if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,filename,size_bytes,content_hash) VALUES(repeat('b',32),'large.mp4',536870912,repeat('b',64))`); err != nil {
		t.Fatal("512 MiB source cannot be accounted", err)
	}
	if _, err := db.Exec(`UPDATE comfy_input_assets SET size_bytes=536870913 WHERE id=repeat('b',32)`); err == nil {
		t.Fatal("unbounded input size")
	}
}
