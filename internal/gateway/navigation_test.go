package gateway

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestShellPreferencesAreRoutedOnAdminListener(t *testing.T) {
	app := &App{}
	for _, path := range []string{"/account/quick-generation-priority", "/account/generation-mining"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: "admin"}))
		w := httptest.NewRecorder()
		app.adminMux().ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
			t.Fatalf("%s: not routed to POST handler: %d %s", path, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		app.adminMux().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login?") {
			t.Fatalf("%s: anonymous request did not reach authentication", path)
		}
	}
}

func TestAccountPagePreservesListenerWorkspace(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{tpl: templates, cfg: Config{PublicBaseURL: "https://public.example"}}
	for _, admin := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodGet, "/account/password", nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: "admin", Username: "admin"}))
		w := httptest.NewRecorder()
		if admin {
			app.adminMux().ServeHTTP(w, r)
		} else {
			app.handleAccountPassword(w, r)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("admin=%v: status=%d", admin, w.Code)
		}
		hasAdminLinks := strings.Contains(w.Body.String(), `href="/admin/users"`)
		hasMobileNav := strings.Contains(w.Body.String(), `class="workspace-mobile-nav"`)
		if hasAdminLinks != admin || hasMobileNav == admin {
			t.Errorf("admin=%v: admin links=%v mobile navigation=%v", admin, hasAdminLinks, hasMobileNav)
		}
		if admin && !strings.Contains(w.Body.String(), `href="https://public.example/generate"`) {
			t.Error("admin account page lost the public studio destination")
		}
	}
	view := workspaceShell(map[string]any{"CurrentUser": &User{Role: "user"}, "AdminWorkspace": true}, false)
	if view.Admin {
		t.Error("listener context must not expose an administrator navigation to an ordinary user")
	}
}

func TestWorkspaceNavigationPermissions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		user    domain.User
		allowed []string
	}{
		{"limited", domain.User{Role: "user"}, []string{"/app"}},
		{"generation", domain.User{Role: "user", CanUseQuickGeneration: true}, []string{"/generate", "/gallery", "/app"}},
		{"training", domain.User{Role: "user", CanTrainImageLora: true}, []string{"/train-lora", "/app"}},
		{"comfy", domain.User{Role: "user", CanUseComfyUI: true}, []string{"/comfyui/", "/app"}},
		{"chat", domain.User{Role: "user", CanUseOpenWebUI: true}, []string{"/openwebui/", "/app"}},
		{"admin", domain.User{Role: "admin"}, []string{"/generate", "/gallery", "/train-lora", "/comfyui/", "/openwebui/", "/app", "https://admin.example/admin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := workspaceNavigation(&tc.user, false, false, "https://admin.example", "https://public.example")
			links := map[string]bool{}
			for _, group := range view.Groups {
				for _, entry := range group.Items {
					if !entry.Task {
						links[entry.Href] = true
					}
				}
			}
			if len(links) != len(tc.allowed) {
				t.Fatalf("links = %v; want %v", links, tc.allowed)
			}
			for _, href := range tc.allowed {
				if !links[href] {
					t.Errorf("missing %s", href)
				}
			}
			for _, entry := range view.Mobile {
				if !entry.Task && !links[entry.Href] {
					t.Errorf("mobile exposes %s", entry.Href)
				}
			}
		})
	}
}

func TestWorkspaceNavigationKeepsAdminReviewWhenIntakeIsHidden(t *testing.T) {
	user := &domain.User{Role: "admin"}
	for _, enabled := range []bool{false, true} {
		for _, admin := range []bool{false, true} {
			view := workspaceNavigation(user, admin, enabled, "https://admin.example", "https://public.example")
			hasPublic, hasReview := false, false
			for _, group := range view.Groups {
				for _, entry := range group.Items {
					hasPublic = hasPublic || entry.Href == "/suggestions"
					hasReview = hasReview || entry.Href == "/admin/suggestions"
				}
			}
			if hasPublic != (!admin && enabled) || hasReview != admin {
				t.Fatalf("admin=%v enabled=%v: public=%v review=%v", admin, enabled, hasPublic, hasReview)
			}
		}
	}
}

func TestWorkspaceShellSharesNavigationWithoutDuplicateDialogs(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nav", "admin_nav"} {
		var result bytes.Buffer
		err := templates.ExecuteTemplate(&result, name, map[string]any{"CurrentUser": &domain.User{Role: "admin", Username: "long-user-name"}, "CSRF": "test-csrf"})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"workspace-navigation", "workspace-account", "notification-panel"} {
			if count := strings.Count(result.String(), `id="`+id+`"`); count != 1 {
				t.Errorf("%s: %s appears %d times", name, id, count)
			}
		}
		if !strings.Contains(result.String(), `value="test-csrf"`) {
			t.Error("missing form CSRF")
		}
	}
}
