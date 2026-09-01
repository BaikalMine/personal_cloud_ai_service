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
	"time"
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
		if body.Model != "test:e4b" || body.Stream || body.Think || body.KeepAlive != "0" || body.Format != "json" || len(body.Messages) != 2 {
			t.Fatalf("unexpected body: %#v", body)
		}
		if !strings.Contains(body.Messages[0].Content, "image editing") || !strings.Contains(body.Messages[0].Content, "image 1: the base scene") || !strings.Contains(body.Messages[0].Content, "image 2: the person") || body.Messages[1].Content != "change the jacket" {
			t.Fatalf("wrong prompt-assistant context: %#v", body.Messages)
		}
		writeAssistantResponse(t, w, `{"prompt":"Keep the same person in a red jacket.","references":[{"id":"Picture 1","summary":"A street portrait with a woman in a dark jacket.","use":"Preserve the base scene and composition."},{"id":"Picture 2","summary":"A woman with long blonde hair and pale skin.","use":"Preserve the person's identity."}]}`)
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
		if !body.Think || body.Options.NumPredict != DefaultImageThinkNumPredict || body.KeepAlive != "0" || body.Format != "json" {
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

func TestWorkflowProfilesAreLimitedToCompatibleGenerationModes(t *testing.T) {
	profiles := []Profile{ProfileWorkflowDefault, ProfilePhotographic, ProfileRealistic, ProfileAnime, ProfileNSFW}
	for _, profile := range profiles {
		if !ValidProfile(ModeTextToImage, profile) {
			t.Fatalf("profile %q must be available for text-to-image", profile)
		}
		if !ValidProfile(ModeImageToImage, profile) {
			t.Fatalf("profile %q must be available for image-to-image", profile)
		}
		if ValidProfile(ModeTextToVideo, profile) {
			t.Fatalf("profile %q must not be available for MiniMax H3 video", profile)
		}
	}
	if ValidProfile(ModeTextToImage, ProfileFluxEdit) {
		t.Fatal("identity-transfer profile must be limited to image-to-image")
	}
	if !ValidProfile(ModeImageToImage, ProfileFluxEdit) {
		t.Fatal("identity-transfer profile must be available for image-to-image")
	}
	if !ValidProfile(ModeTextToVideo, ProfileMiniMaxH3) {
		t.Fatal("MiniMax H3 profile must be available for video")
	}
	if ValidProfile(ModeTextToImage, ProfileMiniMaxH3) || ValidProfile(ModeImageToImage, ProfileMiniMaxH3) {
		t.Fatal("MiniMax H3 profile must be limited to video")
	}
}

func TestEnhanceVideoUsesMiniMaxContextForReferenceAudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		system := body.Messages[0].Content
		if !strings.Contains(system, "prompt-driven Ref2VA with 2 attached image reference") ||
			!strings.Contains(system, "<Audio 1> reference is attached") ||
			!strings.Contains(system, "<Video 1> reference is attached") ||
			!strings.Contains(system, "<Picture 1> (attached image 1): the base scene") ||
			!strings.Contains(system, "<Picture 2> (attached image 2): the person or character's identity") ||
			!strings.Contains(system, "at least three concrete visible attributes") ||
			!strings.Contains(system, "define a human as <Subject 1>") ||
			body.Format != "json" || body.Options.NumPredict != DefaultVideoNumPredict || len(body.Messages[1].Images) != 2 || body.Messages[1].Images[0] != "aW1hZ2UtMQ==" || body.Messages[1].Images[1] != "aW1hZ2UtMg==" {
			t.Fatalf("wrong MiniMax context: %#v", body)
		}
		writeAssistantResponse(t, w, `{"prompt":"summary: a concise video.","references":[{"id":"Picture 1","summary":"A dancer in a bright studio.","use":"Use as the base scene."},{"id":"Picture 2","summary":"The same dancer's face and costume.","use":"Preserve identity."},{"id":"Video 1","summary":"Attached motion reference.","use":"Use for motion timing."},{"id":"Audio 1","summary":"Attached voice reference.","use":"Use for voice and synchronization."}]}`)
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	references := []ImageReference{{Number: 1, Role: ImageReferenceBaseScene, MIMEType: "image/png", Image: []byte("image-1")}, {Number: 2, Role: ImageReferenceIdentity, MIMEType: "image/webp", Image: []byte("image-2")}}
	result, err := NewClient(base, "test:e4b").EnhanceVideo(context.Background(), ModeTextToVideo, ProfileMiniMaxH3, "a dancer turns", references, VideoContext{Mode: "references", DurationSeconds: 10, ImageCount: 2, AudioReference: true, VideoReference: true}, false)
	if err != nil || result != "summary: a concise video." {
		t.Fatalf("result = %q, err = %v", result, err)
	}
}

func TestEnhanceVideoUsesPromptOnlyRef2VAStructure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		system := body.Messages[0].Content
		if !strings.Contains(system, "prompt-driven Ref2VA with 0 attached image reference") ||
			!strings.Contains(system, "Reference files are optional") ||
			!strings.Contains(system, "N/A - no reference media supplied") ||
			strings.Contains(system, "<Picture 1> (attached image") || len(body.Messages[1].Images) != 0 {
			t.Fatalf("wrong prompt-only Ref2VA context: %#v", body)
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"summary: prompt-only Ref2VA."}}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewClient(base, "test:e4b").EnhanceVideo(context.Background(), ModeTextToVideo, ProfileMiniMaxH3, "a quiet city at dawn", nil, VideoContext{Mode: "references", DurationSeconds: 5}, false)
	if err != nil || result != "summary: prompt-only Ref2VA." {
		t.Fatalf("result = %q, err = %v", result, err)
	}
}

func TestEnhanceVideoGroundsExactFrameImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		system := body.Messages[0].Content
		if !strings.Contains(system, "The attached keyframes are") ||
			!strings.Contains(system, "<Picture 1> (attached image 1): the exact opening frame") ||
			!strings.Contains(system, "<Picture 2> (attached image 2): the exact final frame") ||
			!strings.Contains(system, "Do not treat either keyframe as a loose style reference") || len(body.Messages[1].Images) != 2 {
			t.Fatalf("wrong FL2VA visual context: %#v", body)
		}
		writeAssistantResponse(t, w, `{"prompt":"A grounded first-to-last-frame prompt.","references":[{"id":"Picture 1","summary":"The exact opening frame shows a dancer facing left.","use":"Use as the exact first frame."},{"id":"Picture 2","summary":"The exact final frame shows the dancer facing camera.","use":"Use as the exact final frame."}]}`)
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	references := []ImageReference{
		{Number: 1, Role: ImageReferenceBaseScene, MIMEType: "image/png", Image: []byte("first")},
		{Number: 2, Role: ImageReferenceIdentity, MIMEType: "image/png", Image: []byte("last")},
	}
	result, err := NewClient(base, "test:e4b").EnhanceVideo(context.Background(), ModeTextToVideo, ProfileMiniMaxH3, "move from the first frame to the last", references, VideoContext{Mode: "frames", DurationSeconds: 10, ImageCount: 2}, false)
	if err != nil || result != "A grounded first-to-last-frame prompt." {
		t.Fatalf("result = %q, err = %v", result, err)
	}
}

func TestEnhanceVideoUsesT2VAStructureWithoutImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		system := body.Messages[0].Content
		if !strings.Contains(system, "selected mode is T2VA") ||
			!strings.Contains(system, "There is no opening or closing picture") ||
			!strings.Contains(system, "60-second video") {
			t.Fatalf("wrong T2VA context: %s", system)
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"A complete text-to-video prompt."}}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewClient(base, "test:e4b").EnhanceVideo(context.Background(), ModeTextToVideo, ProfileMiniMaxH3, "a distant storm approaches", nil, VideoContext{Mode: "frames", DurationSeconds: 60, ImageCount: 0}, false)
	if err != nil || result != "A complete text-to-video prompt." {
		t.Fatalf("result = %q, err = %v", result, err)
	}
}

func TestEnhanceVideoThinkUsesExtendedTokenBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !body.Think || body.Options.NumPredict != DefaultVideoThinkNumPredict {
			t.Fatalf("wrong MiniMax think budget: %#v", body.Options)
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"A complete video prompt."}}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(base, "test:e4b").EnhanceVideo(context.Background(), ModeTextToVideo, ProfileMiniMaxH3, "a dancer turns", nil, VideoContext{Mode: "frames", DurationSeconds: 15, ImageCount: 2}, true); err != nil {
		t.Fatal(err)
	}
}

func TestEnhanceResultReportsUsageAndCustomPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Options.NumPredict != 1234 || body.KeepAlive != "45s" || body.Format != "json" {
			t.Fatalf("custom request policy was not applied: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(chatResponse{
			Message:            Message{Role: "assistant", Content: `{"prompt":"A measured final prompt.","references":[]}`},
			TotalDuration:      int64(3 * time.Second),
			LoadDuration:       int64(250 * time.Millisecond),
			PromptEvalCount:    81,
			PromptEvalDuration: int64(900 * time.Millisecond),
			EvalCount:          144,
			EvalDuration:       int64(1800 * time.Millisecond),
			DoneReason:         "stop",
		}); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClientWithPolicy(base, "test:e4b", ModelPolicy{
		ImageNumPredict: 1234, ImageTimeout: 75 * time.Second, KeepAlive: "45s",
	})
	result, err := client.EnhanceResult(context.Background(), ModeTextToImage, ProfilePhotographic, "portrait", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Prompt != "A measured final prompt." || result.Policy.NumPredict != 1234 || result.Policy.TimeoutMS != 75000 || result.Policy.KeepAlive != "45s" {
		t.Fatalf("unexpected result policy: %+v", result)
	}
	if result.Usage.PromptTokens != 81 || result.Usage.CompletionTokens != 144 || result.Usage.TotalDurationMS != 3000 ||
		result.Usage.LoadDurationMS != 250 || result.Usage.PromptDurationMS != 900 || result.Usage.CompletionTimeMS != 1800 || result.Usage.DoneReason != "stop" {
		t.Fatalf("unexpected usage metrics: %+v", result.Usage)
	}
}

func writeAssistantResponse(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(chatResponse{Message: Message{Role: "assistant", Content: content}}); err != nil {
		t.Fatal(err)
	}
}
