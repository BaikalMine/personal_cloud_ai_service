package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"ai-access-gateway/internal/config"
)

func harnessURL(t *testing.T, value string) *url.URL {
	t.Helper()
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func harnessAdmin(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: "admin"}))
}

func TestHarnessBrowserBootstrapScopeAndIsolation(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host != "public.example" || r.Header.Get("Cookie") != "dsh-auth-test=private-session" || r.Header.Get("Authorization") != "" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Forwarded-Host") != "" {
			t.Errorf("untrusted headers reached Harness")
		}
		if r.URL.Path == "/upload" {
			body, _ := io.ReadAll(r.Body)
			if string(body) != "file-data" {
				t.Error("upload corrupted")
			}
			w.WriteHeader(201)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><head></head><body>Harness</body></html>")
	}))
	defer upstream.Close()
	bootstrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" || r.URL.Query().Get("host") != "public.example" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("b", 64) {
			t.Error("invalid bootstrap")
		}
		json.NewEncoder(w).Encode(map[string]string{"cookie": "dsh-auth-test=private-session; Path=/; HttpOnly; SameSite=Strict"})
	}))
	defer bootstrap.Close()
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{tpl: tpl, cfg: Config{PublicURL: harnessURL(t, "https://public.example"), CookieSecure: true,
		Harness: config.HarnessConfig{Upstream: harnessURL(t, upstream.URL), BootstrapURL: harnessURL(t, bootstrap.URL), BootstrapKey: strings.Repeat("b", 64)}}}
	_, ingressNetwork, _ := net.ParseCIDR("10.0.0.7/32")
	app.cfg.Harness.IngressHost = "localhost:8080"
	app.cfg.Harness.IngressPeers = []*net.IPNet{ingressNetwork}
	handler := http.NewServeMux()
	app.registerHarnessRoutes(handler)
	r := harnessAdmin(httptest.NewRequest("GET", "https://public.example/harness/", nil))
	r.Header.Set("Cookie", "gateway_session=must-not-leak; other=must-not-leak")
	r.Header.Set("Authorization", "must-not-leak")
	r.Header.Set("Idempotency-Key", "must-not-leak")
	r.Header.Set("X-Forwarded-Host", "forged.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ai-gateway-return") || strings.Contains(w.Body.String(), "private-session") {
		t.Fatalf("bootstrap failed: %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Path != harnessMount || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("cookie not scoped: %+v", cookies)
	}
	r = harnessAdmin(httptest.NewRequest("GET", "https://public.example/harness/", nil))
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "document")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("external navigation rejected: %d", w.Code)
	}
	r = harnessAdmin(httptest.NewRequest("POST", "https://public.example/harness/upload", strings.NewReader("file-data")))
	r.Header.Set("Origin", "https://public.example")
	r.Header.Set("Cookie", "gateway_session=must-not-leak; dsh-auth-test=private-session")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("upload failed: %d", w.Code)
	}
	for _, method := range []string{"GET", "POST"} {
		target, expected := "http://localhost:8080/harness/", 200
		if method == "POST" {
			target, expected = "http://localhost:8080/harness/upload", 201
		}
		r = harnessAdmin(httptest.NewRequest(method, target, strings.NewReader("file-data")))
		r.RemoteAddr = "10.0.0.7:40000"
		r.Header.Set("Origin", "https://public.example")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Cookie", "dsh-auth-test=private-session")
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("trusted ingress %s failed: %d", method, w.Code)
		}
	}
	before := calls.Load()
	for _, tc := range []struct {
		target, origin, role string
		status               int
	}{
		{"https://public.example/harness/", "", "", 302},
		{"https://public.example/harness/upload", "https://evil.example", "admin", 403},
		{"https://evil.example/harness/", "", "admin", 403},
		{"https://public.example/harness/?token=external", "", "admin", 403},
		{"https://public.example/harness/", "", "user", 403},
		{"http://localhost:8080/harness/", "", "", 302},
		{"http://localhost:8080/harness/", "", "user", 403},
		{"http://localhost:8080/harness/upload", "https://evil.example", "admin", 403},
	} {
		r := httptest.NewRequest("GET", tc.target, nil)
		if r.Host == "localhost:8080" {
			r.RemoteAddr = "10.0.0.7:40000"
		}
		if tc.role != "" {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: tc.role}))
		}
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s role=%s: %d", tc.target, tc.role, w.Code)
		}
	}
	if calls.Load() != before {
		t.Error("unauthorized request forwarded")
	}
	r = harnessAdmin(httptest.NewRequest("POST", "https://public.example/harness/upload", nil))
	r.ContentLength = harnessUploadLimit + 1
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 413 || calls.Load() != before {
		t.Error("oversized upload admitted")
	}
}

func TestHarnessModelProxyAuthRoutesAndStreaming(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("c", 64) || r.Header.Get("Cookie") != "" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get(comfyGPUKeyHeader) != "" {
			t.Error("model identity not isolated")
		}
		if r.Method == "POST" {
			if r.URL.Path == "/harness/task" {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"ok":true}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: test\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[]}`)
	}))
	defer upstream.Close()
	app := &App{cfg: Config{Harness: config.HarnessConfig{Upstream: harnessURL(t, upstream.URL), ModelUpstream: harnessURL(t, upstream.URL), ModelKey: strings.Repeat("m", 64), ClientKey: strings.Repeat("c", 64)}}}
	handler := app.harnessModelProxy()
	for _, tc := range []struct {
		method, path string
		auth         bool
		status       int
	}{
		{"GET", "/v1/models", false, 404}, {"GET", "/v1/models", true, 200}, {"POST", "/v1/chat/completions", true, 200},
		{"POST", "/api/pull", true, 404}, {"POST", "/api/delete", true, 404}, {"POST", "/v1/chat/completions?x=1", true, 404},
		{"POST", "/v1/chat/%63ompletions", true, 404}, {"GET", "/v1/chat/completions", true, 404},
		{"POST", "/harness/task", true, 200}, {"POST", "/harness/task", false, 404},
		{"GET", "/harness/task", true, 404}, {"POST", "/harness/task?x=1", true, 404},
	} {
		r := httptest.NewRequest(tc.method, harnessModelMount+tc.path, strings.NewReader(`{"model":"fixture","stream":true}`))
		if tc.auth {
			r.Header.Set("Authorization", "Bearer "+app.cfg.Harness.ModelKey)
		}
		r.Header.Set("Cookie", "gateway_session=private")
		r.Header.Set("Idempotency-Key", "avoid-replay")
		r.Header.Set(comfyGPUKeyHeader, "forged")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
		if tc.method == "POST" && tc.status == 200 && tc.path != "/harness/task" && !strings.Contains(w.Body.String(), "[DONE]") {
			t.Error("stream corrupted")
		}
	}
	if calls.Load() != 3 {
		t.Errorf("unexpected model dispatches: %d", calls.Load())
	}
}
