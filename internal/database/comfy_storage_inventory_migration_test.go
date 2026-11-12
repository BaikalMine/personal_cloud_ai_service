package database

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestComfyStorageInventoryMigration(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 87); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var valid bool
	var definition string
	if err := db.QueryRow(`SELECT i.indisvalid,pg_get_indexdef(i.indexrelid) FROM pg_index i WHERE i.indexrelid='content_media_output_path_idx'::regclass`).Scan(&valid, &definition); err != nil || !valid || !strings.Contains(definition, "original_name, subfolder, id DESC") || !strings.Contains(definition, "storage_type = 'output'") {
		t.Fatal(valid, definition, err)
	}
}
