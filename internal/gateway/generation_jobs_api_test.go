package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/config"
	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
)

func TestGenerationJobViewTranslatesTerminalJob(t *testing.T) {
	cipher, err := contentcrypto.NewCipher("generation-job-view-test")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(generationSavedPayload{Version: 1, Values: map[string]string{"positive_prompt": "portrait in studio"}})
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt(string(payload))
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	finished := created.Add(95 * time.Second)
	app := &App{cfg: Config{Retention: config.RetentionPolicy{GenerationHistory: 24 * time.Hour}}, contentCipher: cipher}
	view := app.generationJobView(domain.GenerationJob{
		PublicID: "job_view_1234567890", RequestID: "request_view_1234567890", State: domain.GenerationJobFailed,
		StatusMessage: "ComfyUI завершил генерацию с ошибкой", ErrorCode: "comfy_execution_failed",
		TemplateID: "text-to-image", WorkflowID: "photoflow-krea2", ModelName: "Krea2.safetensors",
		Seed: 42, Attempt: 1, PayloadCipher: encrypted, CreatedAt: created, UpdatedAt: finished, FinishedAt: &finished,
	})
	if view.State != "error" || view.JobState != "failed" || !view.Retryable || view.Cancellable {
		t.Fatalf("terminal generation job view = %+v", view)
	}
	if view.Prompt != "portrait in studio" || view.DurationSeconds != 95 {
		t.Fatalf("generation job prompt/duration = %q/%d", view.Prompt, view.DurationSeconds)
	}
	if view.ExpiresAt == nil || !view.ExpiresAt.Equal(finished.Add(24*time.Hour)) {
		t.Fatalf("generation job expiry = %v", view.ExpiresAt)
	}
}

func TestGenerationJobViewMarksCancellationInProgress(t *testing.T) {
	now := time.Now()
	app := &App{}
	view := app.generationJobView(domain.GenerationJob{
		PublicID: "job_cancel_123456789", RequestID: "request_cancel_123456789",
		State: domain.GenerationJobRunning, StatusMessage: "Отменяем генерацию",
		CancellationRequestedAt: &now, CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	})
	if view.State != "cancelling" || view.Cancellable || view.Retryable {
		t.Fatalf("cancelling generation job view = %+v", view)
	}
}

func TestWriteGenerationJobRevisionEvent(t *testing.T) {
	var output bytes.Buffer
	if err := writeGenerationJobRevisionEvent(&output, "jobs", 27); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "event: jobs\n") || !strings.Contains(got, "data: 27\n\n") {
		t.Fatalf("generation job event = %q", got)
	}
}
