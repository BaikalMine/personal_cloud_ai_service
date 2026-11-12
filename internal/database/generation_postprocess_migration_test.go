package database

import (
	"context"
	"os"
	"testing"
)

func TestPostprocessMigrationPreservesExistingJobs(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 79); err != nil {
		t.Fatal(err)
	}
	var jobID int64
	if err := db.QueryRow(`INSERT INTO generation_jobs(public_id,correlation_id,request_id,state,template_id,payload_cipher) VALUES('old-generation-job','old-generation-job','old-generation-job','completed','minimax-h3-video','unchanged-cipher') RETURNING id`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var template, cipher string
	if err := db.QueryRow(`SELECT template_id,convert_from(payload_cipher,'UTF8') FROM generation_jobs WHERE id=$1`, jobID).Scan(&template, &cipher); err != nil || template != "minimax-h3-video" || cipher != "unchanged-cipher" {
		t.Fatal(template, cipher, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM generation_postprocess_receipts`).Scan(&n); err != nil || n != 0 {
		t.Fatal("invented lineage", n, err)
	}
}
