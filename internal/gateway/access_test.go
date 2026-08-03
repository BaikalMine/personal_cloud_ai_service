package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireServiceAccess(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{tpl: templates}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	tests := []struct {
		name   string
		user   User
		status int
	}{
		{name: "allowed user", user: User{CanUseComfyUI: true}, status: http.StatusNoContent},
		{name: "denied user", user: User{CanUseComfyUI: false}, status: http.StatusForbidden},
		{name: "admin bypass", user: User{Role: "admin"}, status: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/comfyui/", nil)
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &tt.user))
			response := httptest.NewRecorder()
			app.requireServiceAccess("comfyui", next).ServeHTTP(response, req)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d", response.Code, tt.status)
			}
			if tt.status == http.StatusForbidden && !strings.Contains(response.Body.String(), "Доступ запрещён") {
				t.Fatal("missing localized access denied page")
			}
		})
	}
}
