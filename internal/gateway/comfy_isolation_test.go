package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestFilterComfyHistoryJSON(t *testing.T) {
	body := []byte(`{"mine":{"status":"ok"},"other":{"status":"ok"}}`)
	filtered, err := filterComfyHistoryJSON(body, map[string]struct{}{"mine": {}})
	if err != nil {
		t.Fatal(err)
	}
	var history map[string]any
	if err := json.Unmarshal(filtered, &history); err != nil {
		t.Fatal(err)
	}
	if _, ok := history["mine"]; !ok {
		t.Fatal("owned prompt was removed")
	}
	if _, ok := history["other"]; ok {
		t.Fatal("another user's prompt remained visible")
	}
}

func TestFilterComfyQueueJSON(t *testing.T) {
	body := []byte(`{"queue_running":[[1,"mine",{}, {"client_id":"client-a"},[]]],"queue_pending":[[2,"other",{}, {"client_id":"client-b"},[]],[3,"mine-2",{}, {"client_id":"client-a"},[]]]}`)
	filtered, err := filterComfyQueueJSON(body, "client-a")
	if err != nil {
		t.Fatal(err)
	}
	if !comfyQueueContainsClient(filtered, "queue_running", "client-a") ||
		!comfyQueueContainsClient(filtered, "queue_pending", "client-a") {
		t.Fatal("owned queue entries were removed")
	}
	if comfyQueueContainsClient(filtered, "queue_pending", "client-b") {
		t.Fatal("another user's queue entry remained visible")
	}
}

func TestExtractComfyOutputOwnerships(t *testing.T) {
	body := []byte(`{"prompt-1":{"outputs":{"7":{"images":[{"filename":"image.png","subfolder":"alice","type":"output"}],"gifs":[{"filename":"clip.mp4","subfolder":"alice","type":"output","format":"video/h264-mp4"}]}}}}`)
	outputs, err := extractComfyOutputOwnerships(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 {
		t.Fatalf("outputs = %d, want 2", len(outputs))
	}
	byName := make(map[string]domain.ComfyOutputOwnership, len(outputs))
	for _, output := range outputs {
		byName[output.Filename] = output
	}
	if image := byName["image.png"]; image.PromptID != "prompt-1" || image.MediaType != "image" {
		t.Fatalf("unexpected image ownership: %+v", image)
	}
	if video := byName["clip.mp4"]; video.MediaType != "video" {
		t.Fatalf("unexpected video ownership: %+v", video)
	}
}

func TestOversizedComfyResponseIsPreserved(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1025)
	response := &http.Response{
		Body:   io.NopCloser(bytes.NewReader(payload)),
		Header: make(http.Header),
	}
	body, oversized, err := readResponseForFiltering(response, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !oversized || body != nil {
		t.Fatalf("oversized=%v body=%d", oversized, len(body))
	}
	forwarded, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(forwarded, payload) {
		t.Fatalf("response changed: got %d bytes, want %d", len(forwarded), len(payload))
	}
}
