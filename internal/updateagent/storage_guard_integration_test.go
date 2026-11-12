package updateagent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/updates"
)

func TestComfyStorageRealGuardIntegration(t *testing.T) {
	python := os.Getenv("COMFY_GUARD_TEST_PYTHON")
	if python == "" {
		t.Skip("COMFY_GUARD_TEST_PYTHON is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	stateDirectory := t.TempDir()
	var destination atomic.Pointer[url.URL]
	proxy := &httputil.ReverseProxy{Director: func(r *http.Request) {
		target := destination.Load()
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
	}}
	front := httptest.NewServer(proxy)
	defer front.Close()
	start := func() func() {
		t.Helper()
		run, stop := context.WithCancel(ctx)
		command := exec.CommandContext(run, python, "-B", "../../integrations/comfyui_gpu_guard/storage_fixture.py", stateDirectory)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		stdout, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		closed := false
		cleanup := func() {
			if !closed {
				closed = true
				stop()
				_ = command.Wait()
				if stderr.Len() > 0 {
					t.Log(stderr.String())
				}
			}
		}
		t.Cleanup(cleanup)
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() {
			cleanup()
			t.Fatal("guard fixture did not start", stderr.String())
		}
		base, err := url.Parse(scanner.Text())
		if err != nil || base.Scheme != "http" || base.Hostname() != "127.0.0.1" {
			cleanup()
			t.Fatal("invalid fixture address")
		}
		destination.Store(base)
		return cleanup
	}
	stop := start()
	target := ComfyTarget{InputDirectory: t.TempDir(), OutputDirectory: t.TempDir(), Storage: &ComfyStorageConfig{JournalDirectory: t.TempDir(), GuardURL: front.URL + "/ai-gateway/storage/window", GuardToken: strings.Repeat("k", 64)}}
	executor, err := NewComfyStorageExecutor(target)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.guard.client.CloseIdleConnections()
	status, err := executor.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("synthetic tracked input")
	file := filepath.Join(target.InputDirectory, "input.png")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	request := updates.ComfyStorageRequest{Protocol: 1, ID: strings.Repeat("a", 32), ExecutorID: status.ExecutorID, GuardJournalID: status.GuardJournalID, Items: []updates.ComfyStorageSelection{{Key: "input:1", Version: strings.Repeat("e", 64), File: updates.ComfyAssetFile{Filename: "input.png", StorageType: "input", SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}}}}
	send := func(path string) int {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+path, strings.NewReader(`{"prompt":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if code := send("/prompt"); code != 200 {
		t.Fatal("synthetic prompt was not admitted", code)
	}
	refused, err := executor.Execute(ctx, request)
	if err != nil || refused.State != "rejected" || !refused.GuardClosed {
		t.Fatal("active prompt did not exclude deletion", refused, err)
	}
	assertStorageFiles(t, []string{file}, true)
	if code := send("/test/complete"); code != 200 {
		t.Fatal("synthetic executor did not finish", code)
	}
	request.ID = strings.Repeat("b", 32)
	entered, release := make(chan struct{}), make(chan struct{})
	original := executor.remove
	executor.remove = func(ctx context.Context, dir string, file updates.ComfyAssetFile, remove bool) (string, error) {
		if remove {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return original(ctx, dir, file, remove)
	}
	type outcome struct {
		result updates.ComfyStorageResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := executor.Execute(ctx, request); done <- outcome{result, err} }()
	select {
	case <-entered:
	case result := <-done:
		t.Fatal("execution did not reach guarded unlink", result)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	joined := false
	defer func() {
		if !joined {
			close(release)
			<-done
		}
	}()
	for _, path := range []string{"/prompt", "/api/prompt", "/upload/image", "/api/upload/mask"} {
		if code := send(path); code != 409 {
			t.Fatal("host window allowed mutation", path, code)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDirectory, "upload-reached-handler")); !os.IsNotExist(err) {
		t.Fatal("blocked upload reached handler", err)
	}
	stop()
	stop = start()
	defer stop()
	for _, path := range []string{"/prompt", "/upload/image"} {
		if code := send(path); code != 409 {
			t.Fatal("restart lost storage window", path, code)
		}
	}
	close(release)
	completed := <-done
	joined = true
	if completed.err != nil || completed.result.State != "completed" || !completed.result.GuardClosed {
		t.Fatal(completed.result, completed.err)
	}
	assertStorageFiles(t, []string{file}, false)
	if code := send("/upload/image"); code != 200 {
		t.Fatal("closed window did not restore file admission", code)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewComfyStorageExecutor(target)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.guard.client.CloseIdleConnections()
	replayed, err := restarted.Execute(ctx, request)
	if err != nil || replayed.RequestHash != completed.result.RequestHash {
		t.Fatal(replayed, err)
	}
	assertStorageFiles(t, []string{file}, true)
	var archive bytes.Buffer
	manifest, err := ExportComfyStorageBackup(ctx, target, &archive)
	if err != nil || manifest.HostReceipts != 2 || manifest.GuardWindows != 2 {
		t.Fatal("real SQLite journal export failed", manifest, err)
	}
	restored, err := RestoreComfyStorageBackup(ctx, comfyBackupArchive(t, archive.Bytes()), filepath.Join(t.TempDir(), "restored"), comfyBackupPython(t))
	if err != nil || !sameComfyBackupSummary(manifest, restored) {
		t.Fatal("real guard backup roundtrip failed", restored, err)
	}
	assertStorageFiles(t, []string{file}, true)
}
