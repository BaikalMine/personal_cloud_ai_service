package mining

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
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

func TestClientHealthChecksAgentWithoutCommandPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/healthz" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	if err := NewClient(baseURL, "test-token").Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientBackfillsCollectedAtFromLegacyAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(State{Running: true})
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	before := time.Now().UTC()
	state, err := NewClient(baseURL, "test-token").State(context.Background(), "miner.exe")
	if err != nil {
		t.Fatal(err)
	}
	if state.CollectedAt.Before(before) || state.CollectedAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("legacy collected_at = %v", state.CollectedAt)
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

func TestClientUsesLongRunningMinerUpdateEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Path != "/v1/update" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		var request UpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ArchiveURL != "https://example.com/miner.zip" || request.MinerName != "Example miner" || request.ArchiveSHA256 != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
			t.Fatalf("unexpected update request: %+v", request)
		}
		_ = json.NewEncoder(w).Encode(UpdateResult{Success: true, PreservedScripts: 3, Message: "ok"})
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client := NewClient(baseURL, "test-token")
	result, err := client.Update(context.Background(), UpdateRequest{
		ScriptPath: `testdata/mining/start.bat`, ProcessName: "miner.exe", MinerName: "Example miner", ArchiveURL: "https://example.com/miner.zip", ArchiveSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	if err != nil || !result.Success || result.PreservedScripts != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestClientUsesDedicatedTransportForLongRunningUpdates(t *testing.T) {
	client := NewClient(&url.URL{Scheme: "http", Host: "localhost"}, "test-token")
	regularTransport, ok := client.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("regular transport type = %T", client.http.Transport)
	}
	updateTransport, ok := client.updateHTTP.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("update transport type = %T", client.updateHTTP.Transport)
	}
	if regularTransport == updateTransport {
		t.Fatal("long-running updates must not share the short-lived request transport")
	}
	if regularTransport.ResponseHeaderTimeout != 4*time.Second {
		t.Fatalf("regular response header timeout = %s", regularTransport.ResponseHeaderTimeout)
	}
	if updateTransport.ResponseHeaderTimeout != 0 {
		t.Fatalf("update response header timeout = %s, want disabled", updateTransport.ResponseHeaderTimeout)
	}
	if client.updateHTTP.Timeout != updateTimeout {
		t.Fatalf("update client timeout = %s, want %s", client.updateHTTP.Timeout, updateTimeout)
	}
}

func TestClientTrimsOnlyConfiguredComfyMemory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Method != http.MethodPost || r.URL.Path != "/v1/comfyui/trim" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if r.ContentLength > 0 {
			t.Fatal("memory trim must not accept caller-controlled process data")
		}
		_ = json.NewEncoder(w).Encode(ComfyMemoryTrim{Trimmed: 1, Message: "ok"})
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	result, err := NewClient(baseURL, "test-token").TrimComfyMemory(context.Background())
	if err != nil || result.Trimmed != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestClientReportsLegacyMonitorWithoutJSONDecodeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	result, err := NewClient(baseURL, "test-token").TrimComfyMemory(context.Background())
	if err == nil || result.Message != "Windows-агент пока не поддерживает очистку памяти ComfyUI." {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
