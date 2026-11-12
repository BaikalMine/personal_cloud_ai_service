//go:build windows

package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/modelruntime"
	"ai-access-gateway/internal/store"
	"golang.org/x/sys/windows"
)

// The test executable impersonates only a local HTTP fixture, never Ollama.
func TestMain(m *testing.M) {
	if os.Getenv("OWNED_WORKER_FIXTURE") == "1" && len(os.Args) == 2 {
		switch os.Args[1] {
		case "leaf":
			time.Sleep(time.Minute)
			os.Exit(0)
		case "serve":
			child := exec.Command(os.Args[0], "leaf")
			child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
			if child.Start() != nil {
				os.Exit(3)
			}
			defer child.Process.Kill()
			mux := http.NewServeMux()
			mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"version":"fixture"}`) })
			mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
				data, _ := json.Marshal([]int{os.Getpid(), child.Process.Pid})
				if os.WriteFile(os.Getenv("OWNED_WORKER_TRACE"), data, 0600) != nil {
					os.Exit(4)
				}
				if os.Getenv("OWNED_WORKER_STREAM") == "1" {
					w.Header().Set("Content-Type", "application/x-ndjson")
					fmt.Fprintln(w, `{"done":false,"message":{"content":"fixture"}}`)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			})
			if http.ListenAndServe(os.Getenv("OLLAMA_HOST"), mux) != nil {
				os.Exit(5)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestOwnedWorkerCrashHelper(t *testing.T) {
	if os.Getenv("OWNED_WORKER_HELPER") != "1" {
		t.Skip("subprocess only")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, err := contentcrypto.NewCipher("inference-fixture-secret-never-production")
	if err != nil {
		t.Fatal(err)
	}
	var config modelruntime.Config
	if err = json.Unmarshal([]byte(os.Getenv("OWNED_WORKER_CONFIG")), &config); err != nil {
		t.Fatal(err)
	}
	runtime, err := modelruntime.New(config)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewOwnedWorker(store.New(db), cipher, runtime, os.Getenv("OWNED_WORKER_JOURNAL"))
	if err != nil {
		runtime.Close()
		t.Fatal(err)
	}
	defer worker.Close()
	if _, err = worker.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedWorkerNativeCrashRecovery(t *testing.T) {
	f := newFixture(t)
	job := f.submit(t)
	dir := t.TempDir()
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
	config := modelruntime.Config{Executable: executable, ModelsDirectory: dir, StateDirectory: filepath.Join(dir, "owner"), Address: address, StartupTimeout: 5 * time.Second, StopTimeout: 5 * time.Second}
	encoded, _ := json.Marshal(config)
	trace, journal := filepath.Join(dir, "trace.json"), filepath.Join(dir, "journal")
	command := exec.Command(executable, "-test.run=^TestOwnedWorkerCrashHelper$", "-test.timeout=30s")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	command.Env = append(os.Environ(), "OWNED_WORKER_FIXTURE=1", "OWNED_WORKER_HELPER=1", "OWNED_WORKER_CONFIG="+string(encoded), "OWNED_WORKER_TRACE="+trace, "OWNED_WORKER_JOURNAL="+journal)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { command.Process.Kill(); command.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	var pids []int
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(trace)
		if readErr == nil && json.Unmarshal(data, &pids) == nil && len(pids) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(pids) != 2 {
		command.Process.Kill()
		command.Wait()
		t.Fatalf("fixture did not observe dispatch: %s", output.String())
	}
	var handles []windows.Handle
	for _, pid := range pids {
		handle, openErr := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer windows.CloseHandle(handle)
		handles = append(handles, handle)
	}
	before, err := f.store.GPUInference(context.Background(), job.ID)
	if err != nil || before.State != "running" || before.ExecutorID == "" || before.ExecutorGeneration != 1 {
		t.Fatalf("binding before crash: %+v %v", before, err)
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	command.Wait()
	for _, handle := range handles {
		state, waitErr := windows.WaitForSingleObject(handle, 5000)
		if waitErr != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatalf("fixture descendant survived: %d %v", state, waitErr)
		}
	}
	runtime, err := modelruntime.New(config)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewOwnedWorker(f.store, f.cipher, runtime, journal)
	if err != nil {
		runtime.Close()
		t.Fatal(err)
	}
	defer worker.Close()
	if worked, runErr := worker.RunOne(context.Background()); runErr != nil || worked {
		t.Fatalf("restart: %v %v", worked, runErr)
	}
	after, err := f.store.GPUInference(context.Background(), job.ID)
	if err != nil || after.State != "stopped" || after.CompletionCode != "executor_stopped" || worker.owned.executor.Generation != 2 || worker.owned.executor.ID != before.ExecutorID {
		t.Fatalf("recovery: %+v %v", after, err)
	}
	if _, err = os.Stat(trace); err != nil {
		t.Fatal(err)
	}
}
