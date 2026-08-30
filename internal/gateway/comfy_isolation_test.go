package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
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

func TestComfyIsolationFiltersRejectMalformedResponses(t *testing.T) {
	if _, err := filterComfyHistoryJSON([]byte(`{"mine":`), map[string]struct{}{}); err == nil {
		t.Fatal("malformed history response was accepted")
	}
	if _, err := filterComfyQueueJSON([]byte(`{"queue_running":{}}`), "client-a"); err == nil {
		t.Fatal("malformed queue response was accepted")
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

func TestComfyPromptAdmissionLimitsPerUserQueue(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	clientID := app.comfyClientID(7)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := `{"queue_running":[[1,"one",{}, {"client_id":"` + clientID + `"},[]]],"queue_pending":[[2,"two",{}, {"client_id":"` + clientID + `"},[]]]}`
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app.cfg.ComfyUIUpstream = upstream
	app.comfyPromptLimiter = security.NewLoginLimiter(time.Minute, 10)
	release, err := app.acquireComfyPromptAdmission(t.Context(), 7)
	if release != nil {
		release()
	}
	if !errors.Is(err, errComfyPromptAdmission) {
		t.Fatalf("admission error = %v", err)
	}
}

func TestComfyPromptAdmissionBoundsWaitingRequests(t *testing.T) {
	app := &App{
		cfg:                Config{SessionSecret: "01234567890123456789012345678901"},
		comfyPromptLimiter: security.NewLoginLimiter(time.Minute, 10),
		comfyPromptSlots:   make(chan struct{}, 1),
	}
	app.comfyPromptSlots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, err := app.acquireComfyPromptAdmission(ctx, 7); release != nil || !errors.Is(err, errComfyPromptAdmission) {
		t.Fatalf("bounded admission = release:%v err:%v", release != nil, err)
	}
}

func TestComfyInterruptIsRewrittenToAtomicOwnedJobCancellation(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	clientID := app.comfyClientID(7)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"queue_running":[[1,"abc123",{}, {"client_id":"` + clientID + `"},[]]],"queue_pending":[]}`))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app.cfg.ComfyUIUpstream = upstream
	request := httptest.NewRequest(http.MethodPost, "/interrupt", nil)
	allowed, err := app.enforceComfyControlIsolation(request, &User{ID: 7, Role: "user"}, "comfyui")
	if err != nil || !allowed {
		t.Fatalf("interrupt isolation = allowed:%v err:%v", allowed, err)
	}
	if request.URL.Path != "/api/jobs/abc123/cancel" || request.ContentLength != 0 {
		t.Fatalf("interrupt target = %q length=%d", request.URL.Path, request.ContentLength)
	}
}

func TestValidateComfyPromptWorkloadBoundsGraphAndBatch(t *testing.T) {
	safe := map[string]comfyPromptNode{
		"1": {ClassType: "EmptyLatentImage", Inputs: map[string]any{"width": float64(1024), "height": float64(1024), "batch_size": float64(2)}},
		"2": {ClassType: "KSampler", Inputs: map[string]any{"steps": float64(30)}},
	}
	if err := validateComfyPromptWorkload(safe); err != nil {
		t.Fatalf("safe graph rejected: %v", err)
	}
	safe["1"] = comfyPromptNode{ClassType: "EmptyLatentImage", Inputs: map[string]any{"width": float64(1024), "height": float64(1024), "batch_size": float64(maxComfyPromptBatch + 1)}}
	if err := validateComfyPromptWorkload(safe); err == nil {
		t.Fatal("oversized batch was accepted")
	}
	linked := map[string]comfyPromptNode{
		"1": {ClassType: "EmptyLatentImage", Inputs: map[string]any{"width": []any{"2", float64(0)}, "height": float64(1024)}},
		"2": {ClassType: "PrimitiveNode", Inputs: map[string]any{"value": float64(32768)}},
	}
	if err := validateComfyPromptWorkload(linked); err == nil {
		t.Fatal("linked resource input was accepted")
	}
	fractional := map[string]comfyPromptNode{
		"1": {ClassType: "KSampler", Inputs: map[string]any{"steps": float64(30.5)}},
	}
	if err := validateComfyPromptWorkload(fractional); err == nil {
		t.Fatal("fractional resource input was accepted")
	}
	tooMany := make(map[string]comfyPromptNode, maxComfyPromptNodes+1)
	for index := 0; index <= maxComfyPromptNodes; index++ {
		tooMany[strconv.Itoa(index)] = comfyPromptNode{ClassType: "PrimitiveNode"}
	}
	if err := validateComfyPromptWorkload(tooMany); err == nil {
		t.Fatal("oversized graph was accepted")
	}
}

func TestComfyDirectCancelRequiresPromptOwnership(t *testing.T) {
	app := &App{generationJobs: map[string]*generationJob{
		"abc123": {UserID: 7},
		"def456": {UserID: 8},
	}}
	for _, test := range []struct {
		name string
		path string
		user User
		want bool
	}{
		{name: "owned", path: "/api/jobs/abc123/cancel", user: User{ID: 7, Role: "user"}, want: true},
		{name: "foreign", path: "/api/jobs/def456/cancel", user: User{ID: 7, Role: "user"}, want: false},
		{name: "malformed", path: "/api/jobs/abc123/extra/cancel", user: User{ID: 7, Role: "user"}, want: false},
		{name: "admin", path: "/api/jobs/def456/cancel", user: User{ID: 1, Role: "admin"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			allowed, err := app.enforceComfyControlIsolation(request, &test.user, "comfyui")
			if err != nil || allowed != test.want {
				t.Fatalf("allowed=%v err=%v, want %v", allowed, err, test.want)
			}
		})
	}
}
