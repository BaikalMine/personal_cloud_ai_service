package inference

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
)

const openWebUIFixtureName = "AI-Gateway-Compatibility-Fixture"
const openWebUIFixtureModel = "gateway-fixture:latest"

type openWebUIFixtureClient struct {
	t           *testing.T
	base, token string
	client      *http.Client
}

func (c *openWebUIFixtureClient) request(ctx context.Context, method, path string, input any) *http.Response {
	c.t.Helper()
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			c.t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.client.Do(r)
	if err != nil {
		c.t.Fatal(method, path, err)
	}
	return response
}

func (c *openWebUIFixtureClient) json(ctx context.Context, method, path string, input, output any) {
	c.t.Helper()
	response := c.request(ctx, method, path, input)
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != 200 {
		c.t.Fatalf("OpenWebUI %s %s: status=%d read=%v", method, path, response.StatusCode, err)
	}
	if output != nil && json.Unmarshal(body, output) != nil {
		c.t.Fatalf("OpenWebUI %s %s: invalid JSON", method, path)
	}
}

// Run only against the disposable, network-isolated image fixture from the
// compatibility script. Validate its public name before any account/config write.
func TestOllamaOpenWebUICompatibility(t *testing.T) {
	base := os.Getenv("OPENWEBUI_COMPAT_TEST_URL")
	if base == "" {
		t.Skip("OPENWEBUI_COMPAT_TEST_URL is not set")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("compatibility fixture must be an explicit loopback URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := &openWebUIFixtureClient{t: t, base: base, client: &http.Client{Timeout: 15 * time.Second}}
	for {
		r, _ := http.NewRequestWithContext(ctx, "GET", base+"/health", nil)
		response, err := c.client.Do(r)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("OpenWebUI fixture did not become ready")
		case <-time.After(200 * time.Millisecond):
		}
	}
	var config struct {
		Name, Version string
		Onboarding    bool
	}
	c.json(ctx, "GET", "/api/config", nil, &config)
	if !strings.HasPrefix(config.Name, openWebUIFixtureName) || config.Version != os.Getenv("OPENWEBUI_COMPAT_TEST_VERSION") {
		t.Fatal("refusing writes to an unrecognized OpenWebUI instance")
	}
	identity := map[string]string{"name": "Compatibility Fixture", "email": "compatibility-fixture@example.com", "password": "synthetic-compatibility-password-only-12345"}
	var session struct{ Token, Role string }
	if config.Onboarding {
		c.json(ctx, "POST", "/api/v1/auths/signup", identity, &session)
	} else {
		c.json(ctx, "POST", "/api/v1/auths/signin", identity, &session)
	}
	if session.Token == "" || session.Role != "admin" {
		t.Fatal("disposable fixture administrator was not created")
	}
	c.token = session.Token
	x := newOllamaHTTPFixture(t)
	x.completed = make(chan struct{}, 128)
	t.Cleanup(x.server.CloseClientConnections)
	blocked, canceled := make(chan struct{}), make(chan struct{})
	var blockOnce, cancelOnce sync.Once
	x.runtime.handle = func(ctx context.Context, request modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
		var payload map[string]any
		_ = json.Unmarshal(request.Body, &payload)
		var response any
		contentType := "application/json"
		var raw string
		hold := false
		switch request.Path {
		case "/api/version":
			response = map[string]any{"version": "0.99.0-fixture"}
		case "/api/tags":
			response = map[string]any{"models": []any{map[string]any{"name": openWebUIFixtureModel, "model": openWebUIFixtureModel, "modified_at": "2026-09-06T00:00:00Z", "size": 1, "digest": strings.Repeat("a", 64), "details": map[string]any{"format": "gguf", "family": "fixture", "families": []string{"fixture"}, "parameter_size": "1B", "quantization_level": "F16"}}}}
		case "/api/ps":
			response = map[string]any{"models": []any{}}
		case "/api/show":
			response = map[string]any{"details": map[string]any{"format": "gguf", "family": "fixture"}, "model_info": map[string]any{"general.architecture": "fixture", "fixture.context_length": 4096}, "capabilities": []string{"completion", "embedding", "vision"}}
		case "/api/embed":
			response = map[string]any{"model": openWebUIFixtureModel, "embeddings": [][]float64{{0.1, 0.2, 0.3}}, "total_duration": 1, "prompt_eval_count": 1}
		case "/api/embeddings":
			response = map[string]any{"embedding": []float64{0.1, 0.2, 0.3}}
		case "/v1/models":
			response = map[string]any{"object": "list", "data": []any{map[string]any{"id": openWebUIFixtureModel, "object": "model", "created": 1, "owned_by": "fixture"}}}
		case "/v1/embeddings":
			response = map[string]any{"object": "list", "model": openWebUIFixtureModel, "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}}}, "usage": map[string]int{"prompt_tokens": 1, "total_tokens": 1}}
		case "/api/chat", "/api/generate":
			hold = bytes.Contains(request.Body, []byte("compat-cancel"))
			response = map[string]any{"model": openWebUIFixtureModel, "created_at": "2026-09-06T00:00:00Z", "done": !hold, "done_reason": "stop", "message": map[string]string{"role": "assistant", "content": "compatibility response"}, "response": "compatibility response"}
			if stream, _ := payload["stream"].(bool); stream {
				contentType = "application/x-ndjson"
				encoded, _ := json.Marshal(response)
				raw = string(encoded) + "\n"
			}
		case "/v1/chat/completions":
			contentType = "text/event-stream"
			raw = "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"" + openWebUIFixtureModel + "\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"compatibility response\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"
		case "/v1/responses":
			response = map[string]any{"id": "fixture", "object": "response", "status": "completed", "model": openWebUIFixtureModel, "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "compatibility response"}}}}}
		default:
			t.Errorf("unexpected OpenWebUI upstream route: %s", request.Path)
			return modelruntime.Result{Stopped: true}, nil
		}
		if raw == "" {
			encoded, _ := json.Marshal(response)
			raw = string(encoded)
		}
		err := consume(200, http.Header{"Content-Type": {contentType}}, strings.NewReader(raw))
		if hold {
			blockOnce.Do(func() { close(blocked) })
			<-ctx.Done()
			cancelOnce.Do(func() { close(canceled) })
			err = ctx.Err()
		}
		return modelruntime.Result{Status: 200, Dispatched: true, Stopped: true}, err
	}
	c.json(ctx, "POST", "/ollama/verify", map[string]string{"url": x.server.URL, "key": x.api.config.ClientKey}, nil)
	c.json(ctx, "POST", "/ollama/config/update", map[string]any{"ENABLE_OLLAMA_API": true, "OLLAMA_BASE_URLS": []string{x.server.URL}, "OLLAMA_API_CONFIGS": map[string]any{"0": map[string]any{"enable": true, "key": x.api.config.ClientKey}}}, nil)
	assertCatalog := func() {
		t.Helper()
		var catalog struct{ Models []struct{ Model string } }
		c.json(ctx, "GET", "/ollama/api/tags", nil, &catalog)
		if len(catalog.Models) != 1 || catalog.Models[0].Model != openWebUIFixtureModel {
			t.Fatalf("known model disappeared: %+v", catalog.Models)
		}
		var models struct{ Data []struct{ ID string } }
		c.json(ctx, "GET", "/api/models", nil, &models)
		found := false
		for _, model := range models.Data {
			if model.ID == openWebUIFixtureModel {
				found = true
			}
		}
		if !found {
			t.Fatalf("chat model picker lost catalog: %+v", models.Data)
		}
	}
	assertCatalog()
	for _, path := range []string{"/api/models", "/ollama/api/version", "/ollama/api/ps", "/ollama/v1/models"} {
		c.json(ctx, "GET", path, nil, nil)
	}
	c.json(ctx, "POST", "/ollama/api/show", map[string]any{"model": openWebUIFixtureModel}, nil)
	for _, path := range []string{"/ollama/api/embed", "/ollama/api/embeddings", "/ollama/v1/embeddings"} {
		c.json(ctx, "POST", path, map[string]any{"model": openWebUIFixtureModel, "input": "fixture", "prompt": "fixture"}, nil)
	}
	for _, path := range []string{"/ollama/api/chat", "/api/chat/completions"} {
		var reply map[string]any
		c.json(ctx, "POST", path, map[string]any{"model": openWebUIFixtureModel, "messages": []any{map[string]string{"role": "user", "content": "fixture"}}, "stream": false}, &reply)
		encoded, _ := json.Marshal(reply)
		if !bytes.Contains(encoded, []byte("compatibility response")) {
			t.Fatal("chat response was lost", path)
		}
	}
	c.json(ctx, "POST", "/ollama/v1/responses", map[string]any{"model": openWebUIFixtureModel, "input": "fixture", "stream": false}, nil)
	for _, path := range []string{"/ollama/api/generate", "/api/chat/completions", "/ollama/v1/chat/completions"} {
		response := c.request(ctx, "POST", path, map[string]any{"model": openWebUIFixtureModel, "prompt": "fixture", "messages": []any{map[string]string{"role": "user", "content": "fixture"}}, "stream": true})
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !bytes.Contains(body, []byte("compatibility response")) {
			t.Fatal("stream response lost", path, response.StatusCode, err)
		}
	}
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	stream := c.request(streamCtx, "POST", "/api/chat/completions", map[string]any{"model": openWebUIFixtureModel, "messages": []any{map[string]string{"role": "user", "content": "compat-cancel"}}, "stream": true})
	defer stream.Body.Close()
	if stream.StatusCode != 200 {
		t.Fatal("OpenWebUI stream did not start", stream.StatusCode)
	}
	if line, err := bufio.NewReader(stream.Body).ReadString('\n'); err != nil || !strings.Contains(line, "compatibility response") {
		t.Fatalf("OpenWebUI lost first frame: %q %v", line, err)
	}
	await(t, blocked)
	x.assertRunning(t)
	time.Sleep(1200 * time.Millisecond)
	assertCatalog()
	busy := c.request(ctx, "POST", "/ollama/api/generate", map[string]any{"model": openWebUIFixtureModel, "prompt": "fixture", "stream": false})
	_, _ = io.Copy(io.Discard, busy.Body)
	busy.Body.Close()
	if busy.StatusCode != 409 {
		t.Fatalf("OpenWebUI did not preserve GPU busy: %d", busy.StatusCode)
	}
	cancelStream()
	await(t, canceled)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := x.store.ActiveGPUExecutorPermit(ctx, domain.GPUPrimaryResource)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatal("OpenWebUI cancellation retained GPU permit", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("OpenWebUI %s provider verification, catalog, chat, embeddings, busy and cancel passed", config.Version)
}
