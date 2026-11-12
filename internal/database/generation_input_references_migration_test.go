package database

import (
	"context"
	"os"
	"testing"
)

func TestGenerationInputReferencesMigration(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 88); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`INSERT INTO generation_jobs(public_id,request_id,state,payload_cipher)
	 VALUES('legacy-input-reference-job','legacy-input-reference-job','queued','legacy-cipher') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var verified bool
	var payload, state string
	if err := db.QueryRow(`SELECT input_references_verified,payload_cipher,state FROM generation_jobs WHERE id=$1`, id).Scan(&verified, &payload, &state); err != nil || verified || payload != "legacy-cipher" || state != "queued" {
		t.Fatal(verified, payload, state, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM generation_input_references`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invented legacy references", count, err)
	}
	if _, err := db.Exec(`UPDATE generation_jobs SET input_references_verified=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	// An older caller that replaces an encrypted payload cannot retain a stale
	// verified manifest, even if it does not know about the new column.
	if _, err := db.Exec(`UPDATE generation_jobs SET payload_cipher='replacement' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT input_references_verified FROM generation_jobs WHERE id=$1`, id).Scan(&verified); err != nil || verified {
		t.Fatal(verified, err)
	}
	if _, err := db.Exec(`INSERT INTO generation_input_references(job_id,input_id,input_name) VALUES($1,'tracked-id','owner/input.png')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM generation_input_references`).Scan(&count); err != nil || count != 0 {
		t.Fatal("reference did not follow job retention", count, err)
	}
}
