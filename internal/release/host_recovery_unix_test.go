//go:build !windows

package release

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"ai-access-gateway/internal/database"
)

func hideHostRecoveryProcess(_ *exec.Cmd) {}

func TestHostRecoveryUnixPermissions(t *testing.T) {
	directory, j := recoveryTestJournal(t)
	binding := hostRecoveryTestBinding()
	if err := j.PrepareRuntimeRecovery(binding); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(directory, j.recoveryName(binding)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error { t.Fatal("broad permission callback"); return nil }); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal(err)
	}
}

func TestHostRecoveryUnixPartialWriteHelper(t *testing.T) {
	if os.Getenv("RELEASE_HOST_RECOVERY_PARTIAL_HELPER") != "1" {
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
	binding := hostRecoveryTestBinding()
	if _, err := j.Intent(HostPrepareRuntimeRecovery, j.recoveryTarget(binding)); err != nil {
		t.Fatal(err)
	}
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	limited := original
	limited.Cur = 100
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	defer syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original)
	if _, err := j.createRuntimeRecovery(binding); !errors.Is(err, ErrHostJournalIO) {
		t.Fatal("actual partial secret write was not uncertain", err)
	}
	if err := j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error { t.Fatal("poisoned callback"); return nil }); !errors.Is(err, ErrHostJournalClosed) {
		t.Fatal(err)
	}
}

func TestHostRecoveryUnixPartialWrite(t *testing.T) {
	directory, j := hostTestJournal(t)
	j.Close()
	child := hostChild(t, directory, false)
	child.Args[1] = "-test.run=^TestHostRecoveryUnixPartialWriteHelper$"
	child.Env = append(child.Env, "RELEASE_HOST_RECOVERY_PARTIAL_HELPER=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("partial-write child: %v %s", err, output)
	}
	j, err := OpenHostJournal(directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	binding := hostRecoveryTestBinding()
	if _, err := j.ReconcileRuntimeRecovery(binding); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal("partial secret reconciled", err)
	}
	state, err := j.Snapshot()
	if err != nil || state.Pending.Effect != HostPrepareRuntimeRecovery {
		t.Fatal("partial secret cleared journal intent")
	}
	file, err := os.Stat(filepath.Join(directory, j.recoveryName(binding)))
	if err != nil || file.Size() != 100 {
		t.Fatal("partial secret bytes not retained")
	}
}
