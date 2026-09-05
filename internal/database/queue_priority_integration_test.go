package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestQueuePriorityMigrationPreservesLegacyPolicy(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 57); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,pause_mining_for_quick_generation)
		VALUES('legacy-normal','hash','user',false),('legacy-priority','hash','admin',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO invites(token_hash,created_by_user_id,max_uses,expires_at,pause_mining_for_quick_generation)
		SELECT 'legacy-'||username,id,2,now()+interval '1 day',pause_mining_for_quick_generation FROM users`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"users", "invites"} {
		var count, enabled, mismatches int
		if err := db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE queue_priority),count(*) FILTER(WHERE queue_priority<>pause_mining_for_quick_generation) FROM `+table).Scan(&count, &enabled, &mismatches); err != nil {
			t.Fatal(err)
		}
		if count != 2 || enabled != 1 || mismatches != 0 {
			t.Fatalf("%s migration count=%d enabled=%d mismatches=%d", table, count, enabled, mismatches)
		}
		if _, err := db.ExecContext(ctx, `UPDATE `+table+` SET queue_priority=NOT pause_mining_for_quick_generation`); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"users", "invites"} {
		var independent int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE queue_priority<>pause_mining_for_quick_generation`).Scan(&independent); err != nil {
			t.Fatal(err)
		}
		if independent != 2 {
			t.Fatalf("repeated migration reset independent policy in %s", table)
		}
	}
}
