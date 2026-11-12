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
		{name: "legacy grants denied", user: User{CanUseComfyUI: true, CanUseOpenWebUI: true}, status: http.StatusForbidden},
		{name: "denied user", user: User{CanUseComfyUI: false}, status: http.StatusForbidden},
		{name: "admin bypass", user: User{Role: "admin"}, status: http.StatusNoContent},
	}
	for _, tt := range tests {
		for _, service := range []string{"comfyui", "openwebui"} {
			t.Run(tt.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/"+service+"/", nil)
				req.Header.Set("Accept", "text/html")
				req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &tt.user))
				response := httptest.NewRecorder()
				app.requireServiceAccess(service, next).ServeHTTP(response, req)
				if response.Code != tt.status {
					t.Fatalf("status = %d, want %d", response.Code, tt.status)
				}
				if tt.status == http.StatusForbidden && !strings.Contains(response.Body.String(), "Доступ запрещён") {
					t.Fatal("missing localized access denied page")
				}
			})
		}
	}
}

func TestRequireQuickGenerationAccess(t *testing.T) {
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
		{name: "quick-only user", user: User{CanUseQuickGeneration: true}, status: http.StatusNoContent},
		{name: "comfy-only user", user: User{CanUseComfyUI: true}, status: http.StatusForbidden},
		{name: "administrator", user: User{Role: "admin"}, status: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/generate", nil)
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &tt.user))
			response := httptest.NewRecorder()
			app.requireServiceAccess("quick_generation", next).ServeHTTP(response, req)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d", response.Code, tt.status)
			}
		})
	}
}

func TestRawServiceCompatibilityRoutesAreAdminOnly(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{tpl: tpl}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	router := a.serviceCompatibilityRouter(a.requireServiceAccess("comfyui", next), a.requireServiceAccess("openwebui", next))
	for _, role := range []string{"user", "admin"} {
		for _, service := range []string{"comfyui", "openwebui"} {
			for _, path := range []string{"/?gateway_service=" + service, "/api/config", "/prompt", "/ws", "/api/chat/completions"} {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.AddCookie(&http.Cookie{Name: serviceCookieName, Value: service})
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{Role: role, CanUseComfyUI: true, CanUseOpenWebUI: true}))
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				want := http.StatusForbidden
				if role == "admin" {
					want = http.StatusNoContent
				}
				if w.Code != want {
					t.Fatalf("role=%s service=%s path=%s status=%d", role, service, path, w.Code)
				}
			}
		}
	}
}

func TestRequireLoraTrainingAccess(t *testing.T) {
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
		{name: "explicit image LoRA permission", user: User{CanTrainImageLora: true}, status: http.StatusNoContent},
		{name: "image generation alone is insufficient", user: User{CanGenerateTextToImage: true}, status: http.StatusForbidden},
		{name: "administrator bypass", user: User{Role: "admin"}, status: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/train-lora", nil)
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &tt.user))
			response := httptest.NewRecorder()
			app.requireLoraTrainingAccess(next).ServeHTTP(response, req)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d", response.Code, tt.status)
			}
			if tt.status == http.StatusForbidden && !strings.Contains(response.Body.String(), "обучению LoRA для изображений") {
				t.Fatal("missing localized LoRA access explanation")
			}
		})
	}
}
