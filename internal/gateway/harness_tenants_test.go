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

func TestHarnessOnlyInvitationFormAndNavigation(t *testing.T) {
	for _, invitation := range []bool{true, false} {
		request := httptest.NewRequest("POST", "/", nil)
		request.Form = url.Values{"max_video_generation_quality": {"720"}}
		name := "can_use_harness"
		if invitation {
			name = "grant_harness"
		}
		request.Form.Set(name, "on")
		settings, err := parseAccessSettings(request, invitation)
		if err != nil || !settings.Harness || settings.QuickGeneration || settings.Training || settings.PauseMining || settings.QueuePriority {
			t.Fatalf("settings=%+v err=%v", settings, err)
		}
	}
	nav := workspaceNavigation(&User{Role: "user", CanUseHarness: true}, false, false, "", "")
	if nav.Home != "/harness/" || len(nav.Mobile) != 1 || nav.Mobile[0].Href != "/harness/" {
		t.Fatalf("navigation=%+v", nav)
	}
}

func TestHarnessTenantBrowserUsesTrustedUserAndNoNativeCookie(t *testing.T) {
	key := strings.Repeat("t", 64)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Harness-User") != "7" || r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("Cookie") != "" || r.URL.Path != "/api/session/list" {
			t.Errorf("untrusted tenant routing")
		}
		w.Header().Set("Set-Cookie", "dsh-auth-other=leak")
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	app := &App{cfg: Config{PublicURL: harnessURL(t, "https://public.example"), Harness: config.HarnessConfig{TenantURL: harnessURL(t, upstream.URL), TenantKey: key}}}
	request := httptest.NewRequest("POST", "https://public.example/harness/api/session/list", nil)
	request.Header.Set("Origin", "https://public.example")
	request.Header.Set("Cookie", "dsh-auth-admin=private")
	request.Header.Set("X-Harness-User", "1")
	recorder := httptest.NewRecorder()
	app.harnessTenantProxy(7).ServeHTTP(recorder, request)
	if recorder.Code != 200 || recorder.Header().Get("Set-Cookie") != "" {
		t.Fatalf("proxy status=%d", recorder.Code)
	}
}

func TestHarnessBrowserPermissionIsNotAdminOnlyOrPublic(t *testing.T) {
	app := &App{cfg: Config{PublicURL: harnessURL(t, "https://public.example")}}
	mux := http.NewServeMux()
	app.registerHarnessRoutes(mux)
	for _, grant := range []bool{false, true} {
		request := httptest.NewRequest("GET", "https://public.example/harness/", nil)
		request = request.WithContext(context.WithValue(request.Context(), userCtxKey, &User{ID: 7, Role: "user", CanUseHarness: grant}))
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		want := 403
		if grant {
			want = 503
		}
		if recorder.Code != want {
			t.Fatalf("grant=%v status=%d", grant, recorder.Code)
		}
	}
	request := httptest.NewRequest("GET", "https://public.example/internal/harness-users/7/access", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != 404 {
		t.Fatalf("internal access exposed: %d", recorder.Code)
	}
}
