package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLoraVerificationMigrationPreservesExistingBatches(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 73); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"seeds", "parameter"} {
		parameter, values := "", "[]"
		if mode == "parameter" {
			parameter, values = "steps", `["20","30"]`
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO generation_batches
 (public_id,request_id,template_id,workflow_id,model_name,experiment_mode,parameter_name,parameter_values,total_count,seed_locked)
 VALUES($1,$1,'text-to-image','workflow-fixture','base-fixture',$2,$3,$4,2,true)`, "upgrade-batch-"+mode, mode, parameter, values); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO generation_jobs(public_id,request_id,state,batch_id,batch_position,experiment_value,payload_cipher,seed)
 SELECT 'upgrade-child-fixture','upgrade-child-fixture','draft',id,1,'42','original-cipher',42 FROM generation_batches WHERE experiment_mode='seeds'`); err != nil {
		t.Fatal(err)
	}
	snapshot := func(table string) string {
		t.Helper()
		var value string
		// Table names are test-owned constants, never input.
		if err := db.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(t)-'input_references_verified' ORDER BY id)::text FROM `+table+` t`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	beforeBatches, beforeJobs := snapshot("generation_batches"), snapshot("generation_jobs")
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot("generation_batches") != beforeBatches || snapshot("generation_jobs") != beforeJobs {
		t.Fatal("migration changed existing batch or child data")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM lora_verifications)+(SELECT count(*) FROM lora_verification_members)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented historical verification evidence", count, err)
	}
}
