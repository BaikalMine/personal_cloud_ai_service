package updateagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-access-gateway/internal/updates"
)

func storageServerFixture(t *testing.T) (*comfyStorageFixture, *Server, *updates.Client) {
	t.Helper()
	f := newComfyStorageFixture(t, 1)
	server, err := NewServer(strings.Repeat("s", 64), &fakeController{}, f.target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.storage.guard.client.CloseIdleConnections)
	host := httptest.NewServer(server.Handler())
	t.Cleanup(host.Close)
	endpoint, _ := url.Parse(host.URL + "/ignored?ignored=1")
	return f, server, updates.NewClient(endpoint, strings.Repeat("s", 64))
}

func TestComfyStorageHTTPRoundTrip(t *testing.T) {
	f, _, client := storageServerFixture(t)
	status, err := client.ComfyStorageStatus(context.Background())
	if err != nil || status.ExecutorID != f.request.ExecutorID || status.GuardJournalID != f.request.GuardJournalID || status.GuardHeld {
		t.Fatal(status, err)
	}
	if _, err := client.ReadComfyStorage(context.Background(), f.request); !errors.Is(err, updates.ErrComfyStorageNotFound) {
		t.Fatal(err)
	}
	result, err := client.ExecuteComfyStorage(context.Background(), f.request)
	if err != nil || result.State != "completed" || !result.GuardClosed {
		t.Fatal(result, err)
	}
	read, err := client.ReadComfyStorage(context.Background(), f.request)
	if err != nil || read.RequestHash != result.RequestHash || read.Items[0].FreedBytes != result.Items[0].FreedBytes {
		t.Fatal(read, err)
	}
	if err := os.WriteFile(f.paths[0], []byte("rebuilt file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ExecuteComfyStorage(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FenceComfyStorage(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
	assertStorageFiles(t, f.paths, true)
	changed := f.request
	changed.Items = append([]updates.ComfyStorageSelection(nil), changed.Items...)
	changed.Items[0].Version = strings.Repeat("9", 64)
	if _, err := client.ExecuteComfyStorage(context.Background(), changed); !errors.Is(err, updates.ErrComfyStorageConflict) {
		t.Fatal(err)
	}
}

func TestComfyStorageHTTPKeepsPartialClosureEvidence(t *testing.T) {
	f, _, client := storageServerFixture(t)
	f.guard.fail["close"] = true
	result, err := client.ExecuteComfyStorage(context.Background(), f.request)
	if !errors.Is(err, updates.ErrUnavailable) || !result.ExecutorFenced || result.GuardClosed || result.Items[0].Outcome != "deleted" {
		t.Fatal(result, err)
	}
	status, err := client.ComfyStorageStatus(context.Background())
	if err != nil || status.PendingID != f.request.ID {
		t.Fatal(status, err)
	}
	f.guard.mu.Lock()
	f.guard.fail["close"] = false
	f.guard.mu.Unlock()
	result, err = client.FenceComfyStorage(context.Background(), f.request)
	if err != nil || !result.GuardClosed {
		t.Fatal(result, err)
	}
}

func TestComfyStorageHTTPAuthValidationAndNoLegacyBypass(t *testing.T) {
	f, server, _ := storageServerFixture(t)
	payload, _ := json.Marshal(f.request)
	for _, path := range []string{"/v1/comfy-storage", "/v1/comfy-storage/execute", "/v1/comfy-storage/fence", "/v1/comfy-storage/operations/" + f.request.ID} {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	for _, body := range []string{string(payload) + `{}`, strings.Replace(string(payload), `"protocol":1`, `"protocol":1,"unknown":true`, 1), strings.Repeat("x", 129<<10), `null`} {
		r := httptest.NewRequest(http.MethodPost, "/v1/comfy-storage/execute", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/v1/comfy-output/delete", "/v1/comfy-asset/delete"} {
		legacy, _ := json.Marshal(updates.ComfyAssetDeleteRequest{Files: []updates.ComfyAssetFile{f.request.Items[0].File}})
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(legacy))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "storage_operation_required") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if len(f.guard.calls) != 0 {
		t.Fatal("rejected HTTP reached guard", f.guard.calls)
	}
	assertStorageFiles(t, f.paths, true)
}

func TestComfyStorageHTTPDoesNotExposeJournalPathsOrKeys(t *testing.T) {
	f, server, _ := storageServerFixture(t)
	if err := os.WriteFile(filepath.Join(f.target.Storage.JournalDirectory, comfyStoragePendingFile), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(f.request)
	r := httptest.NewRequest(http.MethodPost, "/v1/comfy-storage/execute", bytes.NewReader(payload))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), f.target.Storage.JournalDirectory) || strings.Contains(w.Body.String(), f.target.Storage.GuardToken) {
		t.Fatal(w.Code, w.Body.String())
	}
	assertStorageFiles(t, f.paths, true)
}

func TestComfyStorageHTTPDisabledAndInvalidConfiguration(t *testing.T) {
	server, err := NewServer(strings.Repeat("s", 64), &fakeController{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/comfy-storage", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	f := newComfyStorageFixture(t, 1)
	config := *f.target.Storage
	config.GuardToken = "short"
	f.target.Storage = &config
	if _, err := NewServer(strings.Repeat("s", 64), &fakeController{}, f.target); err == nil {
		t.Fatal("misconfigured storage silently disabled")
	}
}

func TestComfyStorageInstallExclusion(t *testing.T) {
	f, server, _ := storageServerFixture(t)
	controller := server.controller.(*fakeController)
	f.guard.fail["close"] = true
	if _, err := f.executor.Execute(context.Background(), f.request); !errors.Is(err, ErrComfyStorageGuard) {
		t.Fatal(err)
	}
	request := updates.Request{Components: []string{updates.ComponentComfyUI}}
	if _, err := server.installWithStorageGuard(context.Background(), request); !errors.Is(err, ErrComfyStorageBusy) || len(controller.last.Components) > 0 {
		t.Fatal("install bypassed pending cleanup", err)
	}
	f.guard.mu.Lock()
	f.guard.fail["close"] = false
	f.guard.mu.Unlock()
	if _, err := f.executor.Fence(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
	if _, err := server.installWithStorageGuard(context.Background(), request); err != nil || len(controller.last.Components) != 1 {
		t.Fatal(err, controller.last)
	}
	root, lock, err := f.executor.openJournal()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	defer lock.Close()
	if _, err := server.installWithStorageGuard(context.Background(), request); !errors.Is(err, ErrComfyStorageBusy) {
		t.Fatal("install bypassed process lock", err)
	}
}
