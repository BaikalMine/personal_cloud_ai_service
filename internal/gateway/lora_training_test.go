package gateway

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/security"
)

func TestTruncateLoraTextPreservesUTF8(t *testing.T) {
	t.Parallel()
	result := truncateLoraText("  обучение готово  ", 7)
	if result != "обучени" || strings.ToValidUTF8(result, "") != result {
		t.Fatalf("truncateLoraText returned %q", result)
	}
}

func TestLoraTrainingJSONUsesLiveAgentState(t *testing.T) {
	t.Parallel()
	job := domain.LoraTrainingJob{PublicID: "lora-job-0123456789", State: domain.LoraTrainingRunning, Progress: 45}
	status := &loratraining.JobStatus{State: "installing", Stage: "Установка", Progress: 96, Message: "Копируем файл"}
	result := loraTrainingJSON(job, status, false)
	if result.State != string(domain.LoraTrainingInstalling) || result.StateLabel != "Установка" || result.Progress != 96 || !result.CanCancel {
		t.Fatalf("live LoRA status was not projected: %+v", result)
	}
}

func TestEnsureLoraCaptionTriggerAlwaysUsesExactLeadingTrigger(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		caption string
		want    string
	}{
		{name: "missing", caption: "portrait with anna_person embroidered on a bag", want: "anna_person, portrait with anna_person embroidered on a bag"},
		{name: "normalizes case", caption: "ANNA_PERSON: side profile in daylight", want: "anna_person, side profile in daylight"},
		{name: "removes duplicate prefix", caption: "anna_person, anna_person, seated by a window", want: "anna_person, seated by a window"},
		{name: "does not match a longer word", caption: "anna_personal portrait", want: "anna_person, anna_personal portrait"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ensureLoraCaptionTrigger("anna_person", test.caption); got != test.want {
				t.Fatalf("ensureLoraCaptionTrigger() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLoraCaptionJobRegistryKeepsJobsPrivateAndBounded(t *testing.T) {
	registry := newLoraCaptionJobRegistry()
	job, accepted := registry.enqueue(7)
	if !accepted || job.State != loraCaptionQueued || job.ID == "" {
		t.Fatalf("caption job was not queued: %+v accepted=%t", job, accepted)
	}
	if _, ok := registry.get(8, job.ID); ok {
		t.Fatal("another user can read a caption job")
	}
	registry.update(job.ID, loraCaptionRunning, "Ассистент анализирует кадр")
	registry.complete(job.ID, "subject_token, portrait in window light", "test:vision", "")
	completed, ok := registry.get(7, job.ID)
	if !ok || completed.State != loraCaptionCompleted || completed.Caption == "" || completed.Model != "test:vision" {
		t.Fatalf("completed caption job was not retained: %+v", completed)
	}

	for index := 0; index < maxQueuedLoraCaptionJobsPerUser; index++ {
		if _, accepted := registry.enqueue(11); !accepted {
			t.Fatalf("queued caption %d was rejected before the limit", index)
		}
	}
	if _, accepted := registry.enqueue(11); accepted {
		t.Fatal("caption queue accepted more jobs than the per-user limit")
	}
}

func TestTrainingCaptionsRequireTriggerAtTheBeginning(t *testing.T) {
	t.Parallel()
	captions, err := trainingCaptions([]string{"front portrait with subject_token on a sign"}, "", "subject_token", 1)
	if err != nil {
		t.Fatal(err)
	}
	if captions[0] != "subject_token, front portrait with subject_token on a sign" {
		t.Fatalf("unexpected training caption %q", captions[0])
	}
}

func TestRandomTrainingSeedFitsNumPyRange(t *testing.T) {
	for range 32 {
		seed := randomTrainingSeed()
		if seed < 0 || seed > loratraining.MaxNumpySeed {
			t.Fatalf("randomTrainingSeed() = %d, outside NumPy range", seed)
		}
	}
}

func TestReadLoraCaptionSubmissionAcceptsOnlyOneImage(t *testing.T) {
	const sessionToken = "lora-caption-session"
	app := &App{csrfSigner: security.NewCSRFSigner("lora-caption-secret")}

	singleRequest := newLoraCaptionMultipartRequest(t, app, sessionToken, 1)
	submission, err := app.readLoraCaptionSubmission(httptest.NewRecorder(), singleRequest)
	if err != nil {
		t.Fatal(err)
	}
	if submission.TriggerWord != "subject_token" || submission.ConceptType != "character" || submission.MIMEType != "image/png" || len(submission.Image) == 0 {
		t.Fatalf("unexpected caption submission: %+v", submission)
	}
	clear(submission.Image)

	multipleRequest := newLoraCaptionMultipartRequest(t, app, sessionToken, 2)
	submission, err = app.readLoraCaptionSubmission(httptest.NewRecorder(), multipleRequest)
	clear(submission.Image)
	if err == nil || !strings.Contains(err.Error(), "только одно изображение") {
		t.Fatalf("expected one-image rejection, got %v", err)
	}
}

func newLoraCaptionMultipartRequest(t *testing.T, app *App, sessionToken string, imageCount int) *http.Request {
	t.Helper()
	fixture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	fixture.Set(0, 0, color.RGBA{R: 40, G: 80, B: 120, A: 255})
	var imageBody bytes.Buffer
	if err := png.Encode(&imageBody, fixture); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("csrf", app.csrfSigner.Token(sessionToken)); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("trigger_word", "subject_token"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("concept_type", "character"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < imageCount; index++ {
		part, err := writer.CreateFormFile("image", "frame.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(imageBody.Bytes()); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/lora-training/caption", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	return request
}
