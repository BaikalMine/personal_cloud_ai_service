package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

func TestPublicMuxDoesNotExposeRawServiceBrowsersOrRootAPIs(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var proxied atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	app := &App{tpl: tpl, cfg: Config{ComfyUIUpstream: base, OpenWebUIUpstream: base}}
	mux := app.publicMux()
	for _, role := range []string{"", "user", "admin"} {
		for _, service := range []string{"comfyui", "openwebui"} {
			for _, path := range []string{"/" + service, "/" + service + "/", "/" + service + "/api/config", "/api/config", "/prompt", "/queue", "/ws", "/api/chat/completions", "/api/tags", "/ollama/"} {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.AddCookie(&http.Cookie{Name: serviceCookieName, Value: service})
				r.Header.Set("Referer", "https://ai.example/"+service+"/")
				if role != "" {
					r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: role, CanUseComfyUI: true, CanUseOpenWebUI: true}))
				}
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if w.Code != http.StatusNotFound {
					t.Fatalf("role=%s path=%s status=%d", role, path, w.Code)
				}
			}
			r := httptest.NewRequest(http.MethodGet, "/?gateway_service="+service, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
				t.Fatalf("legacy selector still affects the root: %d %s", w.Code, w.Header().Get("Location"))
			}
		}
	}
	if proxied.Load() != 0 {
		t.Fatalf("retired services received %d upstream requests", proxied.Load())
	}
}

func TestGenerationProgressRouteIsNarrowAndKeepsExistingAdminBoundary(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{tpl: tpl}
	for _, tc := range []struct {
		role, method, origin string
		upgrade              bool
		status               int
	}{
		{"", "GET", "", false, http.StatusFound},
		{"user", "GET", "", false, http.StatusForbidden},
		{"admin", "POST", "", false, http.StatusMethodNotAllowed},
		{"admin", "GET", "", false, http.StatusBadRequest},
		{"admin", "GET", "https://foreign.example", true, http.StatusForbidden},
		{"admin", "GET", "http://example.com", true, http.StatusServiceUnavailable},
	} {
		r := httptest.NewRequest(tc.method, "/generate/ws", nil)
		if tc.role != "" {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: tc.role, CanUseQuickGeneration: true}))
		}
		if tc.upgrade {
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		app.publicMux().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: status=%d", tc, w.Code)
		}
	}
}

func TestOpenWebUIIsNotAnActiveDependencyOrUpdateTarget(t *testing.T) {
	base, _ := url.Parse("http://retired.invalid")
	app := &App{cfg: Config{OpenWebUIUpstream: base}}
	for _, spec := range app.dependencySpecs() {
		if spec.Key == dependencyOpenWebUI {
			t.Fatal("retired OpenWebUI remains a dependency")
		}
	}
	if validUpdateRequest(updates.Request{Components: []string{updates.ComponentOpenWebUI}}) {
		t.Fatal("retired component can still be installed")
	}
	view := newAdminUpdatesView(time.Now(), updates.Status{Available: true, Components: []updates.ComponentStatus{{Name: updates.ComponentOpenWebUI, Configured: true}}}, nil, "", nil)
	if len(view.Components) != 0 {
		t.Fatal("retired component is still shown in updates")
	}
}

func TestRetiredRawServiceGrantsCannotBeIssued(t *testing.T) {
	for _, legacy := range []string{"comfyui", "openwebui"} {
		if _, err := (store.AccessListFilter{Access: legacy}).Normalize(false); err == nil {
			t.Fatal("retired service still qualifies as an active access filter")
		}
	}
	for _, invitation := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodPost, "/admin/users/1", nil)
		r.Form = url.Values{"grant_comfyui": {"on"}, "grant_openwebui": {"on"}, "can_use_comfyui": {"on"}, "can_use_openwebui": {"on"}, "max_video_generation_quality": {"720"}}
		settings, err := parseAccessSettings(r, invitation)
		if settings.ComfyUI || settings.OpenWebUI {
			t.Fatal("a retired raw UI grant was accepted")
		}
		if invitation && err == nil {
			t.Fatal("raw UI-only invitation was accepted")
		}
	}
}
