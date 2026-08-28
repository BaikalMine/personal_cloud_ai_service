package gateway

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestAdminContentRendersEveryMediaItem(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Title": "AI content",
		"Events": []ContentEventView{{
			ID: 1, Username: "alice", Prompt: "prompt", MediaCount: 2,
			Media: []domain.ContentMediaSummary{
				{ID: 11, EventID: 1, MediaType: "image"},
				{ID: 12, EventID: 1, MediaType: "video"},
			},
		}},
	}
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "admin_content", data); err != nil {
		t.Fatal(err)
	}
	html := rendered.String()
	for _, expected := range []string{
		`/admin/content/media/11`,
		`/admin/content/media/12`,
		`data-admin-content-gallery`,
		`content-event-detail-1`,
		`content-detail-dialog`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("rendered gallery does not contain %q", expected)
		}
	}
}

func TestAdminMediaViewerRendersCenteredMedia(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "admin_media_viewer", map[string]any{"Title": "Просмотр", "MediaID": int64(42), "MediaType": "image", "Filename": "result.png"}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`admin-media-viewer`, `/admin/content/media/42?raw=1`, `Скачать оригинал`} {
		if !strings.Contains(rendered.String(), expected) {
			t.Fatalf("rendered viewer does not contain %q", expected)
		}
	}
}

func TestAcquireAdminMediaSlotQueuesInsteadOfRejecting(t *testing.T) {
	app := &App{adminMediaSlots: make(chan struct{}, 1)}
	app.adminMediaSlots <- struct{}{}
	go func() {
		time.Sleep(15 * time.Millisecond)
		<-app.adminMediaSlots
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release := app.acquireAdminMediaSlot(ctx)
	if release == nil {
		t.Fatal("media slot should wait for capacity instead of rejecting")
	}
	release()
}
