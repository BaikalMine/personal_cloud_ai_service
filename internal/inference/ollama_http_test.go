package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
)

type ollamaRuntimeFixture struct {
	advances, httpCalls, managedCalls atomic.Int64
	advanceErr                        error
	handle                            func(context.Context, modelruntime.HTTPRequest, modelruntime.HTTPConsumer) (modelruntime.Result, error)
	stopCalls                         atomic.Int64
	stopHandle                        func(context.Context) (bool, error)
}

func (r *ollamaRuntimeFixture) Advance(context.Context) (domain.GPUInferenceExecutor, error) {
	generation := r.advances.Add(1)
	return domain.GPUInferenceExecutor{ID: strings.Repeat("a", 64), Generation: generation, Token: fmt.Sprintf("%064x", generation)}, r.advanceErr
}
func (r *ollamaRuntimeFixture) Execute(context.Context, []byte) (modelruntime.Result, error) {
	r.managedCalls.Add(1)
	return modelruntime.Result{Stopped: true}, errors.New("unexpected managed execution")
}
func (r *ollamaRuntimeFixture) ExecuteHTTP(ctx context.Context, request modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
	r.httpCalls.Add(1)
	if r.handle == nil {
		return modelruntime.Result{}, errors.New("unexpected native execution")
	}
	return r.handle(ctx, request, consume)
}
func (*ollamaRuntimeFixture) Close() error { return nil }
func (r *ollamaRuntimeFixture) StopRetained(ctx context.Context) (bool, error) {
	r.stopCalls.Add(1)
	if r.stopHandle != nil {
		return r.stopHandle(ctx)
	}
	return true, nil
}

func ollamaTestConfig(endpoint string) OllamaAPIConfig {
	return OllamaAPIConfig{ClientKey: strings.Repeat("c", 64), AdmissionKey: strings.Repeat("g", 64), AdmissionURL: endpoint + "/internal/gpu/ollama/permit"}
}

func newOllamaUnitAPI(t *testing.T, endpoint string) *OllamaAPI {
	t.Helper()
	w := &Worker{owned: &ownedExecution{runtime: &ollamaRuntimeFixture{}}, journal: &outcomeJournal{}}
	w.running.Store(true)
	api, err := NewOllamaAPI(w, ollamaTestConfig(endpoint))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Close)
	return api
}

func TestOllamaHTTPRouteAllowlist(t *testing.T) {
	for _, route := range []string{"/", "/api/version", "/api/tags", "/api/ps", "/v1/models", "/v1/models/example:latest", "/v1/models/hf.co/user/model"} {
		u, _ := url.Parse(route)
		if got := ollamaRouteMethod(u); got != http.MethodGet {
			t.Fatalf("GET %q: %q", route, got)
		}
	}
	for _, route := range []string{"/api/chat", "/api/generate", "/api/embed", "/api/embeddings", "/api/show", "/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/responses"} {
		u, _ := url.Parse(route)
		if got := ollamaRouteMethod(u); got != http.MethodPost {
			t.Fatalf("POST %q: %q", route, got)
		}
	}
	for _, route := range []string{"/api/pull", "/api/push", "/api/create", "/api/delete", "/api/copy", "/api/blobs/hash", "/api/chat?x=1", "/api/chat?", "/api/chat/", "/api//chat", "/api/%63hat", "/v1/models/a/../b", "/v1/models/", "/unknown"} {
		u, _ := url.Parse(route)
		if got := ollamaRouteMethod(u); got != "" {
			t.Fatalf("forbidden %q: %q", route, got)
		}
	}
}

func TestOllamaAPIConstruction(t *testing.T) {
	for _, endpoint := range []string{"/internal/gpu/ollama/permit", "/internal/gpu/harness/permit"} {
		config := ollamaTestConfig("http://fixture.test")
		config.AdmissionURL = "http://fixture.test" + endpoint
		if err := ValidateOllamaAPIConfig(config); err != nil {
			t.Fatalf("supported admission endpoint rejected: %v", err)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*OllamaAPIConfig)
	}{
		{"short client key", func(c *OllamaAPIConfig) { c.ClientKey = "short" }},
		{"long client key", func(c *OllamaAPIConfig) { c.ClientKey = strings.Repeat("a", 257) }},
		{"space in client key", func(c *OllamaAPIConfig) { c.ClientKey += " key" }},
		{"control in admission key", func(c *OllamaAPIConfig) { c.AdmissionKey += "\r\n" }},
		{"non ASCII key", func(c *OllamaAPIConfig) { c.ClientKey += "\u00e9" }},
		{"same keys", func(c *OllamaAPIConfig) { c.ClientKey = c.AdmissionKey }},
		{"relative URL", func(c *OllamaAPIConfig) { c.AdmissionURL = "/internal/gpu/ollama/permit" }},
		{"missing hostname", func(c *OllamaAPIConfig) { c.AdmissionURL = "http://:8080/internal/gpu/ollama/permit" }},
		{"query", func(c *OllamaAPIConfig) { c.AdmissionURL += "?secret=value" }},
		{"fragment", func(c *OllamaAPIConfig) { c.AdmissionURL += "#fragment" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := ollamaTestConfig("http://fixture.test")
			tc.change(&config)
			if err := ValidateOllamaAPIConfig(config); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	for _, worker := range []*Worker{nil, {}, {journal: &outcomeJournal{}, owned: &ownedExecution{runtime: &ownedFixture{}}}} {
		if api, err := NewOllamaAPI(worker, ollamaTestConfig("http://fixture.test")); err == nil || api != nil {
			t.Fatal("unowned/non-HTTP worker accepted")
		}
	}
	api := newOllamaUnitAPI(t, "http://fixture.test")
	if api.client.Timeout < 30*time.Second || api.client.Timeout > time.Minute {
		t.Fatal("admission response budget does not cover handler cleanup")
	}
	if !api.worker.ollamaAdmission.Load() {
		t.Fatal("guard readiness missing")
	}
	if second, err := NewOllamaAPI(api.worker, api.config); err == nil || second != nil {
		t.Fatal("duplicate listener owner accepted")
	}
	api.Close()
	if api.worker.ollamaAdmission.Load() {
		t.Fatal("closed API remains ready")
	}
}

func TestOllamaAdmissionWaitsForCleanupAcknowledgement(t *testing.T) {
	if testing.Short() {
		t.Skip("real admission response deadline regression")
	}
	permit := domain.GPUExecutorPermitRequest{ID: strings.Repeat("a", 32)}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		timer := time.NewTimer(10500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"protocol":1,"id":%q,"state":"reserved","started":false}`, permit.ID)
	}))
	defer server.Close()
	api := newOllamaUnitAPI(t, server.URL)
	if err := api.reserve(context.Background(), permit); err != nil {
		t.Fatalf("slow but confirmed admission rejected: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("reservation replayed")
	}
}

func TestOllamaHTTPBoundaryBeforeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, method, route, body string
		status                    int
		change                    func(*http.Request, *OllamaAPI)
	}{
		{"missing key", "POST", "/api/chat", `{}`, 401, func(r *http.Request, _ *OllamaAPI) { r.Header.Del("Authorization") }},
		{"duplicate key", "POST", "/api/chat", `{}`, 401, func(r *http.Request, a *OllamaAPI) { r.Header.Add("Authorization", "Bearer "+a.config.ClientKey) }},
		{"internal key", "POST", "/api/chat", `{}`, 401, func(r *http.Request, a *OllamaAPI) { r.Header.Set("Authorization", "Bearer "+a.config.AdmissionKey) }},
		{"download", "POST", "/api/pull", `{}`, 404, nil},
		{"wrong method", "DELETE", "/api/chat", `{}`, 405, nil},
		{"invalid JSON", "POST", "/api/chat", `{`, 400, nil},
		{"JSON array", "POST", "/api/chat", `[]`, 400, nil},
		{"JSON null", "POST", "/api/chat", `null`, 400, nil},
		{"trailing JSON", "POST", "/api/chat", `{} {}`, 400, nil},
		{"GET body", "GET", "/api/tags", `{}`, 400, nil},
		{"compressed body", "POST", "/api/chat", `{}`, 415, func(r *http.Request, _ *OllamaAPI) { r.Header.Set("Content-Encoding", "gzip") }},
		{"oversize", "POST", "/api/chat", `{}`, 413, func(r *http.Request, _ *OllamaAPI) { r.ContentLength = domain.MaxGPUInferenceRequestBytes + 1 }},
		{"stopped worker", "POST", "/api/chat", `{}`, 503, func(_ *http.Request, a *OllamaAPI) { a.worker.running.Store(false) }},
		{"closed API", "POST", "/api/chat", `{}`, 503, func(_ *http.Request, a *OllamaAPI) { a.Close() }},
		{"busy worker", "POST", "/v1/chat/completions", `{}`, 409, func(_ *http.Request, a *OllamaAPI) { a.worker.runMu.Lock() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newOllamaUnitAPI(t, "http://unused.test")
			r := httptest.NewRequest(tc.method, tc.route, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+api.config.ClientKey)
			if tc.change != nil {
				tc.change(r, api)
			}
			if tc.name == "busy worker" {
				defer api.worker.runMu.Unlock()
			}
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), api.config.ClientKey) || strings.Contains(w.Body.String(), api.config.AdmissionKey) {
				t.Fatal("response boundary")
			}
			if tc.status == 409 && w.Header().Get("Retry-After") == "" {
				t.Fatal("missing retry hint")
			}
			if api.runtime.(*ollamaRuntimeFixture).httpCalls.Load() != 0 {
				t.Fatal("invalid request started runtime")
			}
		})
	}
}

func TestOllamaAdmissionAcknowledgement(t *testing.T) {
	permit := domain.GPUExecutorPermitRequest{Executor: domain.GPUExecutorOllama, ID: strings.Repeat("a", 32), InstanceID: strings.Repeat("b", 32), Token: strings.Repeat("c", 64), OwnerID: strings.Repeat("d", 64), OwnerToken: strings.Repeat("e", 64), OwnerGeneration: 1}
	valid := `{"protocol":1,"id":"` + permit.ID + `","state":"reserved","started":false}`
	for _, tc := range []struct {
		name, body string
		status     int
		ok, busy   bool
	}{
		{"reserved", valid, 200, true, false},
		{"busy", `{"protocol":1,"error":"executor_permit_unavailable"}`, 409, false, true},
		{"invalid busy", `{}`, 409, false, false},
		{"wrong ID", strings.ReplaceAll(valid, permit.ID, strings.Repeat("f", 32)), 200, false, false},
		{"already running", strings.ReplaceAll(valid, "reserved", "running"), 200, false, false},
		{"closed", strings.ReplaceAll(valid, "reserved", "closed"), 200, false, false},
		{"start was consumed", strings.ReplaceAll(valid, "false", "true"), 200, false, false},
		{"trailing data", valid + `{}`, 200, false, false},
		{"oversize", valid + strings.Repeat(" ", 4096), 200, false, false},
		{"redirect", valid, 307, false, false},
		{"server failure", valid, 503, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/internal/gpu/ollama/permit" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("g", 64) {
					t.Error("admission credentials or route")
				}
				var input struct {
					Action string `json:"action"`
					domain.GPUExecutorPermitRequest
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil || input.Action != "reserve" || input.GPUExecutorPermitRequest != permit {
					t.Error("admission identity changed")
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			api := newOllamaUnitAPI(t, server.URL)
			err := api.reserve(context.Background(), permit)
			if (err == nil) != tc.ok || errors.Is(err, errOllamaAdmissionBusy) != tc.busy || calls.Load() != 1 {
				t.Fatalf("reserve=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestOwnedOllamaReadinessProof(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		ok          bool
	}{
		{"owned guard", `,"owned":true,"ollama_admission":true`, true},
		{"direct worker", `,"owned":false,"ollama_admission":true`, false},
		{"no listener", `,"owned":true,"ollama_admission":false`, false},
		{"old worker", ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					t.Error("wrong health path")
				}
				_, _ = fmt.Fprintf(w, `{"service":"inference-worker","protocol":2,"ready":true%s}`, tc.extra)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			if err := ProbeReadiness(context.Background(), base); err != nil {
				t.Fatal("legacy readiness changed", err)
			}
			if err := ProbeOwnedOllamaReadiness(context.Background(), base); (err == nil) != tc.ok {
				t.Fatalf("guard proof: %v", err)
			}
		})
	}
}
