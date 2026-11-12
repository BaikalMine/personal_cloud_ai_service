package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/security"
)

func TestVideoPreviewPolicyAndAccess(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{tpl: tpl, csrfSigner: security.NewCSRFSigner("preview-test")}
	w := httptest.NewRecorder()
	a.renderAuthPage(w, httptest.NewRequest("GET", "/login", nil), 200, "login", AuthPageView{})
	policy := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"media-src 'self' blob:;", "object-src 'none';", "script-src 'self';"} {
		if !strings.Contains(policy, want) {
			t.Fatal("unexpected CSP", policy)
		}
	}
	for _, tc := range []struct {
		method, token string
		status        int
	}{
		{"GET", "", 405}, {"POST", "bad", 403}, {"POST", a.csrfSigner.Token("preview-session"), 404},
	} {
		form := url.Values{"csrf": {tc.token}, "input_video": {"foreign/reference.mp4"}}
		r := httptest.NewRequest(tc.method, "/generate/preview/video", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "preview-session"})
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1}))
		w := httptest.NewRecorder()
		a.handleGenerationVideoPreview(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d want %d", tc.method, w.Code, tc.status)
		}
	}
}

func TestAIHubManualDurationStillLimited(t *testing.T) {
	in := aiHubTestInput(t)
	in.VideoDurationSeconds = 30
	if normalizeAIHub(&in) == nil {
		t.Fatal("manual duration expanded unintentionally")
	}
	in.AIHubMatchDuration, in.AIHubOutputFrames = true, 720
	if err := normalizeAIHub(&in); err != nil {
		t.Fatal(err)
	}
}
