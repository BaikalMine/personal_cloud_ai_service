package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/security"
)

func TestModelUpdateAdminTokenAndCSRF(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/api/lm/loras/updates/refresh":
			json.NewEncoder(w).Encode(map[string]any{"success": true})
		case "/api/lm/loras/updates/status/42":
			json.NewEncoder(w).Encode(map[string]any{"success": true, "record": map[string]any{"modelId": 42, "hasUpdate": true, "lastCheckedAt": time.Now().Unix()}})
		default:
			t.Error("unexpected upstream path")
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	cipher, _ := contentcrypto.NewCipher(strings.Repeat("s", 32))
	app := &App{cfg: Config{ComfyUIUpstream: base}, contentCipher: cipher, csrfSigner: security.NewCSRFSigner("test-updates")}
	token, _ := app.modelToken(modelLibraryToken{Scope: "item", Owner: 1, Expires: time.Now().Add(time.Hour).Unix(), Kind: "loras", ModelID: 42})
	for _, tc := range []struct {
		name, role string
		id         int64
		csrf       bool
		want       int
	}{
		{"read-only user", "user", 1, true, 403},
		{"missing csrf", "admin", 1, false, 403},
		{"other owner", "admin", 2, true, 400},
		{"admin", "admin", 1, true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"token": token})
			req := httptest.NewRequest("POST", "/models/api/check-update", strings.NewReader(string(body)))
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &User{ID: tc.id, Role: tc.role}))
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "test-session"})
			if tc.csrf {
				req.Header.Set("X-CSRF-Token", app.csrfSigner.Token("test-session"))
			}
			before := calls
			w := httptest.NewRecorder()
			app.handleModelLibraryAPI(w, req)
			if w.Code != tc.want {
				t.Fatalf("status %d expected %d", w.Code, tc.want)
			}
			if tc.want != 200 && calls != before {
				t.Fatal("unauthorized request reached upstream")
			}
			if tc.want == 200 && !strings.Contains(w.Body.String(), `"update":true`) {
				t.Fatal("result missing")
			}
		})
	}
}
