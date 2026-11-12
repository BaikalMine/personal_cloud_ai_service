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
	"testing"
	"time"
)

func TestDatabaseWriterPrepareBeforeInput(t *testing.T) {
	directory := t.TempDir()
	body := bytes.Repeat([]byte{0, 17, 127, 255}, 1<<18)
	input := databaseWriterTestInput(t, directory, body)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var recorded databaseProcessIdentity
	calls := 0
	err := runPreparedDatabaseWriter(ctx, os.Args[0], []string{"-test.run=^TestDatabaseWriterHelper$"},
		databaseWriterTestEnvironment(directory, "barrier"), directory, input, func(ctx context.Context, identity databaseProcessIdentity) error {
			calls++
			if !identity.valid() || identity.PID != databaseWriterTestPID(t, directory) {
				t.Error("identity does not identify the started child")
				return errors.New("invalid child identity")
			}
			if offset, err := input.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
				t.Errorf("archive read before preparation: %d, %v", offset, err)
			}
			if _, err := os.Stat(filepath.Join(directory, "first-byte")); !os.IsNotExist(err) {
				t.Errorf("child received input before preparation: %v", err)
			}
			raw, err := json.Marshal(identity)
			if err != nil || json.Unmarshal(raw, &recorded) != nil || recorded != identity {
				return errors.New("identity serialization failed")
			}
			if exited, err := databaseProcessExited(recorded); err != nil || exited {
				t.Errorf("live child reported exited: %v, %v", exited, err)
			}
			// A different start time represents a reused PID, not this child.
			previous := recorded
			previous.Started++
			if exited, err := databaseProcessExited(previous); err != nil || !exited {
				t.Errorf("different process creation time not distinguished: %v, %v", exited, err)
			}
			if exited, err := databaseProcessExited(recorded); err != nil || exited {
				t.Errorf("read-only observation changed child: %v, %v", exited, err)
			}
			return ctx.Err()
		})
	if err != nil || calls != 1 {
		t.Fatalf("prepared copy failed: %v, calls=%d", err, calls)
	}
	copied, err := os.ReadFile(filepath.Join(directory, "copied"))
	if err != nil || !bytes.Equal(copied, body) {
		t.Fatalf("prepared copy changed archive: %v", err)
	}
	if exited, err := databaseProcessExited(recorded); err != nil || !exited {
		t.Fatalf("reaped child not recognized: %v, %v", exited, err)
	}
}

func TestDatabaseWriterPrepareFailure(t *testing.T) {
	for _, mode := range []string{"reject", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			input := databaseWriterTestInput(t, directory, bytes.Repeat([]byte("x"), 1<<20))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var recorded databaseProcessIdentity
			err := runPreparedDatabaseWriter(ctx, os.Args[0], []string{"-test.run=^TestDatabaseWriterHelper$"},
				databaseWriterTestEnvironment(directory, "barrier"), directory, input, func(_ context.Context, identity databaseProcessIdentity) error {
					recorded = identity
					if identity.PID != databaseWriterTestPID(t, directory) {
						t.Error("wrong child")
					}
					if mode == "cancel" {
						cancel()
						return nil
					}
					return errors.New("secret database connection metadata")
				})
			if err != errDatabaseWriter {
				t.Fatalf("preparation failure not sanitized: %v", err)
			}
			if offset, err := input.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
				t.Fatalf("failed preparation advanced archive: %d, %v", offset, err)
			}
			if _, err := os.Stat(filepath.Join(directory, "first-byte")); !os.IsNotExist(err) {
				t.Fatalf("failed preparation delivered input: %v", err)
			}
			if exited, err := databaseProcessExited(recorded); err != nil || !exited {
				t.Fatalf("failed preparation left child running: %v, %v", exited, err)
			}
		})
	}
}

func TestDatabaseWriterPreparedParentDeath(t *testing.T) {
	directory := t.TempDir()
	databaseWriterTestInput(t, directory, bytes.Repeat([]byte("x"), 1<<20))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	owner := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDatabaseWriterHelper$")
	owner.Env = databaseWriterTestEnvironment(directory, "prepared-owner")
	configureReleaseCommand(owner)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill(); _ = owner.Wait() })
	pid := databaseWriterTestPID(t, directory)
	var identity databaseProcessIdentity
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(directory, "identity"))
		if err == nil && json.Unmarshal(raw, &identity) == nil && identity.valid() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if identity.PID != pid {
		t.Fatal("missing prepared child identity")
	}
	if exited, err := databaseProcessExited(identity); err != nil || exited {
		t.Fatalf("prepared child not alive: %v, %v", exited, err)
	}
	watch := databaseWriterTestWatch(t, pid)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	watch()
	if exited, err := databaseProcessExited(identity); err != nil || !exited {
		t.Fatalf("orphan exit not proven: %v, %v", exited, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "first-byte")); !os.IsNotExist(err) {
		t.Fatalf("parent death opened archive barrier: %v", err)
	}
}
