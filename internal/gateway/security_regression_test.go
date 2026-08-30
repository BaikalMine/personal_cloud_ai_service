package gateway

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSafeNextAllowsOnlySameOriginPaths(t *testing.T) {
	valid := []string{"/app", "/generate?mode=video", "/admin/content/media/42#details"}
	for _, candidate := range valid {
		if got := safeNext(candidate); got != candidate {
			t.Errorf("safeNext(%q) = %q", candidate, got)
		}
	}

	unsafe := []string{
		"", "https://example.com", "//example.com", `\\example.com`, `/\\example.com`,
		"/%5cexample.com", "/%2f%2fexample.com", "/app\r\nLocation: https://example.com",
	}
	for _, candidate := range unsafe {
		if got := safeNext(candidate); got != "" {
			t.Errorf("unsafe safeNext(%q) = %q", candidate, got)
		}
	}
}

func TestPublicListenerRequiresAdminAndKeepsMetricsPrivate(t *testing.T) {
	upstream, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{
		AdminBaseURL:      "https://ai.example.test",
		ComfyUIUpstream:   upstream,
		OpenWebUIUpstream: upstream,
	}}
	handler := app.publicMux()
	for _, path := range []string{"/admin", "/admin/users?disabled=1"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusFound {
			t.Errorf("unauthenticated public %s returned %d, want login redirect", path, response.Code)
			continue
		}
		if location := response.Header().Get("Location"); !strings.HasPrefix(location, "/login?next=") {
			t.Errorf("public %s redirected to %q, want login", path, location)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Errorf("public /metrics returned %d, want 404", response.Code)
	}
}

func TestAdminListenerRetainsPrivateAdminRoute(t *testing.T) {
	_, network, err := net.ParseCIDR("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{AdminAllowedNetworks: []*net.IPNet{network}}}
	request := httptest.NewRequest(http.MethodGet, "/admin", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.adminMux().ServeHTTP(response, request)
	if response.Code != http.StatusFound {
		t.Fatalf("private admin route returned %d, want login redirect", response.Code)
	}
}

func TestHTTPServerBoundsRequestBodyReadTime(t *testing.T) {
	server := newHTTPServer(":0", http.NotFoundHandler())
	if server.ReadTimeout <= 0 || server.ReadTimeout > 15*time.Minute {
		t.Fatalf("ReadTimeout = %s, want a positive bounded value", server.ReadTimeout)
	}
}

func TestComfyFreeRequiresAdministrator(t *testing.T) {
	app := &App{}
	request := httptest.NewRequest(http.MethodPost, "/free", nil)
	allowed, err := app.enforceComfyControlIsolation(request, &User{Role: "user"}, "comfyui")
	if err != nil || allowed {
		t.Fatalf("ordinary user /free = allowed:%v err:%v", allowed, err)
	}
	allowed, err = app.enforceComfyControlIsolation(request, &User{Role: "admin"}, "comfyui")
	if err != nil || !allowed {
		t.Fatalf("admin /free = allowed:%v err:%v", allowed, err)
	}
}

func TestComfyManagerPathsRequireAdministrator(t *testing.T) {
	blocked := []string{
		"/manager/queue/install",
		"/customnode/install/pip",
		"/snapshot/restore",
		"/externalmodel/getlist",
		"/comfyui_manager/comfyui_switch_version",
	}
	for _, requestPath := range blocked {
		if !isComfyAdminOnlyPath(requestPath) {
			t.Errorf("isComfyAdminOnlyPath(%q) = false", requestPath)
		}
	}
	for _, requestPath := range []string{"/prompt", "/view", "/managerial/workflow"} {
		if isComfyAdminOnlyPath(requestPath) {
			t.Errorf("isComfyAdminOnlyPath(%q) = true", requestPath)
		}
	}
}
