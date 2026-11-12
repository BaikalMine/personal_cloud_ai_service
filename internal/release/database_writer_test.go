//go:build linux || windows

package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDatabaseWriterHelper(t *testing.T) {
	mode := os.Getenv("RELEASE_WRITER_HELPER")
	if mode == "" {
		return
	}
	directory := os.Getenv("RELEASE_WRITER_DIRECTORY")
	if mode == "prepared-owner" {
		input, err := os.Open(filepath.Join(directory, "input"))
		if err != nil {
			os.Exit(50)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err = runPreparedDatabaseWriter(ctx, os.Args[0], []string{"-test.run=^TestDatabaseWriterHelper$"},
			databaseWriterTestEnvironment(directory, "barrier"), directory, input, func(ctx context.Context, identity databaseProcessIdentity) error {
				body, err := json.Marshal(identity)
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(directory, "identity"), body, 0600); err != nil {
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			})
		if err != nil {
			os.Exit(51)
		}
		os.Exit(52)
	}
	if mode == "owner" {
		input, err := os.Open(filepath.Join(directory, "input"))
		if err != nil {
			os.Exit(40)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err = runDatabaseWriter(ctx, os.Args[0], []string{"-test.run=^TestDatabaseWriterHelper$"},
			databaseWriterTestEnvironment(directory, "block"), directory, input)
		if err != nil {
			os.Exit(41)
		}
		os.Exit(0)
	}
	if mode == "block" {
		if os.WriteFile(filepath.Join(directory, "child-pid"), []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
			os.Exit(42)
		}
		time.Sleep(30 * time.Second)
		_ = os.WriteFile(filepath.Join(directory, "late-write"), []byte("unexpected"), 0600)
		os.Exit(43)
	}
	if mode == "fail" {
		os.Exit(44)
	}
	if mode != "copy" && mode != "barrier" {
		os.Exit(45)
	}
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		os.Exit(46)
	}
	var prefix []byte
	if mode == "barrier" {
		if os.WriteFile(filepath.Join(directory, "child-pid"), []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
			os.Exit(53)
		}
		prefix = make([]byte, 1)
		if _, err := io.ReadFull(os.Stdin, prefix); err != nil {
			os.Exit(54)
		}
		if os.WriteFile(filepath.Join(directory, "first-byte"), prefix, 0600) != nil {
			os.Exit(55)
		}
	}
	// These diagnostics must never block the writer or be returned to callers.
	_, _ = io.CopyN(os.Stdout, strings.NewReader(strings.Repeat("secret", 1<<18)), 1<<20)
	_, _ = io.CopyN(os.Stderr, strings.NewReader(strings.Repeat("secret", 1<<18)), 1<<20)
	body, err := io.ReadAll(os.Stdin)
	if err != nil || os.WriteFile(filepath.Join(directory, "copied"), append(prefix, body...), 0600) != nil {
		os.Exit(47)
	}
	observed, _ := json.Marshal(struct {
		Args    []string
		Ambient string
		CWD     string
	}{os.Args[1:], os.Getenv("RELEASE_WRITER_AMBIENT_SECRET"), mustWriterCWD()})
	if os.WriteFile(filepath.Join(directory, "observed"), observed, 0600) != nil {
		os.Exit(48)
	}
	os.Exit(0)
}

func mustWriterCWD() string {
	value, _ := os.Getwd()
	return value
}

func databaseWriterTestEnvironment(directory, mode string) []string {
	values := []string{"RELEASE_WRITER_HELPER=" + mode, "RELEASE_WRITER_DIRECTORY=" + directory, "GORACE=atexit_sleep_ms=0"}
	if runtime.GOOS == "windows" {
		values = append(values, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	return values
}

func databaseWriterTestInput(t *testing.T, directory string, body []byte) *os.File {
	t.Helper()
	path := filepath.Join(directory, "input")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func TestDatabaseWriterCopyAndIsolation(t *testing.T) {
	t.Setenv("RELEASE_WRITER_AMBIENT_SECRET", "must-not-inherit")
	directory := t.TempDir()
	body := bytes.Repeat([]byte{0, 17, 127, 255}, 1<<20)
	input := databaseWriterTestInput(t, directory, body)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{"-test.run=^TestDatabaseWriterHelper$", "--", "space value", `quote"value`, `slash\`, ""}
	if err := runDatabaseWriter(ctx, os.Args[0], args, databaseWriterTestEnvironment(directory, "copy"), directory, input); err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(directory, "copied"))
	if err != nil || !bytes.Equal(copied, body) {
		t.Fatalf("input not delivered exactly: %v (%d bytes)", err, len(copied))
	}
	var observed struct {
		Args    []string
		Ambient string
		CWD     string
	}
	raw, err := os.ReadFile(filepath.Join(directory, "observed"))
	if err != nil || json.Unmarshal(raw, &observed) != nil {
		t.Fatalf("missing child observation: %v", err)
	}
	want, _ := json.Marshal(args)
	got, _ := json.Marshal(observed.Args)
	if !bytes.Equal(want, got) || observed.Ambient != "" || observed.CWD != directory {
		t.Fatalf("unexpected child arguments/environment/directory: %s", raw)
	}
}

func TestDatabaseWriterEarlyExitAndValidation(t *testing.T) {
	directory := t.TempDir()
	input := databaseWriterTestInput(t, directory, bytes.Repeat([]byte("x"), 1<<20))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env := databaseWriterTestEnvironment(directory, "fail")
	for _, test := range []struct {
		name string
		ctx  context.Context
		path string
		env  []string
	}{
		{"failure", ctx, os.Args[0], env},
		{"not-found", ctx, filepath.Join(directory, "missing-client"), env},
		{"no-deadline", context.Background(), os.Args[0], env},
		{"relative-executable", ctx, "client", env},
		{"inherited-environment", ctx, os.Args[0], nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := input.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			if err := runDatabaseWriter(test.ctx, test.path, []string{"-test.run=^TestDatabaseWriterHelper$"}, test.env, directory, input); !errors.Is(err, errDatabaseWriter) {
				t.Fatalf("failure not sanitized: %v", err)
			}
		})
	}
}

func databaseWriterTestPID(t *testing.T, directory string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(filepath.Join(directory, "child-pid"))
		if err == nil {
			pid, err := strconv.Atoi(string(body))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child did not start")
	return 0
}

func TestDatabaseWriterCancellationReapsChild(t *testing.T) {
	directory := t.TempDir()
	input := databaseWriterTestInput(t, directory, bytes.Repeat([]byte("x"), 1<<20))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runDatabaseWriter(ctx, os.Args[0], []string{"-test.run=^TestDatabaseWriterHelper$"}, databaseWriterTestEnvironment(directory, "block"), directory, input)
	}()
	pid := databaseWriterTestPID(t, directory)
	watch := databaseWriterTestWatch(t, pid)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errDatabaseWriter) {
			t.Fatalf("cancelled writer succeeded: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled writer did not return")
	}
	watch()
	if _, err := os.Stat(filepath.Join(directory, "late-write")); !os.IsNotExist(err) {
		t.Fatalf("child survived cancellation: %v", err)
	}
}

func TestDatabaseWriterParentDeath(t *testing.T) {
	directory := t.TempDir()
	databaseWriterTestInput(t, directory, bytes.Repeat([]byte("x"), 1<<20))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	owner := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDatabaseWriterHelper$")
	owner.Env = databaseWriterTestEnvironment(directory, "owner")
	configureReleaseCommand(owner)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill(); _ = owner.Wait() })
	pid := databaseWriterTestPID(t, directory)
	watch := databaseWriterTestWatch(t, pid)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	watch()
	if _, err := os.Stat(filepath.Join(directory, "late-write")); !os.IsNotExist(err) {
		t.Fatalf("child survived parent death: %v", err)
	}
}
