package database

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestComfyStorageRetentionMigration(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 90); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gpu_resources(id) VALUES('primary')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO comfy_storage_operations(id,resource_id,actor_name,host_key,request_hash,request,selection,item_count,expected_bytes)
	 VALUES($1,'primary','former-admin',$2,$2,'{}','[{}]',1,42)`, strings.Repeat("m", 32), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var origin, name string
	if err := db.QueryRow(`SELECT origin,actor_name FROM comfy_storage_operations`).Scan(&origin, &name); err != nil || origin != "manual" || name != "former-admin" {
		t.Fatal("old null-actor operation changed ownership", origin, name, err)
	}
	for _, query := range []string{
		`UPDATE comfy_storage_operations SET origin='automatic'`,
		`UPDATE comfy_storage_operations SET origin='retention'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatal("accepted invalid origin", query)
		}
	}
	if _, err := db.Exec(`UPDATE comfy_storage_operations SET origin='retention',actor_name=''`); err != nil {
		t.Fatal(err)
	}
	var actor int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('retention-admin','hash','admin') RETURNING id`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE comfy_storage_operations SET actor_id=$1`, actor); err == nil {
		t.Fatal("retention impersonates administrator")
	}
}
