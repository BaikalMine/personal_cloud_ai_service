//go:build !windows

package release

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestHostJournalUnixPermissions(t *testing.T) {
	for _, target := range []string{"directory", "file"} {
		t.Run(target, func(t *testing.T) {
			directory, j := hostTestJournal(t)
			name, mode := directory, os.FileMode(0o750)
			if target == "file" {
				name, mode = filepath.Join(directory, hostJournalName), 0o640
			}
			if err := os.Chmod(name, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := j.Snapshot(); !errors.Is(err, ErrHostJournalIdentity) {
				t.Fatalf("live permissions accepted: %v", err)
			}
			j.Close()
			other, err := OpenHostJournal(directory, hostTestInstallation)
			if !errors.Is(err, ErrHostJournalIdentity) {
				if other != nil {
					other.Close()
				}
				t.Fatalf("broad permissions accepted: %v", err)
			}
		})
	}
}

func TestHostJournalUnixSymlink(t *testing.T) {
	directory, j := hostTestJournal(t)
	j.Close()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	if other, err := OpenHostJournal(alias, hostTestInstallation); !errors.Is(err, ErrHostJournalIdentity) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("directory symlink accepted: %v", err)
	}
	name := filepath.Join(directory, hostJournalName)
	if err := os.Rename(name, name+".actual"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(name+".actual", name); err != nil {
		t.Fatal(err)
	}
	if other, err := OpenHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalIdentity) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("file symlink accepted: %v", err)
	}
}

func TestHostJournalUnixLiveReplacement(t *testing.T) {
	directory, j := hostTestJournal(t)
	if err := os.Rename(directory, directory+".old"); err != nil {
		t.Fatal(err)
	}
	if err := InitializeHostJournal(directory, hostTestInstallation); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin(hostTestSpec()); !errors.Is(err, ErrHostJournalIdentity) {
		t.Fatalf("detached owner accepted: %v", err)
	}
	// A new root is a new identity, never a continuation of the moved journal.
	j.Close()
}

func TestHostJournalUnixPartialWriteHelper(t *testing.T) {
	if os.Getenv("RELEASE_HOST_JOURNAL_PARTIAL_HELPER") != "1" {
		return
	}
	j, err := OpenHostJournal(os.Getenv("RELEASE_HOST_JOURNAL_DIRECTORY"), hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.Begin(hostTestSpec()); err != nil {
		t.Fatal(err)
	}
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	limited := original
	limited.Cur = uint64(j.length + 100)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	defer syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original)
	if _, err := j.Intent(HostStopService, strings.Repeat("d", 64)); !errors.Is(err, ErrHostJournalIO) {
		t.Fatalf("partial write: %v", err)
	}
	if _, err := j.Intent(HostStopService, strings.Repeat("d", 64)); !errors.Is(err, ErrHostJournalClosed) {
		t.Fatalf("poisoned owner reused: %v", err)
	}
	info, err := j.file.Stat()
	if err != nil || info.Size() != int64(limited.Cur) {
		t.Fatalf("partial bytes were not retained: %v", err)
	}
}

func TestHostJournalUnixPartialWrite(t *testing.T) {
	directory, j := hostTestJournal(t)
	j.Close()
	child := hostChild(t, directory, false)
	child.Args[1] = "-test.run=^TestHostJournalUnixPartialWriteHelper$"
	child.Env = append(child.Env, "RELEASE_HOST_JOURNAL_PARTIAL_HELPER=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("actual partial write: %v %s", err, output)
	}
	name := filepath.Join(directory, hostJournalName)
	before, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := OpenHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalCorrupt) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("partial write adopted: %v", err)
	}
	after, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("damaged tail was changed")
	}
}
