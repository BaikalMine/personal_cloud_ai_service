package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestTaskMigrationBackfillsRevisionWithoutChangingWork(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 70); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('task-migration-owner','disabled','user') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO generation_jobs(public_id,user_id,request_id,state,status_message,payload_cipher)
		VALUES('task-migration-job-001',$1,'task-migration-job-001','running','original status','private-payload')`, userID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
		var revision int64
		if err := db.QueryRowContext(ctx, `SELECT revision FROM user_task_revisions WHERE user_id=$1`, userID).Scan(&revision); err != nil || revision != 1 {
			t.Fatalf("revision=%d err=%v", revision, err)
		}
	}
	var state, message, payload string
	if err := db.QueryRowContext(ctx, `SELECT state,status_message,convert_from(payload_cipher,'UTF8') FROM generation_jobs WHERE user_id=$1`, userID).Scan(&state, &message, &payload); err != nil {
		t.Fatal(err)
	}
	if state != "running" || message != "original status" || payload != "private-payload" {
		t.Fatal("migration changed source work")
	}
	if _, err := db.ExecContext(ctx, `UPDATE generation_jobs SET status_message='new status' WHERE user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.QueryRowContext(ctx, `SELECT revision FROM user_task_revisions WHERE user_id=$1`, userID).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
}
