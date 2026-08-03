package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSelectedService(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		referer string
		cookie  string
		want    string
	}{
		{name: "default remains comfyui", path: "/api/prompt", want: "comfyui"},
		{name: "openwebui config is deterministic", path: "/api/config", cookie: "comfyui", want: "openwebui"},
		{name: "explicit selector wins over stale cookie", path: "/?gateway_service=comfyui", cookie: "openwebui", want: "comfyui"},
		{name: "openwebui cookie", path: "/api/other", cookie: "openwebui", want: "openwebui"},
		{name: "comfyui cookie", path: "/api/other", cookie: "comfyui", want: "comfyui"},
		{name: "referer wins over cookie", path: "/api/other", referer: "https://ai.example/openwebui/", cookie: "comfyui", want: "openwebui"},
		{name: "comfyui referer wins", path: "/api/other", referer: "https://ai.example/comfyui/", cookie: "openwebui", want: "comfyui"},
		{name: "invalid cookie ignored", path: "/api/other", cookie: "other", want: "comfyui"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.referer != "" {
				request.Header.Set("Referer", test.referer)
			}
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: serviceCookieName, Value: test.cookie})
			}
			if got := selectedService(request); got != test.want {
				t.Fatalf("selectedService() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestServiceCompatibilityRouter(t *testing.T) {
	app := &App{}
	comfyUI := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Service", "comfyui") })
	openWebUI := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Service", "openwebui") })
	handler := app.serviceCompatibilityRouter(comfyUI, openWebUI)

	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.AddCookie(&http.Cookie{Name: serviceCookieName, Value: "openwebui"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got := response.Header().Get("X-Service"); got != "openwebui" {
		t.Fatalf("X-Service = %q, want openwebui", got)
	}
}

func TestRootOrServices(t *testing.T) {
	app := &App{}
	services := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Service", "selected")
	})
	handler := app.rootOrServices(services)

	request := httptest.NewRequest(http.MethodGet, "/c/chat-id", nil)
	request.AddCookie(&http.Cookie{Name: serviceCookieName, Value: "openwebui"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got := response.Header().Get("X-Service"); got != "selected" {
		t.Fatalf("X-Service = %q, want selected", got)
	}
}
