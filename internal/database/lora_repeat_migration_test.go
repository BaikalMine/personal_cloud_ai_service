package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLoraRepeatMigrationPreservesExistingTraining(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 74); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO lora_training_jobs
 (public_id,request_id,profile_id,family,base_model,name,output_name,trigger_word,concept_type,preset,resolution,max_train_steps,network_dim,network_alpha,learning_rate,seed,sample_count,dataset_bytes,dataset_path,state)
 VALUES('upgrade-training-001','upgrade-training-001','profile','krea2','base.safetensors','Original','original','fixture','object','balanced',768,1234,16,8,0.00012,1099511627795,5,100,'old-path.zip','completed')`); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		if err := db.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM lora_training_jobs t`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot() != before {
		t.Fatal("repeat migration changed historical training")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM lora_training_repeats`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration fabricated repeat receipts", count, err)
	}
}
