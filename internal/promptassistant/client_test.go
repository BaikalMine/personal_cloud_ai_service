package promptassistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestEnhanceSendsFlux2EditInstructionAndUnloadsModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "test:e4b" || body.Stream || body.Think || body.KeepAlive != "0" || len(body.Messages) != 2 {
			t.Fatalf("unexpected body: %#v", body)
		}
		if !strings.Contains(body.Messages[0].Content, "image editing") || !strings.Contains(body.Messages[0].Content, "image 1: the base scene") || !strings.Contains(body.Messages[0].Content, "image 2: the person") || body.Messages[1].Content != "change the jacket" {
			t.Fatalf("wrong prompt-assistant context: %#v", body.Messages)
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"<think>draft</think> Keep the same person in a red jacket."}}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(base, "test:e4b")
	result, err := client.Enhance(context.Background(), ModeImageToImage, ProfileFluxEdit, "change the jacket", []ImageReference{
		{Number: 1, Role: ImageReferenceBaseScene},
		{Number: 2, Role: ImageReferenceIdentity},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result != "Keep the same person in a red jacket." {
		t.Fatalf("result = %q", result)
	}
}

func TestEnhanceRejectsEmptyPrompt(t *testing.T) {
	client := NewClient(nil, "test:e4b")
	if _, err := client.Enhance(context.Background(), ModeTextToImage, ProfileWorkflowDefault, " ", nil, false); err == nil {
		t.Fatal("expected empty prompt error")
	}
}

func TestEnhanceCanEnableThinking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !body.Think || body.Options.NumPredict != 1400 || body.KeepAlive != "0" {
			t.Fatalf("thinking request was not configured: %#v", body)
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"A deliberate final prompt."}}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewClient(base, "test:e4b").Enhance(context.Background(), ModeTextToImage, ProfileAnime, "hero portrait", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if result != "A deliberate final prompt." {
		t.Fatalf("result = %q", result)
	}
}

func TestClassifyImageSendsImageAndUnloadsModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/api/chat" || body.Model != "test:e4b" || body.Stream || body.Think || body.KeepAlive != "0" {
			t.Fatalf("unexpected vision request: %#v", body)
		}
		if len(body.Messages) != 2 || len(body.Messages[1].Images) != 1 || body.Messages[1].Images[0] != "c2FtcGxl" {
			t.Fatalf("image was not sent to the vision model: %#v", body.Messages)
		}
		if body.Options.NumPredict != 3 || body.Options.Temperature != 0 {
			t.Fatalf("unexpected vision options: %#v", body.Options)
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"SENSITIVE."}}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	sensitive, err := NewClient(base, "test:e4b").ClassifyImage(context.Background(), []byte("sample"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !sensitive {
		t.Fatal("expected sensitive image")
	}
}

func TestClassifyImageRejectsUnsupportedMedia(t *testing.T) {
	base, err := url.Parse("http://127.0.0.1:11434")
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewClient(base, "test:e4b").ClassifyImage(context.Background(), []byte("sample"), "image/gif")
	if !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("expected unsupported image error, got %v", err)
	}
}

func TestWorkflowProfilesWorkForBothGenerationModes(t *testing.T) {
	profiles := []Profile{ProfileWorkflowDefault, ProfilePhotographic, ProfileRealistic, ProfileAnime, ProfileNSFW}
	for _, profile := range profiles {
		if !ValidProfile(ModeTextToImage, profile) {
			t.Fatalf("profile %q must be available for text-to-image", profile)
		}
		if !ValidProfile(ModeImageToImage, profile) {
			t.Fatalf("profile %q must be available for image-to-image", profile)
		}
		if !ValidProfile(ModeTextToVideo, profile) {
			t.Fatalf("profile %q must be available for text-to-video", profile)
		}
	}
	if ValidProfile(ModeTextToImage, ProfileFluxEdit) {
		t.Fatal("identity-transfer profile must be limited to image-to-image")
	}
	if !ValidProfile(ModeImageToImage, ProfileFluxEdit) {
		t.Fatal("identity-transfer profile must be available for image-to-image")
	}
}
