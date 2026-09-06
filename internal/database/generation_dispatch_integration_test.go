package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestGenerationDispatchMigrationKeepsLegacySendsUncertain(t *testing.T) {
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
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resetMigrationIntegrationSchema(t, cleanup, db)
	}()
	if err = applyMigrationsThroughForTest(ctx, db, 59); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"draft", "preparing", "uploading", "waiting_for_resources", "queued", "completed", "failed"} {
		if _, err = db.ExecContext(ctx, `INSERT INTO generation_jobs(public_id,request_id,state) VALUES($1,$1,$2)`, "dispatch-migration-"+state, state); err != nil {
			t.Fatal(err)
		}
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT state,submission_started_at IS NOT NULL,dispatch_queued_at IS NOT NULL,dispatch_token,submission_rejected_at IS NOT NULL FROM generation_jobs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var state, token string
		var uncertain, queued, rejected bool
		if err = rows.Scan(&state, &uncertain, &queued, &token, &rejected); err != nil {
			t.Fatal(err)
		}
		want := state == "preparing" || state == "uploading" || state == "waiting_for_resources"
		if uncertain != want || queued || token != "" || rejected {
			t.Fatalf("state=%s uncertain=%v queued=%v token=%q rejected=%v", state, uncertain, queued, token, rejected)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
