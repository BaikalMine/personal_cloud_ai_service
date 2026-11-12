package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthPageRendering(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{tpl: tpl}
	for _, next := range []string{"/generate?draft=kept", "//evil.invalid/path", "https://evil.invalid/path"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/login", nil)
		a.renderAuthPage(w, r, 503, "login", AuthPageView{Login: `<script>alert(1)</script>`, Next: next, Error: "Unavailable"})
		body := w.Body.String()
		if w.Code != 503 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "form-action 'self'") {
			t.Fatal(w.Code, w.Header())
		}
		if strings.Contains(body, `<script>alert(1)</script>`) || strings.Contains(body, "evil.invalid") {
			t.Fatal("unescaped identity or unsafe next", body)
		}
		if strings.HasPrefix(next, "/generate") && !strings.Contains(body, `/generate?draft=kept`) {
			t.Fatal("next lost")
		}
		if !strings.Contains(body, `name="password"`) || strings.Contains(body, `name="password" value=`) {
			t.Fatal("unexpected password field", body)
		}
	}
}

func TestAuthPageEarlyRejections(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{tpl: tpl}
	for _, route := range []struct {
		path    string
		handler http.HandlerFunc
	}{{"/login", a.handleLogin}, {"/invite/test-token", a.handleInvite}} {
		for _, tc := range []struct {
			method, origin, body string
			status               int
		}{
			{"DELETE", "", "", 405}, {"POST", "https://foreign.invalid", "password=must-not-echo", 403},
			{"POST", "", "username=%zz&password=must-not-echo", 400},
			{"POST", "", "username=" + strings.Repeat("x", (1<<20)+1), 400},
		} {
			r := httptest.NewRequest(tc.method, route.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			route.handler(w, r)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "must-not-echo") {
				t.Fatalf("%s %s: %d %s", tc.method, route.path, w.Code, w.Body.String())
			}
			if tc.status == 405 && w.Header().Get("Allow") != "GET, HEAD, POST" {
				t.Fatal("missing Allow header")
			}
		}
	}
}

func TestRecoveryPageNegotiationAndRetry(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{tpl: tpl}
	for _, tc := range []struct {
		name, method, accept, dest string
		ws, html                   bool
	}{
		{"browser", "GET", "text/html,application/xhtml+xml", "", false, true},
		{"document", "GET", "*/*", "document", false, true},
		{"API", "GET", "application/json", "", false, false},
		{"plain", "GET", "", "", false, false},
		{"websocket", "GET", "text/html", "", true, false},
		{"uncertain POST", "POST", "text/html", "document", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/comfyui/custom?draft=kept", nil)
			r.Header.Set("Accept", tc.accept)
			r.Header.Set("Sec-Fetch-Dest", tc.dest)
			if tc.ws {
				r.Header.Set("Connection", "Upgrade")
				r.Header.Set("Upgrade", "websocket")
			}
			w := httptest.NewRecorder()
			a.upstreamUnavailablePage(w, r, "comfyui")
			body := w.Body.String()
			if w.Code != 502 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Code, w.Header())
			}
			if strings.Contains(w.Header().Get("Content-Type"), "text/html") != tc.html {
				t.Fatal("incorrect negotiation", w.Header())
			}
			if !tc.html && strings.Contains(body, "<!DOCTYPE") {
				t.Fatal("HTML sent to non-document client")
			}
			if tc.html && tc.method == "GET" && !strings.Contains(body, `href="/comfyui/custom?draft=kept"`) {
				t.Fatal("safe retry URL lost", body)
			}
			if tc.html && tc.method == "POST" && (!strings.Contains(body, `href="/comfyui"`) || strings.Contains(body, "custom?draft") || !strings.Contains(body, "мог быть обработан")) {
				t.Fatal("unsafe POST retry", body)
			}
		})
	}
	for _, handler := range []http.HandlerFunc{a.notFoundPage, func(w http.ResponseWriter, r *http.Request) { a.forbiddenPage(w, r, "No permission") }} {
		r := httptest.NewRequest("GET", "/secret-path-token", nil)
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		handler(w, r)
		if (w.Code != 403 && w.Code != 404) || !strings.Contains(w.Body.String(), `href="/login"`) || strings.Contains(w.Body.String(), "secret-path-token") {
			t.Fatal("unsafe or missing recovery", w.Code, w.Body.String())
		}
	}
}
