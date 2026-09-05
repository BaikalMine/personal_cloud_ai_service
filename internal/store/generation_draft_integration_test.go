package store_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/store"
)

func assertGenerationDraftLifecycle(t *testing.T, ctx context.Context, db *sql.DB, repository *store.Store, userID int64) {
	t.Helper()
	if _, err := repository.GenerationDraft(ctx, userID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty draft: %v", err)
	}
	row, err := repository.SaveGenerationDraft(ctx, userID, 0, []byte("encrypted-first"))
	if err != nil || row.Revision <= 0 || time.Until(row.ExpiresAt) < 29*24*time.Hour {
		t.Fatalf("create draft: %+v %v", row, err)
	}
	if _, err := repository.SaveGenerationDraft(ctx, userID, 0, []byte("duplicate")); !errors.Is(err, store.ErrGenerationDraftConflict) {
		t.Fatalf("concurrent insert: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repository.SaveGenerationDraft(ctx, userID, row.Revision, []byte("encrypted-next"))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	saved, conflicts := 0, 0
	for err := range results {
		if err == nil {
			saved++
		} else if errors.Is(err, store.ErrGenerationDraftConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if saved != 1 || conflicts != 7 {
		t.Fatalf("CAS winners=%d conflicts=%d", saved, conflicts)
	}
	if err := repository.DeleteGenerationDraft(ctx, userID, row.Revision); !errors.Is(err, store.ErrGenerationDraftConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	current, err := repository.GenerationDraft(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteGenerationDraft(ctx, userID, current.Revision); err != nil {
		t.Fatal(err)
	}
	recreated, err := repository.SaveGenerationDraft(ctx, userID, 0, []byte("recreated"))
	if err != nil || recreated.Revision <= current.Revision {
		t.Fatalf("recreate: %+v %v", recreated, err)
	}
	if _, err := repository.SaveGenerationDraft(ctx, userID, row.Revision, []byte("stale")); !errors.Is(err, store.ErrGenerationDraftConflict) {
		t.Fatalf("ABA overwrite: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE generation_drafts SET expires_at=now()-interval '1 second' WHERE user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GenerationDraft(ctx, userID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired read: %v", err)
	}
	if _, err := repository.SaveGenerationDraft(ctx, userID, recreated.Revision, []byte("stale")); !errors.Is(err, store.ErrGenerationDraftConflict) {
		t.Fatalf("expired update: %v", err)
	}
	replaced, err := repository.SaveGenerationDraft(ctx, userID, 0, []byte("replace expired"))
	if err != nil || replaced.Revision <= recreated.Revision {
		t.Fatalf("expired replace: %+v %v", replaced, err)
	}
	if count, err := repository.DeleteExpiredGenerationDrafts(ctx); err != nil || count != 0 {
		t.Fatalf("cleanup live: %d %v", count, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE generation_drafts SET expires_at=now()-interval '1 second' WHERE user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.DeleteExpiredGenerationDrafts(ctx); err != nil || count != 1 {
		t.Fatalf("cleanup expired: %d %v", count, err)
	}
}
