package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMiningResumeMigrationDoesNotAssumeOldLeaseCompleted(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 58); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('resume-owner','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO miners(name,script_path,process_name,enabled,is_default) VALUES('test','test.bat','test.exe',true,true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO quick_generation_mining_leases(id,user_id,miner_id,script_path,process_name,resume_mining,created_at) SELECT 'legacy',u.id,m.id,m.script_path,m.process_name,true,now()-interval '1 day' FROM users u CROSS JOIN miners m`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var ready bool
	if err := db.QueryRowContext(ctx, `SELECT resume_ready FROM quick_generation_mining_leases WHERE id='legacy'`).Scan(&ready); err != nil || ready {
		t.Fatalf("legacy ready=%v err=%v", ready, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE quick_generation_mining_leases SET resume_ready=true WHERE id='legacy'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT resume_ready FROM quick_generation_mining_leases WHERE id='legacy'`).Scan(&ready); err != nil || !ready {
		t.Fatalf("persisted ready=%v err=%v", ready, err)
	}
}
