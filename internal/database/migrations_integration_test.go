package database

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

func TestDurableGenerationJobsMigrationBackfill(t *testing.T) {
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
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		resetMigrationIntegrationSchema(t, cleanupCtx, db)
	}()
	if err := applyMigrationsThroughForTest(ctx, db, 39); err != nil {
		t.Fatal(err)
	}

	var userID, minerID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role)
		VALUES ('migration-job-user','hash','user') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO miners(name,script_path,process_name,enabled,is_default)
		VALUES ('Migration miner','migration/miner/start.bat','migration-miner.exe',true,true) RETURNING id`).Scan(&minerID); err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO generation_requests(user_id,request_id,prompt_id,created_at,updated_at)
			VALUES ($1,'migration-matched-request','migration-prompt-completed',now()-interval '2 hours',now()-interval '1 hour'),
			       ($1,'migration-abandoned-request',NULL,now()-interval '2 hours',now()-interval '1 hour'),
			       ($1,'migration-recent-request',NULL,now(),now())`, []any{userID}},
		{`INSERT INTO quick_generation_variants(user_id,prompt_id,template_id,workflow_id,model_name,seed,payload_cipher,state,created_at,finished_at,state_changed_at,error_message)
			VALUES ($1,'migration-prompt-completed','image','krea2','Krea2',11,decode('01','hex'),'completed',now()-interval '2 hours',now()-interval '1 hour',now()-interval '1 hour',''),
			       ($1,'migration-prompt-running','video','minimax-h3-v4','MiniMax H3',12,decode('02','hex'),'running',now()-interval '10 minutes',NULL,now()-interval '5 minutes',''),
			       ($1,'migration-prompt-failed','video','minimax-h3-v4','MiniMax H3',13,decode('03','hex'),'error',now()-interval '2 hours',now()-interval '1 hour',now()-interval '1 hour','legacy failure')`, []any{userID}},
		{`INSERT INTO content_events(user_id,service,kind,external_id,model,generation_state,prompt_cipher,response_cipher,metadata_cipher,expires_at)
			VALUES ($1,'comfyui','comfyui_prompt','migration-prompt-completed','Krea2','completed',decode('01','hex'),decode('02','hex'),decode('03','hex'),now()+interval '1 day')`, []any{userID}},
		{`INSERT INTO quick_generation_mining_leases(id,prompt_id,user_id,miner_id,script_path,process_name,resume_mining)
			VALUES ('migration-lease','migration-prompt-running',$1,$2,'migration/miner/start.bat','migration-miner.exe',true)`, []any{userID, minerID}},
	}
	for _, fixture := range fixtures {
		if _, err := db.ExecContext(ctx, fixture.query, fixture.args...); err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var jobCount, transitionCount, migrationCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM generation_jobs`).Scan(&jobCount); err != nil || jobCount != 5 {
		t.Fatalf("backfilled generation jobs=%d want=5 err=%v", jobCount, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM generation_job_transitions`).Scan(&transitionCount); err != nil || transitionCount != jobCount {
		t.Fatalf("backfilled generation job transitions=%d want=%d err=%v", transitionCount, jobCount, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version IN (40,41,42)`).Scan(&migrationCount); err != nil || migrationCount != 3 {
		t.Fatalf("generation job migration records=%d want=3 err=%v", migrationCount, err)
	}

	var state, requestID string
	var resourcesReleased, requestLinked, variantLinked, contentLinked bool
	if err := db.QueryRowContext(ctx, `SELECT j.state,j.request_id,j.resources_released_at IS NOT NULL,
		gr.job_id=j.id,v.job_id=j.id,e.generation_job_id=j.id
		FROM generation_jobs j
		JOIN generation_requests gr ON gr.user_id=j.user_id AND gr.request_id=j.request_id
		JOIN quick_generation_variants v ON v.prompt_id=j.prompt_id
		JOIN content_events e ON e.external_id=j.prompt_id
		WHERE j.prompt_id='migration-prompt-completed'`).Scan(
		&state, &requestID, &resourcesReleased, &requestLinked, &variantLinked, &contentLinked,
	); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || requestID != "migration-matched-request" || !resourcesReleased || !requestLinked || !variantLinked || !contentLinked {
		t.Fatalf("completed job backfill state=%s request=%s released=%v request_link=%v variant_link=%v content_link=%v",
			state, requestID, resourcesReleased, requestLinked, variantLinked, contentLinked)
	}
	var leaseLinked bool
	if err := db.QueryRowContext(ctx, `SELECT j.state='running' AND l.generation_job_id=j.id
		FROM generation_jobs j JOIN quick_generation_mining_leases l ON l.prompt_id=j.prompt_id
		WHERE j.prompt_id='migration-prompt-running'`).Scan(&leaseLinked); err != nil || !leaseLinked {
		t.Fatalf("running job lease linked=%v err=%v", leaseLinked, err)
	}
	var failedCode string
	if err := db.QueryRowContext(ctx, `SELECT state,error_code,resources_released_at IS NOT NULL
		FROM generation_jobs WHERE prompt_id='migration-prompt-failed'`).Scan(&state, &failedCode, &resourcesReleased); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || failedCode != "legacy_generation_error" || !resourcesReleased {
		t.Fatalf("failed job backfill state=%s code=%s released=%v", state, failedCode, resourcesReleased)
	}
	if err := db.QueryRowContext(ctx, `SELECT state,resources_released_at IS NOT NULL
		FROM generation_jobs WHERE request_id='migration-abandoned-request'`).Scan(&state, &resourcesReleased); err != nil {
		t.Fatal(err)
	}
	if state != "expired" || !resourcesReleased {
		t.Fatalf("abandoned request backfill state=%s released=%v", state, resourcesReleased)
	}
	if err := db.QueryRowContext(ctx, `SELECT state,resources_released_at IS NOT NULL
		FROM generation_jobs WHERE request_id='migration-recent-request'`).Scan(&state, &resourcesReleased); err != nil {
		t.Fatal(err)
	}
	if state != "preparing" || resourcesReleased {
		t.Fatalf("recent request backfill state=%s released=%v", state, resourcesReleased)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	var repeatedJobCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM generation_jobs`).Scan(&repeatedJobCount); err != nil || repeatedJobCount != jobCount {
		t.Fatalf("jobs after repeated migration=%d want=%d err=%v", repeatedJobCount, jobCount, err)
	}
}

func applyMigrationsThroughForTest(ctx context.Context, db *sql.DB, maximumVersion int64) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version BIGINT PRIMARY KEY,name TEXT NOT NULL,checksum TEXT NOT NULL,applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}
	for _, item := range migrationCatalog {
		if item.version > maximumVersion {
			break
		}
		for _, statement := range item.statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum) VALUES($1,$2,$3)`,
			item.version, item.name, migrationChecksum(item)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func resetMigrationIntegrationSchema(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS public CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
}
