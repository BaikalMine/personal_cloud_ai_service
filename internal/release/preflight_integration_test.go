package release

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"github.com/lib/pq"
)

func releaseTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var suffix [12]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "release_preflight_" + hex.EncodeToString(suffix[:])
	if _, err = admin.ExecContext(ctx, `CREATE DATABASE `+pq.QuoteIdentifier(name)); err != nil {
		t.Fatal(err)
	}
	// Only the successfully created random test database is ever dropped.
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanupCtx, `DROP DATABASE `+pq.QuoteIdentifier(name)+` WITH (FORCE)`); err != nil {
			t.Error(err)
		}
	})
	dsn = releaseTestDSN(t, dsn, name)
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

func releaseTestDSN(t *testing.T, dsn, name string) string {
	t.Helper()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		parsed.Path = "/" + name
		query := parsed.Query()
		query.Del("dbname")
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " dbname=" + name
	}
	return dsn
}

func preflightExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func observationCount(t *testing.T, report DatabaseObservation, kind string) int64 {
	t.Helper()
	for _, item := range report.Workload {
		if item.Kind == kind {
			if !item.Applicable {
				t.Fatalf("%s is not applicable", kind)
			}
			return item.Count
		}
	}
	t.Fatalf("missing workload %s: %+v", kind, report)
	return -1
}

func TestDatabasePreflightCatalogAndReadFailures(t *testing.T) {
	db := releaseTestDatabase(t)
	_, manifest := fixtureManifest(t)
	ctx := context.Background()
	report, err := InspectDatabase(ctx, db, manifest, manifest)
	if err != nil || !report.DatabaseChecksPassed || !report.MatchesPrevious || !report.ReaderSupported ||
		!report.WorkloadChecked || !report.WorkloadIdle || report.DeploymentAuthorized || len(report.Workload) != 15 ||
		report.SnapshotAt.IsZero() || report.FinishedAt.Before(report.SnapshotAt) || !report.Schema.BinaryRollback {
		t.Fatalf("idle current database: %+v %v", report, err)
	}
	for _, item := range report.Workload {
		if !item.Applicable || item.Count != 0 {
			t.Fatalf("unexpected baseline: %+v", item)
		}
	}
	t.Run("declared previous is not the live catalog", func(t *testing.T) {
		old := manifest
		old.Migrations = slices.Clone(manifest.Migrations[:len(manifest.Migrations)-1])
		report, err := InspectDatabase(ctx, db, old, manifest)
		if err != nil || report.DatabaseChecksPassed || report.MatchesPrevious || !report.WorkloadChecked ||
			!slices.Contains(report.Blockers, "database_differs_from_previous_release") {
			t.Fatalf("wrong previous accepted: %+v %v", report, err)
		}
	})
	t.Run("older candidate", func(t *testing.T) {
		candidate := manifest
		candidate.Migrations = slices.Clone(manifest.Migrations[:len(manifest.Migrations)-1])
		report, err := InspectDatabase(ctx, db, manifest, candidate)
		if err != nil || report.DatabaseChecksPassed || report.Schema.CanUpgrade || !report.Schema.RestoreRequired ||
			!slices.Contains(report.Blockers, "candidate_schema_incompatible") {
			t.Fatalf("old candidate accepted: %+v %v", report, err)
		}
	})
	t.Run("checksum mismatch is not repaired", func(t *testing.T) {
		last := manifest.Migrations[len(manifest.Migrations)-1]
		preflightExec(t, db, `UPDATE schema_migrations SET checksum=$1 WHERE version=$2`, strings.Repeat("f", 64), last.Version)
		defer preflightExec(t, db, `UPDATE schema_migrations SET checksum=$1 WHERE version=$2`, last.Checksum, last.Version)
		report, err := InspectDatabase(ctx, db, manifest, manifest)
		if err != nil || report.DatabaseChecksPassed || report.ReaderSupported || report.WorkloadChecked || report.WorkloadIdle {
			t.Fatalf("unknown catalog accepted: %+v %v", report, err)
		}
		var saved string
		if err := db.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=$1`, last.Version).Scan(&saved); err != nil || saved != strings.Repeat("f", 64) {
			t.Fatalf("preflight changed checksum: %q %v", saved, err)
		}
	})
	t.Run("future database even with matching future manifests", func(t *testing.T) {
		future := manifest
		future.Migrations = append(slices.Clone(manifest.Migrations), database.MigrationRecord{Version: int64(len(manifest.Migrations) + 1), Name: "future_test_schema", Checksum: strings.Repeat("e", 64)})
		last := future.Migrations[len(future.Migrations)-1]
		preflightExec(t, db, `INSERT INTO schema_migrations(version,name,checksum) VALUES($1,$2,$3)`, last.Version, last.Name, last.Checksum)
		defer preflightExec(t, db, `DELETE FROM schema_migrations WHERE version=$1`, last.Version)
		report, err := InspectDatabase(ctx, db, future, future)
		if err != nil || report.DatabaseChecksPassed || report.ReaderSupported || !report.MatchesPrevious || !report.Schema.CanUpgrade || report.WorkloadChecked {
			t.Fatalf("reader guessed future workload: %+v %v", report, err)
		}
	})
	t.Run("read-only enforced by PostgreSQL", func(t *testing.T) {
		queries := releaseWorkloadQueries
		releaseWorkloadQueries = append(slices.Clone(queries), workloadQuery{"write_probe", 1, `WITH erased AS (DELETE FROM public.users RETURNING 1) SELECT count(*) FROM erased`})
		defer func() { releaseWorkloadQueries = queries }()
		report, err := InspectDatabase(ctx, db, manifest, manifest)
		if err == nil || report.DatabaseChecksPassed || report.SQLState != "25006" || report.WorkloadChecked || report.FailureCode != "workload_unavailable_write_probe" {
			t.Fatalf("transaction was writable: %+v %v", report, err)
		}
	})
	t.Run("known older catalog omits later features explicitly", func(t *testing.T) {
		old := manifest
		old.Migrations = slices.Clone(manifest.Migrations[:68])
		preflightExec(t, db, `DELETE FROM schema_migrations WHERE version>68`)
		defer func() {
			for _, item := range manifest.Migrations[68:] {
				preflightExec(t, db, `INSERT INTO schema_migrations(version,name,checksum) VALUES($1,$2,$3)`, item.Version, item.Name, item.Checksum)
			}
		}()
		report, err := InspectDatabase(ctx, db, old, manifest)
		if err != nil || !report.DatabaseChecksPassed || !report.Schema.RestoreRequired || report.Schema.BinaryRollback {
			t.Fatalf("known prefix: %+v %v", report, err)
		}
		for _, item := range report.Workload {
			if item.Kind == "executor_permits" && item.Applicable {
				t.Fatal("later feature was implicitly checked")
			}
		}
	})
	t.Run("claimed table missing", func(t *testing.T) {
		preflightExec(t, db, `ALTER TABLE comfy_storage_operations RENAME TO preflight_hidden_storage`)
		defer preflightExec(t, db, `ALTER TABLE preflight_hidden_storage RENAME TO comfy_storage_operations`)
		report, err := InspectDatabase(ctx, db, manifest, manifest)
		if err == nil || report.DatabaseChecksPassed || report.WorkloadChecked || report.WorkloadIdle || report.SQLState != "42P01" || report.FailureCode != "workload_unavailable_comfy_storage" {
			t.Fatalf("missing table treated as empty: %+v %v", report, err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		report, err := InspectDatabase(cancelled, db, manifest, manifest)
		if err == nil || report.DatabaseChecksPassed || report.DeploymentAuthorized || report.FailureCode == "" {
			t.Fatalf("cancellation passed: %+v %v", report, err)
		}
	})
}

func TestDatabasePreflightActiveAndUncertainWork(t *testing.T) {
	db := releaseTestDatabase(t)
	_, manifest := fixtureManifest(t)
	preflightExec(t, db, `INSERT INTO users(username,password_hash,role) VALUES('private_preflight_user','test','user')`)
	preflightExec(t, db, `INSERT INTO gpu_resources(id) VALUES('gpu0')`)
	check := func(kind string, want int64) {
		t.Helper()
		report, err := InspectDatabase(context.Background(), db, manifest, manifest)
		if err != nil || !report.WorkloadChecked || report.DeploymentAuthorized || observationCount(t, report, kind) != want {
			t.Fatalf("%s: %+v %v", kind, report, err)
		}
		if want > 0 && (report.WorkloadIdle || report.DatabaseChecksPassed || !slices.Contains(report.Blockers, "workload_"+kind)) {
			t.Fatalf("%s did not block: %+v", kind, report)
		}
		data, _ := json.Marshal(report)
		if strings.Contains(string(data), "private_preflight_user") || strings.Contains(string(data), "private-prompt") {
			t.Fatal("observation leaked private job data")
		}
	}
	t.Run("mining resume obligations", func(t *testing.T) {
		preflightExec(t, db, `INSERT INTO miners(name,script_path,process_name) VALUES('fixture','test-only-path','fixture.exe')`)
		preflightExec(t, db, `INSERT INTO quick_generation_mining_leases(id,user_id,miner_id,script_path,process_name,resume_mining,resume_ready)
		 VALUES('test-mining-lease',1,1,'test-only-path','fixture.exe',false,true)`)
		check("mining_leases", 1)
		preflightExec(t, db, `DELETE FROM quick_generation_mining_leases WHERE id='test-mining-lease'`)
		check("mining_leases", 0)
	})
	preflightExec(t, db, `INSERT INTO feature_suggestions(title,description_cipher,links_cipher,json_cipher,status) VALUES('private-prompt','x','x','x','draft')`)
	preflightExec(t, db, `INSERT INTO lora_dataset_assets(id,user_id,name,content_hash,mime_type,size_bytes,width,height) VALUES('test-asset',1,'private-prompt',repeat('c',64),'image/png',1,512,512)`)
	preflightExec(t, db, `INSERT INTO gpu_work(id,resource_id,kind,job_key,state) VALUES('preflight_assistant_job','gpu0','prompt_assistant','preflight_assistant_job','released')`)
	for _, fixture := range []struct {
		kind, table, column, setup string
		active, terminal           []string
	}{
		{"legacy_generation", "quick_generation_variants", "state", `INSERT INTO quick_generation_variants(user_id,prompt_id,seed,payload_cipher) VALUES(1,'private-prompt',1,'x')`, []string{"queued", "running"}, []string{"completed", "error", "cancelled"}},
		{"suggestion_scans", "feature_suggestion_scans", "status", `INSERT INTO feature_suggestion_scans(suggestion_id,kind,source_name) VALUES(1,'url','private-prompt')`, []string{"queued", "in-progress"}, []string{"completed", "error"}},
		{"caption", "lora_caption_jobs", "state", `INSERT INTO lora_caption_jobs(id,user_id,asset_id,request_key,instruction_version,input_cipher) VALUES('preflight_caption_job',1,'test-asset',repeat('d',64),'test','x')`, []string{"queued", "running"}, []string{"completed", "failed", "cancelled"}},
		{"assistant", "prompt_assistant_jobs", "state", `INSERT INTO prompt_assistant_jobs(id,user_id,mode,request_hash,body_hash,request_cipher,metadata_cipher) VALUES('preflight_assistant_job',1,'text-to-image',repeat('e',64),repeat('f',64),'x','x')`, []string{"queued", "running"}, []string{"completed", "failed", "cancelled"}},
		{"caption_series", "lora_caption_series", "state", `INSERT INTO lora_caption_series(id,user_id,name,total_count) VALUES('preflight_caption_series',1,'private-prompt',1)`, []string{"running"}, []string{"completed", "failed", "cancelled", "partial"}},
		{"training", "lora_training_jobs", "state", `INSERT INTO lora_training_jobs(public_id,request_id,profile_id,family,base_model,name,output_name,trigger_word,concept_type,preset,resolution,max_train_steps,network_dim,network_alpha,learning_rate,seed,sample_count,dataset_bytes,dataset_path)
		 VALUES('preflight_training_job','preflight_training_request','test','krea2','test-model','private-prompt','test-lora','test','style','quick',512,100,8,8,0.0001,1,5,1,'test-only-path')`, []string{"queued", "uploading", "preparing", "caching", "running", "installing"}, []string{"completed", "failed", "cancelled"}},
	} {
		t.Run(fixture.kind+" lifecycle", func(t *testing.T) {
			preflightExec(t, db, fixture.setup)
			for _, state := range fixture.active {
				preflightExec(t, db, `UPDATE `+fixture.table+` SET `+fixture.column+`=$1`, state)
				check(fixture.kind, 1)
			}
			for _, state := range fixture.terminal {
				preflightExec(t, db, `UPDATE `+fixture.table+` SET `+fixture.column+`=$1`, state)
				check(fixture.kind, 0)
			}
		})
	}
	t.Run("inference needs positive terminal evidence", func(t *testing.T) {
		preflightExec(t, db, `INSERT INTO gpu_inference_requests(id,lease_token,request_hash,request_cipher,timeout_ms) VALUES('preflight_assistant_job','test-lease',repeat('a',64),'x',1000)`)
		check("inference", 1)
		for _, state := range []string{"running", "uncertain"} {
			preflightExec(t, db, `UPDATE gpu_inference_requests SET state=$1,started_at=now(),run_token='test-run'`, state)
			check("inference", 1)
		}
		for _, terminal := range []struct{ state, code string }{{"completed", "executor_terminal"}, {"not_dispatched", "request_not_dispatched"}, {"stopped", "executor_stopped"}} {
			preflightExec(t, db, `UPDATE gpu_inference_requests SET state=$1,completion_code=$2,executor_id=repeat('b',64),executor_generation=1`, terminal.state, terminal.code)
			check("inference", 0)
		}
	})
	t.Run("generation states", func(t *testing.T) {
		preflightExec(t, db, `INSERT INTO generation_jobs(public_id,request_id,username_snapshot) VALUES('preflight_generation_id','private-prompt','private_preflight_user')`)
		for _, state := range []string{"draft", "preparing", "uploading", "waiting_for_resources", "queued", "running", "postprocessing", "archiving"} {
			preflightExec(t, db, `UPDATE generation_jobs SET state=$1`, state)
			check("generation", 1)
		}
		for _, state := range []string{"completed", "failed", "cancelled", "expired"} {
			preflightExec(t, db, `UPDATE generation_jobs SET state=$1`, state)
			check("generation", 0)
		}
	})
	t.Run("expired leases remain blockers", func(t *testing.T) {
		preflightExec(t, db, `INSERT INTO gpu_work(id,resource_id,kind,job_key) VALUES('preflight_gpu_work','gpu0','generation','private-prompt')`)
		check("gpu_work", 1)
		for _, state := range []string{"held", "uncertain"} {
			preflightExec(t, db, `UPDATE gpu_work SET state=$1,lease_token='private-lease',lease_until=now()-interval '7 days' WHERE id='preflight_gpu_work'`, state)
			check("gpu_work", 1)
		}
		preflightExec(t, db, `UPDATE gpu_work SET state='released',lease_token='',lease_until=NULL WHERE id='preflight_gpu_work'`)
		check("gpu_work", 0)
	})
	t.Run("executor permits do not expire by observation", func(t *testing.T) {
		preflightExec(t, db, `INSERT INTO gpu_executor_permits(id,resource_id,executor,instance_id,token_hash,created_at,updated_at)
		 VALUES(repeat('a',32),'gpu0','comfyui',repeat('b',32),repeat('c',64),now()-interval '7 days',now()-interval '7 days')`)
		check("executor_permits", 1)
		preflightExec(t, db, `UPDATE gpu_executor_permits SET state='running'`)
		check("executor_permits", 1)
		preflightExec(t, db, `UPDATE gpu_executor_permits SET state='closed',completion_code='quiesced'`)
		check("executor_permits", 0)
	})
	t.Run("legacy cleanup uncertainty", func(t *testing.T) {
		preflightExec(t, db, `INSERT INTO comfy_input_assets(id,size_bytes,content_hash) VALUES(repeat('a',32),1,repeat('b',64))`)
		preflightExec(t, db, `INSERT INTO comfy_output_cleanup_tombstones(filename,size_bytes,content_hash) VALUES('private-prompt.png',1,repeat('b',64))`)
		for _, state := range []string{"legacy_retry", "unconfirmed", "pending", "host_unavailable"} {
			preflightExec(t, db, `UPDATE comfy_input_assets SET cleanup_last_outcome=$1`, state)
			preflightExec(t, db, `UPDATE comfy_output_cleanup_tombstones SET last_outcome=$1`, state)
			want := int64(0)
			if state == "legacy_retry" || state == "unconfirmed" {
				want = 1
			}
			check("input_cleanup_unconfirmed", want)
			check("output_cleanup_unconfirmed", want)
		}
	})
	for _, table := range []string{"training_storage", "comfy_storage"} {
		t.Run(table+" uncertain outcomes", func(t *testing.T) {
			column := "preview"
			data := `'{}'::jsonb`
			if table == "comfy_storage" {
				column, data = "selection", `'[{}]'::jsonb`
			}
			preflightExec(t, db, `INSERT INTO `+table+`_operations(id,resource_id,actor_name,host_key,request_hash,request,`+column+`,item_count,expected_bytes)
			 VALUES(repeat('d',32),'gpu0','private_preflight_user',repeat('e',64),repeat('f',64),'{}',`+data+`,1,0)`)
			check(table, 0)
			for _, state := range []string{"dispatching", "unconfirmed"} {
				preflightExec(t, db, `UPDATE `+table+`_operations SET state=$1,dispatched_at=now()`, state)
				check(table, 1)
			}
			preflightExec(t, db, `UPDATE `+table+`_operations SET state='closed_unconfirmed',finished_at=now()`)
			check(table, 1)
			preflightExec(t, db, `UPDATE `+table+`_operations SET state='completed'`)
			check(table, 0)
		})
	}
}

func TestDatabasePreflightActualCLI(t *testing.T) {
	packageDirectory := os.Getenv("RELEASE_TEST_PACKAGE")
	if packageDirectory == "" {
		t.Skip("RELEASE_TEST_PACKAGE is not set; ordinary database tests still run")
	}
	packageDirectory, err := filepath.Abs(packageDirectory)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(packageDirectory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Read(file)
	_ = file.Close()
	if err != nil || !slices.Equal(manifest.Migrations, database.ReleaseMigrations()) {
		t.Fatalf("CLI fixture must contain the current catalog: %v", err)
	}
	db := releaseTestDatabase(t)
	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	dsn := releaseTestDSN(t, os.Getenv("TEST_DATABASE_URL"), name)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "release-tool")
	if os.Getenv("GOOS") == "windows" || os.PathSeparator == '\\' {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/release-tool")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual CLI: %v %s", err, output)
	}
	probe := func(previous, connection string, wantExit int) (DatabaseObservation, string) {
		t.Helper()
		command := exec.CommandContext(ctx, binary, "--mode", "database-preflight", "--directory", packageDirectory, "--previous-directory", previous)
		command.Env = append(os.Environ(), "DATABASE_URL="+connection)
		command.Dir = t.TempDir()
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if command.ProcessState == nil || command.ProcessState.ExitCode() != wantExit {
			t.Fatalf("CLI exit (want %d): %v %s %s", wantExit, err, stdout.String(), stderr.String())
		}
		var report DatabaseObservation
		if stdout.Len() > 0 {
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("CLI did not emit one JSON document: %v %s", err, stdout.String())
			}
		}
		return report, stderr.String()
	}
	t.Run("clean observation is not an installation permit", func(t *testing.T) {
		report, stderr := probe(packageDirectory, dsn, 0)
		if !report.DatabaseChecksPassed || report.DeploymentAuthorized || stderr != "" || !report.WorkloadChecked {
			t.Fatalf("clean CLI result: %+v %s", report, stderr)
		}
	})
	t.Run("blockers return JSON and failure", func(t *testing.T) {
		preflightExec(t, db, `INSERT INTO generation_jobs(public_id,request_id) VALUES('cli_generation_job','private-prompt')`)
		report, _ := probe(packageDirectory, dsn, 1)
		if report.DatabaseChecksPassed || report.DeploymentAuthorized || observationCount(t, report, "generation") != 1 {
			t.Fatalf("blocked CLI result: %+v", report)
		}
	})
	t.Run("explicit database required", func(t *testing.T) {
		report, stderr := probe(packageDirectory, "", 1)
		if report.Scope != "" || !strings.Contains(stderr, "DATABASE_URL is required") {
			t.Fatalf("CLI implicitly selected a database: %+v %s", report, stderr)
		}
	})
	t.Run("previous files verified before any database access", func(t *testing.T) {
		missing := t.TempDir()
		data, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(missing, "manifest.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		report, stderr := probe(missing, "host=127.0.0.1 port=1 password=never-print-me", 1)
		if report.Scope != "" || strings.Contains(stderr, "database observation") || strings.Contains(stderr, "never-print-me") {
			t.Fatalf("unverified previous files reached DB: %+v %s", report, stderr)
		}
	})
}

func TestDatabasePreflightConsistentSnapshot(t *testing.T) {
	db := releaseTestDatabase(t)
	_, manifest := fixtureManifest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const lockID = 198734561
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID)
	queries := releaseWorkloadQueries
	releaseWorkloadQueries = append([]workloadQuery{{"snapshot_barrier", 1, `SELECT count(*) FROM (SELECT pg_advisory_xact_lock(198734561)) AS barrier`}}, queries...)
	defer func() { releaseWorkloadQueries = queries }()
	type result struct {
		report DatabaseObservation
		err    error
	}
	done := make(chan result, 1)
	go func() {
		report, err := InspectDatabase(ctx, db, manifest, manifest)
		done <- result{report, err}
	}()
	finished := false
	defer func() {
		cancel()
		if !finished {
			<-done
		}
	}()
	// Wait for the exact query to be blocked after the migration snapshot exists.
	for {
		var waiting bool
		if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=$1 AND NOT granted)`, lockID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	preflightExec(t, db, `INSERT INTO generation_jobs(public_id,request_id) VALUES('snapshot_generation_id','private-prompt')`)
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, lockID); err != nil {
		t.Fatal(err)
	}
	first := <-done
	finished = true
	if first.err != nil || observationCount(t, first.report, "generation") != 0 {
		t.Fatalf("mixed database snapshots: %+v %v", first.report, first.err)
	}
	releaseWorkloadQueries = queries
	second, err := InspectDatabase(ctx, db, manifest, manifest)
	if err != nil || observationCount(t, second, "generation") != 1 || second.DatabaseChecksPassed {
		t.Fatalf("new observation reused stale state: %+v %v", second, err)
	}
}
