package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/store"
)

func TestModelInstallDurability(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `TRUNCATE model_install_jobs,model_install_requests`)
	if err != nil {
		t.Fatal(err)
	}
	var admin, user int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('model-install-admin','test','admin') RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('model-install-user','test','user') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(`DELETE FROM users WHERE id IN ($1,$2)`, admin, user) }()
	s := store.New(db)
	j := store.ModelInstall{ID: strings.Repeat("a", 32), OwnerID: user, ModelID: 5, VersionID: 7, Kind: "loras", Name: "Studio light", Filename: "studio.safetensors", SHA256: strings.Repeat("b", 64), Size: 1024, Request: []byte(`{"model_version_id":7,"model_root":"C:/ComfyUI/models/loras"}`)}
	if _, err = s.EnqueueModelInstall(ctx, j); err == nil {
		t.Fatal("user could enqueue")
	}
	j.OwnerID = admin
	for i := 0; i < 2; i++ {
		got, err := s.EnqueueModelInstall(ctx, j)
		if err != nil || got.State != "queued" {
			t.Fatalf("enqueue %d: %+v %v", i, got, err)
		}
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM model_install_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate jobs: %d %v", count, err)
	}
	changed := j
	changed.Request = []byte(`{"model_version_id":8}`)
	if _, err = s.EnqueueModelInstall(ctx, changed); !errors.Is(err, store.ErrModelInstallConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	changed = j
	changed.ID = strings.Repeat("c", 32)
	if _, err = s.EnqueueModelInstall(ctx, changed); !errors.Is(err, store.ErrModelInstallConflict) {
		t.Fatalf("duplicate version: %v", err)
	}
	claimed, release, err := s.ClaimModelInstall(ctx)
	if err != nil || claimed.ID != j.ID {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if _, _, err = store.New(db).ClaimModelInstall(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("second worker acquired active download")
	}
	release()
	if _, _, err = store.New(db).ClaimModelInstall(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("restart replayed claimed request")
	}
	if err = s.UpdateModelInstall(ctx, j.ID, "reconciling", "verify", 100); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateModelInstall(ctx, j.ID, "running", "stale poll", 99); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ModelInstalls(ctx, true)
	if err != nil || len(pending) != 1 || pending[0].State != "reconciling" || pending[0].Message != "verify" {
		t.Fatalf("stale poll reverted verification: %+v %v", pending, err)
	}
	if err = s.UpdateModelInstall(ctx, j.ID, "completed", "done", 100); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateModelInstall(ctx, j.ID, "failed", "late", 0); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ModelInstalls(ctx, false)
	if err != nil || len(jobs) != 1 || jobs[0].State != "completed" {
		t.Fatalf("terminal state lost: %+v %v", jobs, err)
	}
	j.ID = strings.Repeat("d", 32)
	j.VersionID = 8
	if _, err = s.EnqueueModelInstall(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelQueuedModelInstall(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.EnqueueModelInstall(ctx, j); err != nil || replay.State != "cancelled" {
		t.Fatal("cancelled request replayed")
	}
	j.ID = strings.Repeat("f", 32)
	j.VersionID = 10
	if err = s.CloseModelInstallRequest(ctx, j.ID, admin); err != nil {
		t.Fatal(err)
	}
	if late, err := s.EnqueueModelInstall(ctx, j); err != nil || late.State != "cancelled" {
		t.Fatal("late POST crossed cancellation fence")
	}
	if receipt, err := s.ModelInstallReceipt(ctx, j.ID); err != nil || receipt.State != "cancelled" {
		t.Fatal("lost cancellation receipt")
	}
	j.ID = strings.Repeat("e", 32)
	j.VersionID = 9
	if _, err = s.EnqueueModelInstall(ctx, j); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE users SET disabled=true WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ClaimModelInstall(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("revoked admin download claimed")
	}
}
