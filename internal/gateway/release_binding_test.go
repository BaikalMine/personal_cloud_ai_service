package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-access-gateway/internal/release"
	"ai-access-gateway/internal/store"
)

func TestRuntimeBindingHandlerBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, method, target, role string
		code                       int
	}{
		{"anonymous", "GET", "/admin/release/binding", "", 403},
		{"ordinary_user", "GET", "/admin/release/binding", "user", 403},
		{"method", "POST", "/admin/release/binding", "admin", 405},
		{"query", "GET", "/admin/release/binding?x=1", "admin", 503},
		{"empty_query", "GET", "/admin/release/binding?", "admin", 503},
		{"restore", "GET", "/admin/release/binding", "admin", 503},
		{"validation", "GET", "/admin/release/binding", "admin", 503},
		{"missing", "GET", "/admin/release/binding", "admin", 400},
		{"duplicate", "GET", "/admin/release/binding", "admin", 400},
		{"malformed", "GET", "/admin/release/binding", "admin", 400},
		{"out_of_range", "GET", "/admin/release/binding", "admin", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{store: store.New(nil)}
			if tc.name == "restore" {
				a.cfg.RestoreDrillMode = true
			}
			if tc.name == "validation" {
				a.cfg.ReleaseValidation = testReleaseValidationRequest()
			}
			r := httptest.NewRequest(tc.method, tc.target, nil)
			if tc.role != "" {
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: tc.role}))
			}
			if tc.name != "missing" {
				r.Header.Set(release.RuntimeBindingPIDHeader, "1")
				r.Header.Set(release.RuntimeBindingClassHeader, "1")
			}
			switch tc.name {
			case "duplicate":
				r.Header.Add(release.RuntimeBindingPIDHeader, "1")
			case "malformed":
				r.Header.Set(release.RuntimeBindingClassHeader, "1,2")
			case "out_of_range":
				r.Header.Set(release.RuntimeBindingPIDHeader, "2147483648")
			}
			w := httptest.NewRecorder()
			a.handleAdminReleaseBinding(w, r)
			if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("boundary: %d %s", w.Code, w.Body.String())
			}
		})
	}
	if releaseValidationRouteAllowed(httptest.NewRequest(http.MethodGet, "/admin/release/binding", nil)) {
		t.Fatal("normal binding route is available in candidate validation mode")
	}
}
