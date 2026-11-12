package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestAdminGPUOverviewIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	if _, err := db.ExecContext(ctx, `TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	s := store.New(db)
	empty, err := s.AdminGPUOverview(ctx)
	if err != nil || len(empty) != 4 {
		t.Fatalf("empty summary: %+v %v", empty, err)
	}
	var user int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('ops-gpu','disabled','user') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []domain.GPUWorkKind{domain.GPUWorkGeneration, domain.GPUWorkTraining, domain.GPUWorkCaption, domain.GPUWorkAssistant} {
		if _, err := s.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: string(kind), ResourceID: domain.GPUPrimaryResource, Kind: kind, JobKey: string(kind), UserID: user}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(kind domain.GPUWorkKind) store.AdminGPUWorkSummary {
		t.Helper()
		groups, err := s.AdminGPUOverview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range groups {
			if item.Kind == kind {
				return item
			}
		}
		t.Fatal("missing work kind")
		return store.AdminGPUWorkSummary{}
	}
	exec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if got := read(domain.GPUWorkAssistant); got.Waiting != 1 || got.SnapshotAt.IsZero() {
		t.Fatalf("eligible assistant: %+v", got)
	}
	exec(`UPDATE gpu_work SET state='held',phase='running',lease_token='private-token',lease_until=now()+interval '1 minute' WHERE id='generation'`)
	if got := read(domain.GPUWorkGeneration); got.Running != 1 || got.Waiting != 0 {
		t.Fatalf("running: %+v", got)
	}
	exec(`UPDATE gpu_work SET phase='reserved' WHERE id='generation'`)
	if got := read(domain.GPUWorkGeneration); got.Reserved != 1 || got.Running != 0 {
		t.Fatalf("reserved: %+v", got)
	}
	exec(`UPDATE gpu_work SET lease_until=now()-interval '1 second' WHERE id='generation'`)
	if got := read(domain.GPUWorkGeneration); got.Unconfirmed != 1 || got.Reserved != 0 {
		t.Fatalf("expired holder: %+v", got)
	}
	exec(`UPDATE gpu_work SET state='uncertain',lease_until=now()+interval '1 minute' WHERE id='generation'`)
	if got := read(domain.GPUWorkGeneration); got.Unconfirmed != 1 {
		t.Fatalf("uncertain: %+v", got)
	}
	exec(`UPDATE gpu_work SET ready_until=now()-interval '1 second' WHERE id='lora_caption'`)
	if got := read(domain.GPUWorkCaption); got.Paused != 1 || got.Waiting != 0 {
		t.Fatalf("expired intent: %+v", got)
	}
	exec(`UPDATE users SET disabled=true WHERE username='ops-gpu'`)
	if got := read(domain.GPUWorkTraining); got.Paused != 1 || got.Waiting != 0 {
		t.Fatalf("disabled owner: %+v", got)
	}
	exec(`UPDATE users SET disabled=false,account_expires_at=now()-interval '1 second' WHERE username='ops-gpu'`)
	if got := read(domain.GPUWorkTraining); got.Paused != 1 {
		t.Fatalf("expired account: %+v", got)
	}
	exec(`UPDATE gpu_work SET state='released',lease_token='',lease_until=NULL WHERE id='generation'`)
	if got := read(domain.GPUWorkGeneration); got.Unconfirmed != 0 || got.Running != 0 {
		t.Fatalf("released: %+v", got)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM gpu_work`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("read mutated work: %d %v", count, err)
	}
}
