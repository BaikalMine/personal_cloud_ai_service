//go:build windows

package inference

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
	"golang.org/x/sys/windows"
)

func TestOllamaHTTPNativeCancellationReceipt(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	trace := filepath.Join(directory, "trace.json")
	t.Setenv("OWNED_WORKER_FIXTURE", "1")
	t.Setenv("OWNED_WORKER_STREAM", "1")
	t.Setenv("OWNED_WORKER_TRACE", trace)
	runtime, err := modelruntime.New(modelruntime.Config{Executable: executable, ModelsDirectory: directory, StateDirectory: filepath.Join(directory, "owner"), Address: address, StartupTimeout: 5 * time.Second, StopTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	x := newOllamaHTTPFixtureWithRuntime(t, runtime)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response := x.request(t, ctx, "/api/chat")
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "{\"done\":false,\"message\":{\"content\":\"fixture\"}}\n" {
		t.Fatalf("native stream: %q %v", line, err)
	}
	x.assertRunning(t)
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	if json.Unmarshal(data, &pids) != nil || len(pids) != 2 {
		t.Fatal("native fixture process IDs missing")
	}
	var handles []windows.Handle
	for _, pid := range pids {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(handle)
		handles = append(handles, handle)
		if status, err := windows.WaitForSingleObject(handle, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
			t.Fatalf("fixture already stopped: %d %v", status, err)
		}
	}
	cancel()
	await(t, x.completed)
	for _, handle := range handles {
		if status, err := windows.WaitForSingleObject(handle, 0); err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatalf("permit cleanup preceded native stop: %d %v", status, err)
		}
	}
	if _, err = x.store.ActiveGPUExecutorPermit(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("native cancellation did not settle permit", err)
	}
	var code string
	if err = x.db.QueryRow(`SELECT completion_code FROM gpu_executor_permits WHERE executor='ollama'`).Scan(&code); err != nil || code != "quiesced" {
		t.Fatalf("native receipt: %q %v", code, err)
	}
	if !x.worker.ownedReady() {
		t.Fatal("confirmed native stop left worker unready")
	}
}
