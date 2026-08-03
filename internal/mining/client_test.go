package mining

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestClientAuthenticatesAndControlsAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/start" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var request Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ScriptPath != `testdata/mining/start.bat` || request.ProcessName != "miner.exe" {
			t.Fatalf("unexpected request: %+v", request)
		}
		_ = json.NewEncoder(w).Encode(State{Running: true, PIDs: []int{42}})
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client := NewClient(baseURL, "test-token")
	state, err := client.Start(context.Background(), Request{ScriptPath: `testdata/mining/start.bat`, ProcessName: "miner.exe"})
	if err != nil {
		t.Fatal(err)
	}
	if !state.Available || !state.Running || len(state.PIDs) != 1 || state.PIDs[0] != 42 {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestClientWithoutTokenIsUnavailable(t *testing.T) {
	client := NewClient(&url.URL{Scheme: "http", Host: "localhost"}, "")
	if _, err := client.State(context.Background(), "miner.exe"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestClientReadsScriptContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/script" || r.URL.Query().Get("script_path") != `testdata/mining/start.bat` {
			t.Fatalf("unexpected request URL: %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(Script{Path: `testdata/mining/start.bat`, Content: "@echo off", SHA256: "abc"})
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client := NewClient(baseURL, "test-token")
	script, err := client.Script(context.Background(), `testdata/mining/start.bat`)
	if err != nil {
		t.Fatal(err)
	}
	if script.Content != "@echo off" || script.SHA256 != "abc" {
		t.Fatalf("unexpected script: %+v", script)
	}
}
