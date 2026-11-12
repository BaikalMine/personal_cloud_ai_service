//go:build linux

package release

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDatabaseWriterRejectsSetIDExecutable(t *testing.T) {
	directory := t.TempDir()
	input := databaseWriterTestInput(t, directory, []byte("unused"))
	executable := filepath.Join(directory, "untrusted-client")
	body, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, body, 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{os.ModeSetuid, os.ModeSetgid} {
		if err := os.Chmod(executable, 0700|mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(executable)
		if err != nil || info.Mode()&mode == 0 {
			t.Fatal("test filesystem did not retain set-ID bit", err)
		}
		process, err := startDatabaseWriter(executable, []string{"-test.run=^TestDatabaseWriterHelper$"},
			databaseWriterTestEnvironment(directory, "fail"), directory, input, input)
		if process != nil || err == nil {
			if process != nil {
				_ = process.terminate()
				_ = process.wait()
				process.close()
			}
			t.Fatal("accepted credential-changing executable", err)
		}
	}
}

func databaseWriterTestWatch(t *testing.T, pid int) func() {
	t.Helper()
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	return func() {
		t.Helper()
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(poll, 5000)
		if err != nil || n != 1 || poll[0].Revents&unix.POLLIN == 0 {
			t.Fatalf("writer still alive after parent termination: %v %+v", err, poll)
		}
	}
}
