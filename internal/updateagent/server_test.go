package updateagent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-access-gateway/internal/updates"
)

type fakeController struct {
	last updates.Request
}

func (f *fakeController) Status(context.Context) (updates.Status, error) {
	return updates.Status{Components: []updates.ComponentStatus{{Name: updates.ComponentGateway}}}, nil
}

func (f *fakeController) Check(_ context.Context, request updates.Request) (updates.Status, error) {
	f.last = request
	return updates.Status{}, nil
}

func (f *fakeController) Install(_ context.Context, request updates.Request) (updates.Status, error) {
	f.last = request
	return updates.Status{}, nil
}

func TestServerRejectsUnauthorizedCommands(t *testing.T) {
	controller := &fakeController{}
	server, err := NewServer(strings.Repeat("x", 32), controller)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/install", strings.NewReader(`{"components":["gateway"]}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestServerAllowsOnlyKnownDistinctComponents(t *testing.T) {
	controller := &fakeController{}
	server, err := NewServer(strings.Repeat("x", 32), controller)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/check", strings.NewReader(`{"components":["gateway","comfyui"]}`))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if len(controller.last.Components) != 2 {
		t.Fatalf("components = %#v", controller.last.Components)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/check", strings.NewReader(`{"components":["gateway","gateway"]}`))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
