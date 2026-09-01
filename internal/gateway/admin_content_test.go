package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/config"
	contentcrypto "ai-access-gateway/internal/content"
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

func TestContentTaskViewMergesAssistantAndGeneration(t *testing.T) {
	cipher, err := contentcrypto.NewCipher("content-task-view-test")
	if err != nil {
		t.Fatal(err)
	}
	encrypt := func(value string) []byte {
		ciphertext, encryptErr := cipher.Encrypt(value)
		if encryptErr != nil {
			t.Fatal(encryptErr)
		}
		return ciphertext
	}
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	userID := int64(7)
	jobID := int64(19)
	metadata := `{"prompt_assistant":{"requested":true,"applied":true,"template":"photographic","original_prompt":"portrait","suggestion":"editorial portrait"}}`
	rows := []domain.ContentEventRow{
		{ID: 11, UserID: userID, Username: "alice", GenerationJobID: &jobID, CorrelationID: "trace-content-task", Service: "ollama", Kind: "prompt_assistant", Model: "qwen3-vl", PromptCipher: encrypt("portrait"), ResponseCipher: encrypt("editorial portrait"), MetadataCipher: encrypt(metadata), CreatedAt: created, UpdatedAt: created, ExpiresAt: created.Add(7 * 24 * time.Hour)},
		{ID: 12, UserID: userID, Username: "alice", GenerationJobID: &jobID, CorrelationID: "trace-content-task", Service: "comfyui", Kind: "comfyui_prompt", Model: "Krea2", GenerationState: "completed", PromptCipher: encrypt("editorial portrait with final edits"), ResponseCipher: encrypt(""), MetadataCipher: encrypt(metadata), GeneratedMediaCount: 1, MediaCount: 1, CreatedAt: created.Add(time.Second), UpdatedAt: created.Add(2 * time.Second), ExpiresAt: created.Add(7 * 24 * time.Hour)},
	}
	jobs := []domain.GenerationJob{{
		ID: jobID, PublicID: "job_content_task_1234", CorrelationID: "trace-content-task", UserID: &userID,
		UsernameSnapshot: "alice", RequestID: "request-content-task", ModelName: "Krea2", State: domain.GenerationJobCompleted,
		StatusMessage: "Задание завершено", CreatedAt: created, UpdatedAt: created.Add(3 * time.Second),
	}}
	app := &App{cfg: Config{Retention: config.RetentionPolicy{AIContent: 7 * 24 * time.Hour}}, contentCipher: cipher}
	views, overview := app.buildContentTaskViews(rows, jobs, map[int64][]domain.ContentMediaSummary{
		12: {{ID: 91, EventID: 12, MediaType: "image", UpdatedAt: created.Add(4 * time.Second)}},
	}, "", "", 200)
	if len(views) != 1 || overview.Total != 1 || overview.ComfyUI != 1 {
		t.Fatalf("merged content tasks=%+v overview=%+v", views, overview)
	}
	view := views[0]
	if view.Key != "job-job_content_task_1234" || view.Service != "comfyui" || view.Prompt != "editorial portrait with final edits" || view.MediaCount != 1 {
		t.Fatalf("merged content task=%+v", view)
	}
	if view.Assistant == nil || view.Assistant.Model != "qwen3-vl" || !view.Assistant.Applied || view.Assistant.OriginalPrompt != "portrait" {
		t.Fatalf("merged assistant=%+v", view.Assistant)
	}
}

func TestContentTaskKeepsDeletedAuthorSnapshot(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	view := (&App{}).contentTaskView(&contentTaskGroup{key: "job-deleted", job: &domain.GenerationJob{
		ID: 1, PublicID: "deleted-author-job", UsernameSnapshot: "former-user", State: domain.GenerationJobFailed,
		ErrorMessage: "workflow failed", CreatedAt: created, UpdatedAt: created,
	}}, nil)
	if !view.AuthorDeleted || view.Username != "former-user" || view.UserID != 0 {
		t.Fatalf("deleted author view=%+v", view)
	}
}

func TestContentTasksStayMergedByCorrelationAfterJobCleanup(t *testing.T) {
	cipher, err := contentcrypto.NewCipher("content-task-correlation-test")
	if err != nil {
		t.Fatal(err)
	}
	encrypt := func(value string) []byte {
		ciphertext, encryptErr := cipher.Encrypt(value)
		if encryptErr != nil {
			t.Fatal(encryptErr)
		}
		return ciphertext
	}
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	rows := []domain.ContentEventRow{
		{ID: 21, UserID: 7, Username: "alice", CorrelationID: "trace-retained-task", Service: "ollama", Kind: "prompt_assistant", PromptCipher: encrypt("portrait"), ResponseCipher: encrypt("editorial portrait"), MetadataCipher: encrypt(`{"prompt_assistant":{"requested":true,"original_prompt":"portrait","suggestion":"editorial portrait"}}`), CreatedAt: created, UpdatedAt: created, ExpiresAt: created.Add(7 * 24 * time.Hour)},
		{ID: 22, UserID: 7, Username: "alice", CorrelationID: "trace-retained-task", Service: "comfyui", Kind: "comfyui_prompt", PromptCipher: encrypt("editorial portrait"), ResponseCipher: encrypt(""), MetadataCipher: encrypt(`{}`), GenerationState: "completed", CreatedAt: created.Add(time.Second), UpdatedAt: created.Add(time.Second), ExpiresAt: created.Add(7 * 24 * time.Hour)},
	}
	app := &App{contentCipher: cipher}
	views, _ := app.buildContentTaskViews(rows, nil, nil, "", "", 200)
	if len(views) != 1 || views[0].Key != "trace-trace-retained-task" || views[0].Assistant == nil || views[0].Prompt != "editorial portrait" {
		t.Fatalf("correlation-retained task views=%+v", views)
	}
}

func TestAdminContentRendersEveryMediaItem(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Title": "AI content", "Retention": newRetentionPolicyView(Config{}.Retention),
		"RetentionStats": domain.ContentRetentionStats{},
		"Events": []ContentEventView{{
			ID: 1, Key: "task-1", Version: "1", Username: "alice", Prompt: "prompt", StateLabel: "Готово", StateClass: "is-complete", MediaCount: 2,
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
		`content-task-detail-task-1`,
		`data-content-task-key="task-1"`,
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
		"Title": "AI content", "Retention": newRetentionPolicyView(Config{}.Retention),
		"RetentionStats": domain.ContentRetentionStats{},
		"Events": []ContentEventView{
			{ID: 1, Key: "archived-1", Version: "1", Username: "alice", Service: "comfyui", Prompt: "archived prompt", GenerationState: "completed", StateLabel: "Готово", StateClass: "is-complete", GeneratedMediaCount: 1, MediaExpired: true, MediaExpiresAt: now.Add(-time.Hour), ExpiresAt: now.Add(6 * 24 * time.Hour)},
			{ID: 2, Key: "failed-2", Version: "2", Username: "bob", Service: "comfyui", Prompt: "failed prompt", GenerationState: "error", StateLabel: "Ошибка", StateClass: "is-error", ErrorMessage: "ComfyUI отклонил workflow", ExpiresAt: now.Add(6 * 24 * time.Hour)},
		},
	}
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "admin_content", data); err != nil {
		t.Fatal(err)
	}
	html := rendered.String()
	for _, expected := range []string{
		"Медиа удалено по сроку хранения",
		"Результат хранился 24 часа",
		"Задание завершилось с ошибкой",
		"Файл результата очищен по сроку хранения",
		"ComfyUI отклонил workflow",
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("rendered content state does not contain %q", expected)
		}
	}
	if strings.Contains(html, `/admin/content/media/`) {
		t.Fatal("archived generation rendered a broken media URL")
	}
}

func TestAdminContentRendersRetentionReport(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	nextMedia := time.Date(2026, 8, 31, 18, 0, 0, 0, time.Local)
	nextEvent := nextMedia.Add(6 * 24 * time.Hour)
	data := map[string]any{
		"Title": "AI content", "Retention": newRetentionPolicyView(Config{}.Retention),
		"RetentionStats": domain.ContentRetentionStats{EventCount: 9, MediaCount: 3, MediaBytes: 2 << 20, NextMediaExpiry: &nextMedia, NextEventExpiry: &nextEvent},
	}
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "admin_content", data); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Хранение AI-контента", "2.0 MB", "3 файла", "31.08.2026 18:00", "результаты хранятся 24 часа", "хранятся 7 дней"} {
		if !strings.Contains(rendered.String(), expected) {
			t.Fatalf("retention report does not contain %q", expected)
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
