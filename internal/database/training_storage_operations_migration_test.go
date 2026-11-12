package database

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestTrainingStorageOperationsMigration(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 85); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gpu_resources(id,observation,message) VALUES('primary','busy','preserve existing work')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var observation string
	if err := db.QueryRow(`SELECT observation FROM gpu_resources WHERE id='primary'`).Scan(&observation); err != nil || observation != "busy" {
		t.Fatal(observation, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM training_storage_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	insert := `INSERT INTO training_storage_operations(id,resource_id,actor_name,host_key,request_hash,request,preview,state,item_count,expected_bytes,dispatched_at)
	 VALUES($1,'primary','admin',$2,$2,'{}','{}','dispatching',1,42,$3)`
	if _, err := db.Exec(insert, strings.Repeat("a", 32), strings.Repeat("b", 64), nil); err == nil {
		t.Fatal("dispatch without timestamp accepted")
	}
	if _, err := db.Exec(insert, strings.Repeat("a", 32), strings.Repeat("b", 64), "2026-09-09T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, strings.Repeat("c", 32), strings.Repeat("b", 64), "2026-09-09T00:00:00Z"); err == nil {
		t.Fatal("two active operations on one resource accepted")
	}
	if _, err := db.Exec(`UPDATE training_storage_operations SET freed_bytes=42 WHERE id=$1`, strings.Repeat("a", 32)); err == nil {
		t.Fatal("unconfirmed operation credited bytes")
	}
	if _, err := db.Exec(`UPDATE training_storage_operations SET request=NULL,preview=NULL,pruned_at=now() WHERE id=$1`, strings.Repeat("a", 32)); err == nil {
		t.Fatal("active operation pruned")
	}
}
