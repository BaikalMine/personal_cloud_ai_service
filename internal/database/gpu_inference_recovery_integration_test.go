package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestGPUInferencePreparationMigrationProtectsLegacyWork(t *testing.T) {
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
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		resetMigrationIntegrationSchema(t, cleanup, db)
	}()
	if err = applyMigrationsThroughForTest(ctx, db, 61); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ id, kind, state, phase, completion, want string }{
		{"assistant", "prompt_assistant", "held", "reserved", "", "preparing_resources"},
		{"caption", "lora_caption", "uncertain", "reserved", "", "preparing_resources"},
		{"generation", "generation", "held", "reserved", "", "reserved"},
		{"completed", "prompt_assistant", "held", "reserved", "executor_terminal", "reserved"},
		{"waiting", "prompt_assistant", "waiting", "reserved", "", "reserved"},
		{"sent", "prompt_assistant", "held", "dispatching", "", "dispatching"},
	}
	for _, tc := range cases {
		if _, err = db.ExecContext(ctx, `INSERT INTO gpu_resources(id) VALUES($1)`, tc.id); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO gpu_work(id,resource_id,kind,job_key,state,phase,inference_completion,lease_token,lease_until)
			VALUES($1,$1,$2,$1,$3,$4,$5,CASE WHEN $3='waiting' THEN '' ELSE 'legacy' END,CASE WHEN $3='waiting' THEN NULL ELSE now()-interval '1 minute' END)`, tc.id, tc.kind, tc.state, tc.phase, tc.completion); err != nil {
			t.Fatal(err)
		}
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		var phase, completion string
		if err = db.QueryRowContext(ctx, `SELECT phase,inference_completion FROM gpu_work WHERE id=$1`, tc.id).Scan(&phase, &completion); err != nil {
			t.Fatal(err)
		}
		if phase != tc.want || completion != tc.completion {
			t.Fatalf("%s: phase=%s completion=%s", tc.id, phase, completion)
		}
	}
}
