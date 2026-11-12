//go:build windows

package modelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"ai-access-gateway/internal/processowner"
	"golang.org/x/sys/windows"
)

const modelFixtureRequest = `{"model":"fixture-local","stream":false,"messages":[{"role":"user","content":"Describe a green vase","images":["Zml4dHVyZQ=="]}]}`

type fixtureProcesses struct {
	Server, Child int
	Host, Models  string
}

func fixtureCommand(t *testing.T, role, mode, path string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestModelRuntimeHelper$", "--", role, mode, path)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	return command
}

func installFixture(t *testing.T, r *Runtime, mode, trace string) {
	t.Helper()
	base := r.command
	r.command = func() *exec.Cmd {
		command := fixtureCommand(t, "server", mode, trace)
		command.Env = base().Env
		return command
	}
}

func fixtureConfig(t *testing.T, dir string) Config {
	t.Helper()
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
	return Config{Executable: executable, ModelsDirectory: dir, StateDirectory: filepath.Join(dir, "state"), Address: address, StartupTimeout: 2 * time.Second, StopTimeout: 5 * time.Second}
}

func newFixture(t *testing.T, mode string) (*Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := New(fixtureConfig(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	trace := filepath.Join(dir, "trace.json")
	installFixture(t, r, mode, trace)
	return r, trace
}

func TestModelRuntimeHelper(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) != 3 {
		t.Skip("model runtime subprocess helper")
	}
	role, mode, path := args[0], args[1], args[2]
	if role == "leaf" {
		time.Sleep(30 * time.Second)
		return
	}
	if role == "supervisor" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config Config
		if err = json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		r, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		installFixture(t, r, mode, path+".trace")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if mode == "stream" {
			r.ExecuteHTTP(ctx, HTTPRequest{Method: http.MethodPost, Path: "/api/chat", Body: []byte(modelFixtureRequest), ResponseLimit: 4096}, func(_ int, _ http.Header, reader io.Reader) error {
				_, err := io.Copy(io.Discard, reader)
				return err
			})
		} else {
			r.Execute(ctx, []byte(modelFixtureRequest))
		}
		return
	}
	if role != "server" {
		t.Fatal("unknown fixture role")
	}
	child := fixtureCommand(t, "leaf", mode, path)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	pids := fixtureProcesses{Server: os.Getpid(), Child: child.Process.Pid, Host: os.Getenv("OLLAMA_HOST"), Models: os.Getenv("OLLAMA_MODELS")}
	data, _ := json.Marshal(pids)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if mode == "idle" {
		time.Sleep(30 * time.Second)
		return
	}
	listener, err := net.Listen("tcp4", os.Getenv("OLLAMA_HOST"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			io.WriteString(w, `{"version":"fixture"}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		os.WriteFile(path+".request", body, 0600)
		os.WriteFile(path+".route", []byte(r.Method+" "+r.URL.RequestURI()), 0600)
		switch mode {
		case "stream", "stream-disconnect":
			w.Header().Set("Content-Type", "application/x-ndjson")
			io.WriteString(w, "{\"done\":false,\"response\":\"first\"}\n")
			w.(http.Flusher).Flush()
			os.WriteFile(path+".streamed", []byte("flushed"), 0600)
			if mode == "stream-disconnect" {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					connection.Close()
				}
				return
			}
			for {
				if _, err := os.Stat(path + ".continue"); err == nil {
					break
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
			io.WriteString(w, "{\"done\":true,\"response\":\"last\"}\n")
		case "redirect":
			w.Header().Set("Location", "http://127.0.0.1:1/foreign")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "hang":
			<-r.Context().Done()
		case "disconnect":
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				connection.Close()
			}
		case "error":
			http.Error(w, "fixture error", 500)
		case "malformed":
			io.WriteString(w, `{"done":`)
		case "oversized":
			io.WriteString(w, strings.Repeat("x", 65537))
		default:
			io.WriteString(w, `{"done":true,"message":{"content":"a green vase"}}`)
		}
	})}
	defer server.Close()
	go server.Serve(listener)
	time.Sleep(30 * time.Second)
}

func waitFixtureRequest(t *testing.T, path string) fixtureProcesses {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path + ".request")
		if err == nil && string(body) == modelFixtureRequest {
			data, err := os.ReadFile(path)
			var pids fixtureProcesses
			if err == nil && json.Unmarshal(data, &pids) == nil && pids.Server > 0 && pids.Child > 0 {
				return pids
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fixture request not observed")
	return fixtureProcesses{}
}

func TestModelRuntimeStopsAfterEveryOutcome(t *testing.T) {
	for _, mode := range []string{"normal", "error", "malformed", "disconnect", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			r, trace := newFixture(t, mode)
			result, err := r.Execute(context.Background(), []byte(modelFixtureRequest))
			if !result.Dispatched || !result.Stopped {
				t.Fatal(result, err)
			}
			if (mode == "disconnect" || mode == "oversized") && err == nil {
				t.Fatal("transport error lost")
			}
			if mode == "normal" && (err != nil || result.Status != 200 || !strings.Contains(string(result.Body), "green vase")) {
				t.Fatal(result, err)
			}
			pids := waitFixtureRequest(t, trace)
			if pids.Host != r.config.Address || pids.Models != r.config.ModelsDirectory {
				t.Fatal("child environment not applied", pids)
			}
			if active, err := r.group.Active(); err != nil || active != 0 {
				t.Fatal("model tree survived", active, err)
			}
			if mode == "normal" {
				result, err = r.Execute(context.Background(), []byte(modelFixtureRequest))
				if err != nil || !result.Stopped || result.Status != 200 {
					t.Fatal("restart", result, err)
				}
			}
		})
	}
}

func TestModelRuntimeCancellationBusyAndClose(t *testing.T) {
	r, trace := newFixture(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := r.Execute(ctx, []byte(modelFixtureRequest)); done <- outcome{result, err} }()
	waitFixtureRequest(t, trace)
	if result, err := r.Execute(context.Background(), []byte(modelFixtureRequest)); !errors.Is(err, ErrBusy) || result.Stopped {
		t.Fatal("concurrent model admitted", result, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	completed := <-done
	if !completed.result.Stopped || !errors.Is(completed.err, context.Canceled) {
		t.Fatal(completed)
	}
	if _, err := r.Execute(context.Background(), []byte(modelFixtureRequest)); !errors.Is(err, context.Canceled) {
		t.Fatal("closed runtime accepted work", err)
	}
}

func TestModelRuntimeStartupAndStopProofFailures(t *testing.T) {
	t.Run("startup timeout", func(t *testing.T) {
		r, _ := newFixture(t, "idle")
		r.config.StartupTimeout = 200 * time.Millisecond
		result, err := r.Execute(context.Background(), []byte(modelFixtureRequest))
		if !errors.Is(err, context.DeadlineExceeded) || result.Dispatched || !result.Stopped {
			t.Fatal(result, err)
		}
	})
	t.Run("missing stop proof", func(t *testing.T) {
		r, trace := newFixture(t, "hang")
		done := make(chan Result, 1)
		go func() { result, _ := r.Execute(context.Background(), []byte(modelFixtureRequest)); done <- result }()
		waitFixtureRequest(t, trace)
		if err := r.group.Close(); err != nil {
			t.Fatal(err)
		}
		result := <-done
		if result.Stopped || len(result.Body) != 0 {
			t.Fatal("missing ownership handle became idle proof", result)
		}
	})
}

func TestModelRuntimeEnvironmentIsPerChild(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1")
	dir := t.TempDir()
	config := fixtureConfig(t, dir)
	config.ModelsDirectory = filepath.Join(dir, "models with spaces \u6a21\u578b")
	if err := os.Mkdir(config.ModelsDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	trace := filepath.Join(dir, "trace.json")
	installFixture(t, r, "normal", trace)
	result, err := r.Execute(context.Background(), []byte(modelFixtureRequest))
	if err != nil || !result.Stopped {
		t.Fatal(result, err)
	}
	pids := waitFixtureRequest(t, trace)
	if pids.Models != config.ModelsDirectory || pids.Host != config.Address || os.Getenv("OLLAMA_HOST") != "127.0.0.1:1" {
		t.Fatal("environment override leaked or was corrupted", pids)
	}
}

func TestModelRuntimeRejectsForeignPortBeforeSending(t *testing.T) {
	r, _ := newFixture(t, "idle")
	var requests atomic.Int32
	var foreign *httptest.Server
	base := r.command
	r.command = func() *exec.Cmd {
		listener, err := net.Listen("tcp4", r.config.Address)
		if err != nil {
			t.Fatal(err)
		}
		foreign = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			io.WriteString(w, `{"version":"foreign"}`)
		}))
		foreign.Listener = listener
		foreign.Start()
		return base()
	}
	defer func() {
		if foreign != nil {
			foreign.Close()
		}
	}()
	result, err := r.Execute(context.Background(), []byte(modelFixtureRequest))
	if !errors.Is(err, processowner.ErrUnownedPeer) || result.Dispatched || !result.Stopped || requests.Load() != 0 {
		t.Fatal("foreign endpoint used", result, err, requests.Load())
	}
	response, err := http.Get(foreign.URL)
	if err != nil {
		t.Fatal("foreign server stopped", err)
	}
	response.Body.Close()
	if requests.Load() != 1 {
		t.Fatal(requests.Load())
	}
}

func TestModelRuntimeOwnerCrashStopsServerAndRunner(t *testing.T) {
	for _, mode := range []string{"hang", "stream"} {
		t.Run(mode, func(t *testing.T) { testModelRuntimeOwnerCrash(t, mode) })
	}
}

func testModelRuntimeOwnerCrash(t *testing.T, mode string) {
	dir := t.TempDir()
	config := fixtureConfig(t, dir)
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(config)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	owner := fixtureCommand(t, "supervisor", mode, path)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			owner.Process.Kill()
			owner.Wait()
		}
	}()
	pids := waitFixtureRequest(t, path+".trace")
	if mode == "stream" {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(path + ".trace.streamed"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("stream did not start before owner crash")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	var handles []windows.Handle
	defer func() {
		for _, handle := range handles {
			windows.CloseHandle(handle)
		}
	}()
	for _, pid := range []int{pids.Server, pids.Child} {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, handle)
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("owner did not crash")
	}
	waited = true
	for _, handle := range handles {
		state, err := windows.WaitForSingleObject(handle, 5000)
		if err != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatal("model process survived owner crash", state, err)
		}
	}
	for _, handle := range handles {
		windows.CloseHandle(handle)
	}
	handles = nil
	restarted, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if active, err := restarted.group.Active(); err != nil || active != 0 {
		t.Fatal("restart not idle", active, err)
	}
}
