package database

import (
	"context"
	"os"
	"testing"
)

func TestGenerationSubmissionMigration(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 91); err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('submission-migration-owner','hash','user') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO generation_jobs(user_id,public_id,request_id) VALUES($1,'migration-submission-job','migration-submission-request')`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO generation_batches(user_id,public_id,request_id,template_id,workflow_id,model_name,experiment_mode,total_count)
	 VALUES($1,'migration-submission-batch','migration-submission-request','text-to-image','photoflow-krea2','model','seeds',2)`, user); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	assertReceipts := func(want int) {
		t.Helper()
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM generation_submissions WHERE user_id=$1 AND public_id<>'' AND NOT closed`, user).Scan(&count); err != nil || count != want {
			t.Fatal(count, want, err)
		}
	}
	assertReceipts(2)
	if _, err := db.Exec(`UPDATE generation_submissions SET closed=true WHERE user_id=$1`, user); err == nil {
		t.Fatal("bound receipt can claim an absent closure")
	}
	if _, err := db.Exec(`DELETE FROM generation_batches WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM generation_jobs WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	assertReceipts(2)
	if _, err := db.Exec(`DELETE FROM users WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}
	assertReceipts(0)
}
