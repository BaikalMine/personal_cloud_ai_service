package miningagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-access-gateway/internal/mining"
)

type fakeController struct {
	request mining.Request
}

func (f *fakeController) State(context.Context, string) (mining.State, error) {
	return mining.State{Running: true, PIDs: []int{7}}, nil
}

func (f *fakeController) Script(_ context.Context, path string) (mining.Script, error) {
	return mining.Script{Path: path, Content: "@echo off", SHA256: "abc"}, nil
}

func (f *fakeController) Start(_ context.Context, request mining.Request) (mining.State, error) {
	f.request = request
	return mining.State{Running: true, PIDs: []int{8}}, nil
}

func (f *fakeController) Stop(_ context.Context, request mining.Request) (mining.State, error) {
	f.request = request
	return mining.State{Running: false}, nil
}

func TestServerRequiresToken(t *testing.T) {
	agent, err := NewServer("01234567890123456789012345678901", &fakeController{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/state?process_name=miner.exe", nil)
	response := httptest.NewRecorder()
	agent.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestServerReturnsObservedState(t *testing.T) {
	agent, _ := NewServer("01234567890123456789012345678901", &fakeController{})
	request := httptest.NewRequest(http.MethodGet, "/v1/state?process_name=miner.exe", nil)
	request.Header.Set("Authorization", "Bearer 01234567890123456789012345678901")
	response := httptest.NewRecorder()
	agent.Handler().ServeHTTP(response, request)
	var state mining.State
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !state.Available || !state.Running || state.PIDs[0] != 7 {
		t.Fatalf("status=%d state=%+v", response.Code, state)
	}
}

func TestServerReturnsAuthenticatedScript(t *testing.T) {
	agent, _ := NewServer("01234567890123456789012345678901", &fakeController{})
	request := httptest.NewRequest(http.MethodGet, `/v1/script?script_path=C%3A%5CMining%5Cstart.bat`, nil)
	request.Header.Set("Authorization", "Bearer 01234567890123456789012345678901")
	response := httptest.NewRecorder()
	agent.Handler().ServeHTTP(response, request)
	var script mining.Script
	if err := json.NewDecoder(response.Body).Decode(&script); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || script.Content != "@echo off" || script.SHA256 != "abc" {
		t.Fatalf("status=%d script=%+v", response.Code, script)
	}
}
