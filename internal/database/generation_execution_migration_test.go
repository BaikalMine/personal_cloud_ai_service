package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestGenerationExecutionMigrationPreservesLegacyJobs(t *testing.T) {
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
	defer resetMigrationIntegrationSchema(t, ctx, db)
	if err = applyMigrationsThroughForTest(ctx, db, 75); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO generation_jobs(public_id,correlation_id,request_id,state,seed) VALUES('legacy-execution-001','legacy-execution-001','legacy-execution-001','completed',4611686018427387918)`); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		t.Helper()
		var value string
		if err := db.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(j)-'input_references_verified' ORDER BY id)::text FROM generation_jobs j`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := read()
	for range 2 {
		if err = Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if read() != before {
		t.Fatal("migration rewrote old jobs")
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM generation_job_executions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("fabricated historical evidence", err)
	}
}
