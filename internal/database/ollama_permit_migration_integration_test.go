package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOwnedOllamaPermitMigrationPreservesComfy(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	resetMigrationIntegrationSchema(t, ctx, db)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resetMigrationIntegrationSchema(t, ctx, db)
	}()
	if err = applyMigrationsThroughForTest(ctx, db, 69); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO gpu_resources(id) VALUES('primary')`); err != nil {
		t.Fatal(err)
	}
	id, instance, hash := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 64)
	if _, err = db.ExecContext(ctx, `INSERT INTO gpu_executor_permits(id,resource_id,executor,instance_id,token_hash,state) VALUES($1,'primary','comfyui',$2,$3,'running')`, id, instance, hash); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var unchanged bool
	if err = db.QueryRowContext(ctx, `SELECT executor='comfyui' AND instance_id=$2 AND token_hash=$3 AND state='running' AND completion_code='' AND owner_id IS NULL AND owner_generation=0 AND owner_token_hash='' FROM gpu_executor_permits WHERE id=$1`, id, instance, hash).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("legacy permit changed: %v %v", unchanged, err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE gpu_executor_permits SET state='closed',completion_code='quiesced' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO gpu_executor_permits(id,resource_id,executor,instance_id,token_hash) VALUES($1,'primary','ollama',$2,$3)`, strings.Repeat("d", 32), instance, hash); err == nil {
		t.Fatal("database accepted ownerless Ollama permit")
	}
}
