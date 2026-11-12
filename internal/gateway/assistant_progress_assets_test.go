package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssistantProgressProductionAsset(t *testing.T) {
	app := &App{}
	mux := http.NewServeMux()
	app.registerStaticRoutes(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/generation-assistant-progress.js", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "assistantProgress") {
		t.Fatal("production progress module is not served")
	}
	if response.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatal("invalid script content type")
	}
}
