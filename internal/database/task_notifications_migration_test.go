package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestTaskNotificationMigrationPreservesExistingEvents(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 71); err != nil {
		t.Fatal(err)
	}
	var owner, jobID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('notice-upgrade','disabled','user') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO generation_jobs(public_id,user_id,request_id,state) VALUES('notice-upgrade-generation',$1,'notice-upgrade-generation','completed') RETURNING id`, owner).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_notifications(user_id,generation_job_id,kind,title,message,href,read_at) VALUES($1,$2,'generation_completed','Original','Original message','/generate?job=notice-upgrade-generation','2026-09-01T12:00:00Z')`, owner, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO lora_dataset_assets(id,user_id,name,content_hash,mime_type,size_bytes,width,height) VALUES('upgrade-asset',$1,'photo',repeat('a',64),'image/png',1,256,256)`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO lora_caption_jobs(id,user_id,image_id,asset_id,request_key,instruction_version,input_cipher,state) VALUES('upgrade-caption',$1,'image','upgrade-asset',repeat('b',64),'v1','original-cipher','running')`, owner); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM user_notifications WHERE title='Original' AND message='Original message' AND generation_job_id=$1 AND read_at='2026-09-01T12:00:00Z'`, jobID).Scan(&count); err != nil || count != 1 {
		t.Fatal("migration rewrote the original notification")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM lora_caption_series`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented historical series")
	}
	var state, cipher string
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT state,convert_from(input_cipher,'UTF8'),run_version FROM lora_caption_jobs WHERE id='upgrade-caption'`).Scan(&state, &cipher, &version); err != nil || state != "running" || cipher != "original-cipher" || version != 1 {
		t.Fatal("migration rewrote caption work")
	}
}
