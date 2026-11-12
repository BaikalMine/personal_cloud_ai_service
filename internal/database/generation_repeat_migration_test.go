package database

import (
	"context"
	"os"
	"testing"
)

func TestGenerationRepeatMigrationPreservesExecution(t *testing.T) {
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
	if err = applyMigrationsThroughForTest(ctx, db, 76); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = db.QueryRow(`INSERT INTO generation_jobs(public_id,correlation_id,request_id,state) VALUES('repeat-migration-001','repeat-migration-001','repeat-migration-001','completed') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO generation_job_executions(job_id,payload_cipher,payload_sha256) VALUES($1,'old-cipher',repeat('a',64))`, id); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var cipher string
	if err = db.QueryRow(`SELECT convert_from(payload_cipher,'UTF8') FROM generation_job_executions WHERE job_id=$1`, id).Scan(&cipher); err != nil || cipher != "old-cipher" {
		t.Fatal(cipher, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM generation_repeats`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invented repeat receipts", count, err)
	}
}
