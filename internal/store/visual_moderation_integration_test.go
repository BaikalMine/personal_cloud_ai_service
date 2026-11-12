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

func TestVisualModerationIncludesVideoIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
	repo := store.New(db)
	var owner int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('visual-owner','disabled','user') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	eventID, err := repo.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: owner, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: "visual-video",
		ExpiresAt: expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"image", "video"} {
		if err := repo.InsertContentMedia(ctx, domain.ContentMediaRecord{
			EventID: eventID, MediaType: kind, MIMEType: kind + "/fixture", OriginalName: kind,
			StorageType: "output", PayloadCipher: []byte("fixture"), SizeBytes: 10, ExpiresAt: expires,
		}); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := repo.ListPendingVisualSensitiveMedia(ctx, 8)
	if err != nil || len(pending) != 2 {
		t.Fatalf("visual queue must contain image and video only: %+v, %v", pending, err)
	}
	var videoID int64
	for _, item := range pending {
		if item.MIMEType == "video/fixture" {
			videoID = item.ID
		}
	}
	if videoID == 0 {
		t.Fatal("video absent from visual queue")
	}
	assertPending := func(want bool) {
		t.Helper()
		items, err := repo.ListUserGenerationMedia(ctx, owner, 10)
		if err != nil || len(items) != 2 {
			t.Fatalf("gallery: %+v, %v", items, err)
		}
		for _, item := range items {
			if item.ID == videoID && item.VisualPending != want {
				t.Fatalf("gallery pending=%v want=%v", item.VisualPending, want)
			}
		}
		item, err := repo.UserGenerationMediaByID(ctx, owner, videoID)
		if err != nil || item.VisualPending != want {
			t.Fatalf("detail pending=%v want=%v: %v", item.VisualPending, want, err)
		}
		variants, err := repo.ListGenerationMediaForPrompts(ctx, owner, []string{"visual-video"})
		if err != nil || len(variants["visual-video"]) != 2 {
			t.Fatalf("variant media: %+v, %v", variants, err)
		}
		for _, item := range variants["visual-video"] {
			if item.ID == videoID && item.VisualPending != want {
				t.Fatalf("variant pending=%v want=%v", item.VisualPending, want)
			}
		}
		summaries, err := repo.ListContentMediaSummaries(ctx, []int64{eventID})
		if err != nil || len(summaries[eventID]) != 2 {
			t.Fatalf("summaries: %+v, %v", summaries, err)
		}
		for _, item := range summaries[eventID] {
			if item.ID == videoID && item.VisualPending != want {
				t.Fatalf("summary pending=%v want=%v", item.VisualPending, want)
			}
		}
	}
	assertPending(true)
	if err := repo.SetContentMediaVisualSensitive(ctx, videoID, eventID, false); err != nil {
		t.Fatal(err)
	}
	assertPending(false)
	pending, err = repo.ListPendingVisualSensitiveMedia(ctx, 8)
	if err != nil || len(pending) != 1 || pending[0].ID == videoID {
		t.Fatalf("classified video must leave queue: %+v, %v", pending, err)
	}
	if err := repo.SetContentMediaVisualSensitive(ctx, pending[0].ID, eventID, true); err != nil {
		t.Fatal(err)
	}
	// A safe sibling must never clear an already sensitive generation.
	if err := repo.SetContentMediaVisualSensitive(ctx, videoID, eventID, false); err != nil {
		t.Fatal(err)
	}
	item, err := repo.UserGenerationMediaByID(ctx, owner, videoID)
	if err != nil || !item.Sensitive {
		t.Fatalf("sensitive event flag lost: %+v, %v", item, err)
	}
}
