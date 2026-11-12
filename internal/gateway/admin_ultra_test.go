package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/config"
)

func ultraAdminRequest(method, path, role string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if role != "" {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: role}))
	}
	return r
}

func TestAdminUltraGuards(t *testing.T) {
	a := &App{}
	for _, tc := range []struct {
		method, path, role string
		code               int
	}{
		{"GET", adminUltraPath, "", 403}, {"GET", adminUltraPath, "user", 403}, {"POST", adminUltraPath, "admin", 405},
		{"GET", adminUltraPath + "/other/outputs/0", "admin", 404}, {"GET", adminUltraPath + "?state=other", "admin", 400},
		{"GET", adminUltraPath + "?q=a&q=b", "admin", 400}, {"GET", adminUltraPath + "?target=http://other", "admin", 400},
		{"GET", adminUltraPath + "/" + ultraTestID + "/outputs/01", "admin", 404}, {"GET", adminUltraPath + "/" + ultraTestID + "/cancel", "admin", 404},
	} {
		w := httptest.NewRecorder()
		a.handleAdminRoutes(w, ultraAdminRequest(tc.method, tc.path, tc.role))
		if w.Code != tc.code || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(tc, w.Code)
		}
	}
}

func TestAdminUltraHistoryAndMediaAreReadOnlyAndScoped(t *testing.T) {
	const key = "history-only-private-credential-0000000000000"
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("unexpected upstream authorization")
		}
		if strings.HasSuffix(r.URL.Path, "/outputs/0") {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", "8")
			w.Write([]byte("PNG-test"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ultraHistoryList{Items: []ultraHistoryJob{{ID: ultraTestID, State: "completed", PromptExcerpt: `<script>alert(1)</script>`, Model: "Krea2/model.safetensors"}}, Counts: map[string]int{"total": 1}, NextCursor: "opaque_cursor"})
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: config.Config{Harness: config.HarnessConfig{UltraHistoryURL: u, UltraHistoryKey: key}}, tpl: tpl}
	w := httptest.NewRecorder()
	a.handleAdminUltra(w, ultraAdminRequest("GET", adminUltraPath+"?q=fixture&partial=1", "admin"))
	if w.Code != 200 || strings.Contains(w.Body.String(), "<script>alert") || !strings.Contains(w.Body.String(), "&lt;script&gt;") || !strings.Contains(w.Body.String(), "opaque_cursor") || strings.Contains(w.Body.String(), key) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.handleAdminUltra(w, ultraAdminRequest("GET", adminUltraPath+"/"+ultraTestID+"/outputs/0?download=1", "admin"))
	if w.Code != 200 || w.Body.String() != "PNG-test" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(paths) != 2 || paths[0] != "/v1/history?q=fixture" || paths[1] != "/v1/history/"+ultraTestID+"/outputs/0" {
		t.Fatal(paths)
	}
}

func TestAdminUltraTemplateAndUnavailableState(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	job := ultraHistoryJob{ID: ultraTestID, State: "failed", Error: `<img src=x onerror=bad()>`, Model: strings.Repeat("long-model", 40), Request: json.RawMessage(`{"prompt":"<script>private</script>"}`)}
	if err = tpl.ExecuteTemplate(&body, "admin_ultra_snapshot", map[string]any{"Job": job}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body.String(), "<img src=x") || !strings.Contains(body.String(), "&lt;img") {
		t.Fatal(body.String())
	}
	a := &App{tpl: tpl}
	w := httptest.NewRecorder()
	a.handleAdminUltra(w, ultraAdminRequest("GET", adminUltraPath, "admin"))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "временно недоступна") || strings.Contains(w.Body.String(), "Заданий не найдено") {
		t.Fatal(w.Code, w.Body.String())
	}
}
