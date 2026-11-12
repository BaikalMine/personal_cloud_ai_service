package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/config"
)

func TestComfyGPUPermitBoundary(t *testing.T) {
	testGPUPermitBoundary(t, false)
}

func TestOllamaGPUPermitBoundary(t *testing.T) {
	testGPUPermitBoundary(t, true)
}

func testGPUPermitBoundary(t *testing.T, ollama bool) {
	key := strings.Repeat("k", 64)
	foreign := "ollama"
	if ollama {
		foreign = "comfyui"
	}
	for _, tc := range []struct {
		name, method, key, body string
		enabled, drill          bool
		status                  int
	}{
		{"disabled", "POST", key, `{}`, false, false, 404},
		{"missing key", "POST", "", `{}`, true, false, 404},
		{"wrong key", "POST", "wrong", `{}`, true, false, 404},
		{"wrong method", "GET", key, `{}`, true, false, 405},
		{"unknown field", "POST", key, `{"action":"reserve","extra":1}`, true, false, 400},
		{"foreign scope", "POST", key, `{"action":"reserve","executor":"` + foreign + `"}`, true, false, 400},
		{"trailing JSON", "POST", key, `{} {}`, true, false, 400},
		{"oversize", "POST", key, `{"action":"` + strings.Repeat("x", 4096) + `"}`, true, false, 400},
		{"no coordinator", "POST", key, `{}`, true, false, 503},
		{"restore drill", "POST", key, `{}`, true, true, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{cfg: config.Config{ComfyGPUGuardRequired: tc.enabled, ComfyGPUGuardToken: key, RestoreDrillMode: tc.drill, MiningFencedCommands: true}}
			handler := a.handleComfyGPUPermit
			if ollama {
				a.cfg.OllamaGPUGuardRequired, a.cfg.OllamaGPUGuardToken = tc.enabled, key
				handler = a.handleOllamaGPUPermit
			}
			r := httptest.NewRequest(tc.method, "/internal/gpu/comfy/permit", strings.NewReader(tc.body))
			if tc.key != "" {
				r.Header.Set("Authorization", "Bearer "+tc.key)
			}
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing cache fence")
			}
			if strings.Contains(w.Body.String(), key) {
				t.Fatal("key leaked")
			}
		})
	}
}

func TestComfyGPUGuardProbe(t *testing.T) {
	key := strings.Repeat("k", 64)
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"enforced", `{"protocol":1,"enforced":true,"instance_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, 200, true},
		{"storage extension", `{"protocol":1,"enforced":true,"instance_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","storage_protocol":1,"storage_held":true,"storage_journal_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`, 200, true},
		{"not enforced", `{"protocol":1,"enforced":false,"instance_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, 200, false},
		{"wrong version", `{"protocol":2,"enforced":true,"instance_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, 200, false},
		{"invalid instance", `{"protocol":1,"enforced":true,"instance_id":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}`, 200, false},
		{"trailing response", `{"protocol":1,"enforced":true,"instance_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {}`, 200, false},
		{"oversize response", `{"protocol":1,"enforced":true,"instance_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}` + strings.Repeat(" ", 4096), 200, false},
		{"unavailable", `{}`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/comfy/ai-gateway/gpu/status" || r.URL.RawQuery != "" || r.Header.Get(comfyGPUKeyHeader) != key || r.Header.Get("Authorization") != "Basic fixture" {
					t.Error("incorrect probe request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			u, _ := url.Parse(s.URL + "/comfy?secret=discard")
			a := &App{cfg: config.Config{ComfyUIUpstream: u, ComfyGPUGuardToken: key, ComfyUIUpstreamAuthHeader: "Basic fixture"}}
			if err := a.probeComfyGPUGuard(context.Background()); (err == nil) != tc.ok {
				t.Fatalf("probe result: %v", err)
			}
		})
	}
}

func TestStripComfyGPUHeaders(t *testing.T) {
	h := http.Header{"X-AI-Gateway-GPU-Key": {"secret"}, "x-ai-gateway-gpu-work": {"work"}, "X-Ai-Gateway-Gpu-Future": {"future"}, "X-Request-Id": {"keep"}}
	stripComfyGPUHeaders(h)
	if len(h) != 1 || h.Get("X-Request-Id") != "keep" {
		t.Fatalf("private headers survived: %v", h)
	}
}
