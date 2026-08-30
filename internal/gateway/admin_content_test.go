package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestContentAssistantIsEmbeddedInGenerationMetadata(t *testing.T) {
	metadata, err := json.Marshal(map[string]any{
		"workflow": "minimax-h3-video",
		"prompt_assistant": map[string]any{
			"requested":       true,
			"applied":         true,
			"template":        "minimax-h3",
			"think":           true,
			"original_prompt": "A calm scene",
			"suggestion":      "A calm cinematic scene with deliberate movement.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assistant := contentAssistantFromMetadata(string(metadata))
	if assistant == nil || !assistant.Applied || !assistant.Think || assistant.Template != "minimax-h3" || assistant.OriginalPrompt != "A calm scene" || assistant.Suggestion == "" {
		t.Fatalf("assistant metadata = %#v", assistant)
	}
}

func TestContentMetadataIsFormattedForAdminReview(t *testing.T) {
	formatted := prettyContentMetadata(`{"workflow":"minimax-h3-video","minimax_h3":{"rife":{"enabled":true}}}`)
	if !strings.Contains(formatted, "\n") || !strings.Contains(formatted, `"rife"`) {
		t.Fatalf("formatted metadata = %q", formatted)
	}
	if got := prettyContentMetadata("not-json"); got != "not-json" {
		t.Fatalf("invalid metadata changed to %q", got)
	}
}

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

func TestAdminContentRendersArchivedAndFailedGenerationStates(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	data := map[string]any{
		"Title": "AI content",
		"Events": []ContentEventView{
			{ID: 1, Username: "alice", Service: "comfyui", Prompt: "archived prompt", GenerationState: "completed", GeneratedMediaCount: 1, MediaExpired: true, MediaExpiresAt: now.Add(-time.Hour), ExpiresAt: now.Add(6 * 24 * time.Hour)},
			{ID: 2, Username: "bob", Service: "comfyui", Prompt: "failed prompt", GenerationState: "error", ExpiresAt: now.Add(6 * 24 * time.Hour)},
		},
	}
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "admin_content", data); err != nil {
		t.Fatal(err)
	}
	html := rendered.String()
	for _, expected := range []string{
		"Результат очищен после трёх дней",
		"Генерация с ошибкой",
		"Файл результата очищен по сроку хранения",
		"ComfyUI завершил эту генерацию с ошибкой",
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("rendered content state does not contain %q", expected)
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

func TestWriteContentRevisionEvent(t *testing.T) {
	var output bytes.Buffer
	if err := writeContentRevisionEvent(&output, "content", 42); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "event: content\ndata: 42\n\n"; got != want {
		t.Fatalf("event output = %q, want %q", got, want)
	}
}
