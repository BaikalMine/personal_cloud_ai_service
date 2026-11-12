package store_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestComfyCleanupRechecksExpiredCandidatesIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	r := store.New(db)
	var owner int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('cleanup-owner','hash','user') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	event, err := r.InsertContentEvent(ctx, domain.ContentEventRecord{UserID: owner, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: "cleanup-expiry", PromptCipher: []byte{1}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ownership := domain.ComfyOutputOwnership{PromptID: "cleanup-expiry", Filename: "expires.png", StorageType: "output", MediaType: "image", ExpiresAt: time.Now().Add(time.Hour)}
	if err := r.InsertComfyOutputOwnerships(ctx, owner, []domain.ComfyOutputOwnership{ownership}); err != nil {
		t.Fatal(err)
	}
	if err := r.InsertContentMedia(ctx, domain.ContentMediaRecord{EventID: event, MediaType: "image", MIMEType: "image/png", OriginalName: ownership.Filename, StorageType: "output", PayloadCipher: []byte{1}, SizeBytes: 12, ContentHash: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	candidates, err := r.ExpiredComfyMedia(ctx, 100)
	if err != nil || len(candidates) != 1 {
		t.Fatal(candidates, err)
	}
	if _, err := db.Exec(`UPDATE content_media SET expires_at=now()+interval '1 day' WHERE id=$1`, candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	if deleted, err := r.QueueExpiredComfyOutputCleanup(ctx, candidates); err != nil || deleted != 0 {
		t.Fatal("used stale expiry", deleted, err)
	}
	if due, err := r.DueComfyOutputCleanup(ctx, 100); err != nil || len(due) != 0 {
		t.Fatal("queued a live file", due, err)
	}
	if _, err := db.Exec(`UPDATE content_media SET expires_at=now()-interval '1 hour',profile_hidden_at=now() WHERE id=$1`, candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	// Mutating the caller's stale snapshot must not choose the file deleted on disk.
	candidates[0].Filename, candidates[0].SizeBytes, candidates[0].ContentHash = "wrong.png", 99, strings.Repeat("b", 64)
	if deleted, err := r.QueueExpiredComfyOutputCleanup(ctx, candidates); err != nil || deleted != 1 {
		t.Fatal(deleted, err)
	}
	due, err := r.DueComfyOutputCleanup(ctx, 100)
	if err != nil || len(due) != 1 || due[0].Filename != "expires.png" || due[0].SizeBytes != 12 || due[0].ContentHash != strings.Repeat("a", 64) {
		t.Fatal("trusted stale file identity", due, err)
	}
	if _, err := r.RecordComfyOutputCleanup(ctx, []domain.ComfyOutputCleanupResult{{ComfyOutputCleanupTombstone: due[0], Outcome: "missing"}}, time.Hour); err != nil {
		t.Fatal(err)
	}
	receipts, err := r.ComfyOutputCleanupReceipts(ctx, 100)
	if err != nil || len(receipts) != 1 || receipts[0].Reason != "profile_hidden" || receipts[0].FreedBytes != 0 {
		t.Fatal("lost lifecycle reason", receipts, err)
	}
}
