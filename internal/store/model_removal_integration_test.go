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

func TestModelRemovalGuard(t *testing.T) {
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
	resetIntegrationDatabase(t, db)
	s := store.New(db)
	if err = s.WithGPUAdmissionGuard(ctx, func() error {
		return s.WithModelRemovalGuard(ctx, func() error {
			if err := store.New(db).WithModelRemovalGuard(ctx, func() error { t.Fatal("concurrent delete ran"); return nil }); !errors.Is(err, store.ErrModelInstallConflict) {
				t.Fatalf("second removal: %v", err)
			}
			if _, _, err := s.ClaimModelInstall(ctx); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("download crossed guard: %v", err)
			}
			if err := s.WithGPUAdmissionGuard(ctx, func() error { t.Fatal("admission crossed guard"); return nil }); !errors.Is(err, store.ErrGPUWorkConflict) {
				t.Fatal(err)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	var admin int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('remove-test','test','admin') RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	j := store.ModelInstall{ID: strings.Repeat("a", 32), OwnerID: admin, ModelID: 5, VersionID: 7, Kind: "loras", Name: "test", Filename: "test.safetensors", SHA256: strings.Repeat("b", 64), Size: 16, Request: []byte(`{}`)}
	if _, err = s.EnqueueModelInstall(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err = s.WithModelRemovalGuard(ctx, func() error { t.Fatal("removed while install queued"); return nil }); !errors.Is(err, store.ErrModelInstallConflict) {
		t.Fatal(err)
	}
	if err = s.CancelQueuedModelInstall(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("verification failed")
	if err = s.WithModelRemovalGuard(ctx, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err = s.WithModelRemovalGuard(ctx, func() error { return nil }); err != nil {
		t.Fatal("failed action left lock held", err)
	}
}
