package database

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestComfyStorageOperationsMigration(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 89); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gpu_resources(id,observation,message) VALUES('primary','busy','existing work')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM comfy_storage_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	var observation string
	if err := db.QueryRow(`SELECT observation FROM gpu_resources WHERE id='primary'`).Scan(&observation); err != nil || observation != "busy" {
		t.Fatal(observation, err)
	}
	insert := `INSERT INTO comfy_storage_operations(id,resource_id,actor_name,host_key,request_hash,request,selection,state,item_count,expected_bytes,dispatched_at) VALUES($1,'primary','admin',$2,$2,'{}','[{}]','dispatching',1,42,$3)`
	if _, err := db.Exec(insert, strings.Repeat("a", 32), strings.Repeat("b", 64), nil); err == nil {
		t.Fatal("accepted dispatch without timestamp")
	}
	if _, err := db.Exec(insert, strings.Repeat("a", 32), strings.Repeat("b", 64), "2026-09-09T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, strings.Repeat("c", 32), strings.Repeat("b", 64), "2026-09-09T00:00:00Z"); err == nil {
		t.Fatal("accepted two active claims")
	}
	for _, query := range []string{
		`UPDATE comfy_storage_operations SET freed_bytes=1`,
		`UPDATE comfy_storage_operations SET request=NULL,selection=NULL,pruned_at=now()`,
		`UPDATE comfy_storage_operations SET selection='[]'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatal("accepted invalid operation", query)
		}
	}
}
