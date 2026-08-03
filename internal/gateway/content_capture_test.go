package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestContentPersistenceContextSurvivesRequestCancellation(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	persistContext, cancelPersist := contentPersistenceContext(requestContext)
	defer cancelPersist()
	select {
	case <-persistContext.Done():
		t.Fatalf("persistence context was canceled with request context: %v", persistContext.Err())
	default:
	}
}

func TestParseOpenWebCaptureSSE(t *testing.T) {
	request := []byte(`{"model":"qwen3","chat_id":"chat-1","messages":[{"role":"user","content":"Привет"}]}`)
	response := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Здравствуйте\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"!\"}}]}\n\n" +
		"data: [DONE]\n")
	externalID, model, prompt, answer, _, err := parseOpenWebCapture(request, response)
	if err != nil {
		t.Fatal(err)
	}
	if externalID != "chat-1" || model != "qwen3" || prompt != "Привет" || answer != "Здравствуйте!" {
		t.Fatalf("unexpected capture: id=%q model=%q prompt=%q answer=%q", externalID, model, prompt, answer)
	}
}

func TestBeginContentCaptureKeepsStreamingOpenWebUIRequests(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat/completions", strings.NewReader(`{"model":"qwen3","chat_id":"chat-1","stream":true,"messages":[{"role":"user","content":"Привет"}]}`))
	capture, err := (&App{}).beginContentCapture(req, &User{ID: 7}, "openwebui")
	if err != nil {
		t.Fatal(err)
	}
	if capture == nil {
		t.Fatal("streaming OpenWebUI request must be captured directly")
	}
}

func TestParseOpenWebChatSave(t *testing.T) {
	request := []byte(`{"chat":{"id":"chat-1","history":{"currentId":"assistant-1","messages":{"user-1":{"id":"user-1","role":"user","content":{"text":"hello"},"timestamp":1},"assistant-1":{"id":"assistant-1","role":"assistant","parentId":"user-1","content":"world","model":"qwen3","timestamp":2}}}}}`)
	externalID, model, prompt, answer, metadata, err := parseOpenWebChatSave(request, "/api/v1/chats/chat-1")
	if err != nil {
		t.Fatal(err)
	}
	if externalID != "chat-1:assistant-1" || model != "qwen3" || prompt != "hello" || answer != "world" {
		t.Fatalf("unexpected chat save capture: id=%q model=%q prompt=%q answer=%q", externalID, model, prompt, answer)
	}
	if !strings.Contains(metadata, "openwebui_chat_save") {
		t.Fatalf("missing source metadata: %s", metadata)
	}
}

func TestIsOpenWebChatSavePath(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/v1/chats/chat-1":              true,
		"/api/v1/chats/new":                 false,
		"/api/v1/chats/all/tags":            false,
		"/api/v1/chats/chat-1/messages/one": false,
	} {
		if got := isOpenWebChatSavePath(path); got != want {
			t.Fatalf("isOpenWebChatSavePath(%q) = %t, want %t", path, got, want)
		}
	}
}

func TestParseComfyCapture(t *testing.T) {
	request := []byte(`{"prompt":{"1":{"inputs":{"text_g":"космический корабль","seed":123}},"2":{"inputs":{"ckpt_name":"flux.safetensors"}}}}`)
	response := []byte(`{"prompt_id":"prompt-42"}`)
	externalID, model, prompt, _, metadata, err := parseComfyCapture(request, response)
	if err != nil {
		t.Fatal(err)
	}
	if externalID != "prompt-42" || model != "flux.safetensors" || prompt != "космический корабль" {
		t.Fatalf("unexpected capture: id=%q model=%q prompt=%q", externalID, model, prompt)
	}
	if !strings.Contains(metadata, "123") {
		t.Fatalf("seed missing from metadata: %s", metadata)
	}
}

func TestParseComfyCaptureWithoutTextUsesWorkflowSummary(t *testing.T) {
	request := []byte(`{"prompt":{"1":{"class_type":"LoadImage","inputs":{"image":"gateway/input.png"}},"2":{"class_type":"SaveImage","inputs":{"images":["1",0]}}}}`)
	response := []byte(`{"prompt_id":"prompt-no-text"}`)
	_, _, prompt, _, metadata, err := parseComfyCapture(request, response)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "[workflow] LoadImage, SaveImage" {
		t.Fatalf("workflow summary = %q", prompt)
	}
	if !strings.Contains(metadata, "LoadImage") || !strings.Contains(metadata, "SaveImage") {
		t.Fatalf("workflow node metadata missing: %s", metadata)
	}
}

func TestLimitedBufferExactLimitIsNotTruncated(t *testing.T) {
	buffer := newLimitedBuffer(4)
	_, _ = buffer.Write([]byte("1234"))
	if buffer.truncated || string(buffer.data) != "1234" {
		t.Fatalf("exact-limit write marked truncated: %#v", buffer)
	}
	_, _ = buffer.Write([]byte("5"))
	if !buffer.truncated {
		t.Fatal("overflow write was not marked truncated")
	}
}

func TestMediaCaptureSlotsAreReleased(t *testing.T) {
	app := &App{mediaCaptureSlots: make(chan struct{}, 1)}
	user := &User{ID: 7}
	request := func() *http.Request {
		return &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/view"}}
	}
	first, err := app.beginContentCapture(request(), user, "comfyui")
	if err != nil || first == nil {
		t.Fatalf("first capture: capture=%v err=%v", first, err)
	}
	second, err := app.beginContentCapture(request(), user, "comfyui")
	if err != nil || second != nil {
		t.Fatalf("second capture must be skipped: capture=%v err=%v", second, err)
	}
	first.Release()
	first.Release()
	third, err := app.beginContentCapture(request(), user, "comfyui")
	if err != nil || third == nil {
		t.Fatalf("slot was not released: capture=%v err=%v", third, err)
	}
	third.Release()
}

func TestOversizedAuditBodyStillReachesUpstreamIntact(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), maxCapturedContent+1024)
	request := &http.Request{
		Method:        http.MethodPost,
		URL:           &url.URL{Path: "/prompt"},
		Body:          io.NopCloser(bytes.NewReader(payload)),
		ContentLength: int64(len(payload)),
	}
	app := &App{}
	capture, err := app.beginContentCapture(request, &User{ID: 9}, "comfyui")
	if err != nil || capture != nil {
		t.Fatalf("oversized body must only skip audit: capture=%v err=%v", capture, err)
	}
	forwarded, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(forwarded, payload) {
		t.Fatalf("upstream body changed: got %d bytes, want %d", len(forwarded), len(payload))
	}
}
