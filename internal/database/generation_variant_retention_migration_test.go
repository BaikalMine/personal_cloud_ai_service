package database

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

func TestVariantRetentionMigration(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 81); err != nil {
		t.Fatal(err)
	}
	var userID, jobID int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('retained-view','test','user') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO generation_jobs(user_id,public_id,correlation_id,request_id,state,template_id,payload_cipher) VALUES($1,'retained-view-job','retained-view-job','retained-view-job','completed','text-to-image','job-cipher') RETURNING id`, userID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO quick_generation_variants(user_id,job_id,prompt_id,template_id,workflow_id,model_name,seed,payload_cipher,state,finished_at) VALUES($1,$2,'retained-view-prompt','text-to-image','test','test',0,'view-cipher','completed',now())`, userID, jobID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	var link sql.NullInt64
	var cipher, state string
	var seed int64
	if err := db.QueryRow(`SELECT job_id,convert_from(payload_cipher,'UTF8'),state,seed FROM quick_generation_variants WHERE prompt_id='retained-view-prompt'`).Scan(&link, &cipher, &state, &seed); err != nil || link.Valid || cipher != "view-cipher" || state != "completed" || seed != 0 {
		t.Fatal("retained projection changed", link, cipher, state, seed, err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM quick_generation_variants`).Scan(&count); err != nil || count != 0 {
		t.Fatal("owner deletion must still remove private projections", count, err)
	}
}
