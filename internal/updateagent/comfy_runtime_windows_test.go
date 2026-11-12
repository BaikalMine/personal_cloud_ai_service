//go:build windows

package updateagent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	if os.Getenv("GATEWAY_RUNTIME_FIXTURE") == "1" && len(os.Args) > 1 && strings.HasSuffix(os.Args[1], "main.py") {
		fmt.Println("runtime fixture started")
		if len(os.Args) > 2 && os.Args[2] == "crash" {
			time.Sleep(300 * time.Millisecond)
			os.Exit(7)
		}
		if len(os.Args) > 2 && os.Args[2] == "parent" {
			child := exec.Command(os.Args[0], os.Args[1], "child")
			if child.Start() != nil {
				os.Exit(8)
			}
			_ = os.WriteFile(filepath.Join(filepath.Dir(os.Args[1]), "child.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0600)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	os.Exit(m.Run())
}

func nativeRuntimeFixture(t *testing.T, mode string) ComfyTarget {
	t.Helper()
	t.Setenv("GATEWAY_RUNTIME_FIXTURE", "1")
	dir := t.TempDir()
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	python := filepath.Join(dir, "python.exe")
	target, err := os.Create(python)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(target, source)
	target.Close()
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.py")
	if err := os.WriteFile(main, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return ComfyTarget{WorkingDirectory: dir, PythonExecutable: python, LaunchArguments: []string{main, mode}}
}

func TestNativeComfyRuntimeIdentityTreeAndExit(t *testing.T) {
	target := nativeRuntimeFixture(t, "parent")
	b := nativeComfyRuntimeBackend{}
	exited := make(chan uint32, 2)
	if err := b.Start(target, target.WorkingDirectory, func(_ comfyProcess, code uint32) { exited <- code }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		values, _ := b.Inspect(context.Background(), target)
		for _, p := range values {
			_ = b.Stop(context.Background(), target, p)
		}
	})
	var processes []comfyProcess
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		processes, err = b.Inspect(context.Background(), target)
		if err == nil && len(processes) == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(processes) != 2 {
		t.Fatalf("expected parent and child, got %v", processes)
	}
	childData, err := os.ReadFile(filepath.Join(target.WorkingDirectory, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	childID, _ := strconv.Atoi(string(childData))
	var parent, child comfyProcess
	for _, process := range processes {
		if process.PID == childID {
			child = process
		} else {
			parent = process
		}
	}
	t.Cleanup(func() { _ = b.Stop(context.Background(), target, parent) })
	stale := parent
	stale.Created++
	if b.Stop(context.Background(), target, stale) == nil {
		t.Fatal("stale identity accepted")
	}
	watch := make(chan uint32, 1)
	if err := b.Watch(parent, func(code uint32) { watch <- code }); err != nil {
		t.Fatal(err)
	}
	if err := b.Stop(context.Background(), target, parent); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watch:
	case <-time.After(5 * time.Second):
		t.Fatal("exit observation missing")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("managed exit missing")
	}
	if h, err := runtimeProcessHandle(child, 0); err == nil {
		// A lingering handle can keep the terminated process object alive; inspect
		// its exit state rather than treating a valid handle as a running child.
		defer windows.CloseHandle(h)
		state, err := windows.WaitForSingleObject(h, 3000)
		if err != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatal("child still running", err)
		}
	}
}

func TestNativeComfyRuntimeCapturesCrash(t *testing.T) {
	target := nativeRuntimeFixture(t, "crash")
	exited := make(chan uint32, 1)
	b := nativeComfyRuntimeBackend{}
	if err := b.Start(target, target.WorkingDirectory, func(_ comfyProcess, code uint32) { exited <- code }); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exited:
		if code != 7 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("crash missing")
	}
	data, err := os.ReadFile(filepath.Join(target.WorkingDirectory, "console.log"))
	if err != nil || !strings.Contains(string(data), "runtime fixture started") {
		t.Fatal("console capture missing", err)
	}
}
