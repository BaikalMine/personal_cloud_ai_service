package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const hostTestInstallation = "1234567890abcdef1234567890abcdef"

func hostTestSpec() HostOperationSpec {
	return HostOperationSpec{ID: strings.Repeat("1", 32), PreviousManifestSHA256: strings.Repeat("a", 64),
		CandidateManifestSHA256: strings.Repeat("b", 64), PlanSHA256: strings.Repeat("c", 64)}
}

func hostTestJournal(t *testing.T) (string, *HostJournal) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "coordinator")
	if err := InitializeHostJournal(directory, hostTestInstallation); err != nil {
		t.Fatal(err)
	}
	j, err := OpenHostJournal(directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return directory, j
}

func TestHostJournalLifecycle(t *testing.T) {
	directory, j := hostTestJournal(t)
	if err := InitializeHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalIdentity) {
		t.Fatal(err)
	}
	if other, err := OpenHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalBusy) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("second owner: %v", err)
	}
	if err := j.Begin(hostTestSpec()); err != nil {
		t.Fatal(err)
	}
	intent, err := j.Intent(HostFence, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Observe(intent.Sequence, HostUnknown, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Intent(HostStopService, strings.Repeat("d", 64)); !errors.Is(err, ErrHostJournalState) {
		t.Fatal(err)
	}
	if err := j.Finish(strings.Repeat("e", 64)); !errors.Is(err, ErrHostJournalState) {
		t.Fatal(err)
	}
	before, err := j.Snapshot()
	if err != nil || before.OwnerEpoch != 1 || before.Pending != intent {
		t.Fatalf("snapshot: %+v %v", before, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin(hostTestSpec()); !errors.Is(err, ErrHostJournalClosed) {
		t.Fatal(err)
	}
	j, err = OpenHostJournal(directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	after, err := j.Snapshot()
	if err != nil || after.OwnerEpoch != 2 || after.Pending != intent || after.Operation != before.Operation {
		t.Fatalf("recovery: %+v %v", after, err)
	}
	if err := j.Observe(intent.Sequence+1, HostApplied, strings.Repeat("e", 64)); !errors.Is(err, ErrHostJournalState) {
		t.Fatal(err)
	}
	if err := j.Observe(intent.Sequence, HostApplied, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if err := j.Observe(intent.Sequence, HostApplied, strings.Repeat("e", 64)); !errors.Is(err, ErrHostJournalState) {
		t.Fatal(err)
	}
	if err := j.Finish(strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin(hostTestSpec()); !errors.Is(err, ErrHostJournalState) {
		t.Fatal(err)
	}
	spec := hostTestSpec()
	spec.ID = strings.Repeat("2", 32)
	if err := j.Begin(spec); err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin(hostTestSpec()); !errors.Is(err, ErrHostJournalState) {
		t.Fatalf("historic ID reused: %v", err)
	}
}

func TestHostJournalInvalidTransitions(t *testing.T) {
	_, j := hostTestJournal(t)
	unchanged := func(call func() error) {
		t.Helper()
		before, _ := j.Snapshot()
		if err := call(); err == nil {
			t.Fatal("invalid transition accepted")
		}
		after, err := j.Snapshot()
		if err != nil || before != after {
			t.Fatalf("refusal changed journal: %v", err)
		}
	}
	unchanged(func() error { _, err := j.Intent(HostFence, strings.Repeat("d", 64)); return err })
	unchanged(func() error { return j.Finish(strings.Repeat("e", 64)) })
	for _, change := range []func(*HostOperationSpec){
		func(s *HostOperationSpec) { s.ID = "secret-session-cookie" },
		func(s *HostOperationSpec) { s.PlanSHA256 = strings.Repeat("0", 64) },
		func(s *HostOperationSpec) { s.PreviousManifestSHA256 = "wrong" },
		func(s *HostOperationSpec) { s.CandidateManifestSHA256 = strings.Repeat("A", 64) },
	} {
		spec := hostTestSpec()
		change(&spec)
		unchanged(func() error { return j.Begin(spec) })
	}
	if err := j.Begin(hostTestSpec()); err != nil {
		t.Fatal(err)
	}
	unchanged(func() error { return j.Begin(hostTestSpec()) })
	unchanged(func() error { _, err := j.Intent("docker rm --force unrelated", strings.Repeat("d", 64)); return err })
	unchanged(func() error { _, err := j.Intent(HostStopService, "private-database-url"); return err })
	i, err := j.Intent(HostStopService, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	unchanged(func() error { return j.Observe(i.Sequence, "failed", strings.Repeat("e", 64)) })
	unchanged(func() error { return j.Observe(i.Sequence, HostNotApplied, "raw private error") })
	if err := j.Observe(i.Sequence, HostNotApplied, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Intent(HostStopService, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestHostJournalCorruptionAndIdentity(t *testing.T) {
	for _, mode := range []string{"tail", "byte", "extra_field", "duplicate_field", "reorder", "blank", "copy", "file_replace", "wrong_installation", "directory_replace", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			directory, j := hostTestJournal(t)
			if err := j.Begin(hostTestSpec()); err != nil {
				t.Fatal(err)
			}
			j.Close()
			name := filepath.Join(directory, hostJournalName)
			original, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			payload := append([]byte(nil), original...)
			switch mode {
			case "tail":
				payload = payload[:len(payload)-5]
			case "byte":
				payload[20] ^= 1
			case "extra_field":
				payload = bytes.Replace(payload, []byte(`{"record":`), []byte(`{"unknown":true,"record":`), 1)
			case "duplicate_field":
				payload = bytes.Replace(payload, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
			case "reorder":
				lines := bytes.Split(payload, []byte{'\n'})
				lines[1], lines[2] = lines[2], lines[1]
				payload = bytes.Join(lines, []byte{'\n'})
			case "blank":
				payload = append(payload, '\n')
			case "copy":
				other := filepath.Join(t.TempDir(), "copied")
				if err := createPrivateHostDirectory(other); err != nil {
					t.Fatal(err)
				}
				directory, name = other, filepath.Join(other, hostJournalName)
			case "file_replace":
				if err := os.Rename(name, name+".old"); err != nil {
					t.Fatal(err)
				}
			case "directory_replace":
				if err := os.Rename(directory, directory+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := createPrivateHostDirectory(directory); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(name, filepath.Join(directory, "alias")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(name, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			id := hostTestInstallation
			if mode == "wrong_installation" {
				id = strings.Repeat("9", 32)
			}
			other, err := OpenHostJournal(directory, id)
			if err == nil {
				other.Close()
				t.Fatal("invalid journal accepted")
			}
			if strings.Contains(err.Error(), directory) || strings.Contains(err.Error(), id) {
				t.Fatal("private identity leaked")
			}
			actual, readErr := os.ReadFile(name)
			if readErr != nil || !bytes.Equal(actual, payload) {
				t.Fatal("refused journal was modified")
			}
		})
	}
}

func TestHostJournalLiveTampering(t *testing.T) {
	_, j := hostTestJournal(t)
	// Use the owning handle so Windows byte-range locking does not itself refuse
	// the injected corruption. Production never exposes this handle.
	if _, err := j.file.WriteAt([]byte("!"), 20); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin(hostTestSpec()); err == nil {
		t.Fatal("live corruption accepted")
	}
}

func TestHostJournalConcurrentIntent(t *testing.T) {
	_, j := hostTestJournal(t)
	if err := j.Begin(hostTestSpec()); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		group.Add(1)
		go func() { defer group.Done(); _, err := j.Intent(HostFence, strings.Repeat("d", 64)); results <- err }()
	}
	group.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrHostJournalState) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("%d intents accepted", success)
	}
}

func TestHostJournalProcessHelper(t *testing.T) {
	if os.Getenv("RELEASE_HOST_JOURNAL_HELPER") != "1" {
		return
	}
	directory := os.Getenv("RELEASE_HOST_JOURNAL_DIRECTORY")
	j, err := OpenHostJournal(directory, hostTestInstallation)
	if os.Getenv("RELEASE_HOST_JOURNAL_BUSY") == "1" {
		if !errors.Is(err, ErrHostJournalBusy) {
			if j != nil {
				j.Close()
			}
			t.Fatalf("expected busy: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.Begin(hostTestSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Intent(HostStopService, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "ready"), []byte("intent-flushed"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func hostChild(t *testing.T, directory string, busy bool) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestHostJournalProcessHelper$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "RELEASE_HOST_JOURNAL_HELPER=1", "RELEASE_HOST_JOURNAL_DIRECTORY="+directory,
		fmt.Sprintf("RELEASE_HOST_JOURNAL_BUSY=%d", map[bool]int{false: 0, true: 1}[busy]))
	return cmd
}

func TestHostJournalProcessCrash(t *testing.T) {
	directory, j := hostTestJournal(t)
	busy := hostChild(t, directory, true)
	if output, err := busy.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock: %v %s", err, output)
	}
	j.Close()
	child := hostChild(t, directory, false)
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			child.Process.Kill()
			child.Wait()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(directory, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not flush intent")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if other, err := OpenHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalBusy) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("live child lock: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	waited = true
	recovered, err := OpenHostJournal(directory, hostTestInstallation)
	if err != nil {
		t.Fatalf("recover after actual process death: %v %s", err, output.String())
	}
	defer recovered.Close()
	snapshot, err := recovered.Snapshot()
	if err != nil || snapshot.OwnerEpoch != 3 || snapshot.Pending.Effect != HostStopService || snapshot.Pending.OwnerEpoch != 2 {
		t.Fatalf("lost pending intent: %+v %v", snapshot, err)
	}
	if _, err := recovered.Intent(HostStopService, strings.Repeat("d", 64)); !errors.Is(err, ErrHostJournalState) {
		t.Fatalf("blind retry: %v", err)
	}
	if err := recovered.Observe(snapshot.Pending.Sequence, HostNotApplied, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Intent(HostStopService, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestHostJournalPublicShape(t *testing.T) {
	directory, j := hostTestJournal(t)
	if err := j.Begin(hostTestSpec()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := j.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), directory) || strings.Contains(string(payload), "token") || strings.Contains(string(payload), "deployment_authorized\":true") {
		t.Fatal("private/authorization data in snapshot")
	}
}

func TestHostJournalActivationHistory(t *testing.T) {
	directory, j := hostTestJournal(t)
	if err := j.Begin(hostTestSpec()); err != nil {
		t.Fatal(err)
	}
	activation, err := j.Intent(HostActivate, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Observe(activation.Sequence, HostApplied, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	recovery, err := j.Intent(HostFenceRecovery, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	j, err = OpenHostJournal(directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	snapshot, events, err := j.OperationHistory()
	if err != nil || snapshot.Pending != recovery || len(events) != 5 {
		t.Fatalf("history: %+v %+v %v", snapshot, events, err)
	}
	if events[1].Effect != HostActivate || events[2].IntentSequence != activation.Sequence || events[2].Observation != HostApplied ||
		events[3].Effect != HostFenceRecovery || events[4].Kind != "owner" || events[4].OwnerEpoch != 2 {
		t.Fatalf("activation evidence lost: %+v", events)
	}
	events[2].Observation = HostNotApplied
	_, unchanged, err := j.OperationHistory()
	if err != nil || unchanged[2].Observation != HostApplied {
		t.Fatal("history was mutable through caller")
	}
	if err := j.Observe(recovery.Sequence, HostApplied, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	spec := hostTestSpec()
	spec.ID = strings.Repeat("3", 32)
	if err := j.Begin(spec); err != nil {
		t.Fatal(err)
	}
	snapshot, events, err = j.OperationHistory()
	if err != nil || snapshot.Operation != spec || len(events) != 1 || events[0].Kind != "begin" {
		t.Fatalf("new operation history mixed: %+v %v", events, err)
	}
}

func TestHostJournalInitializationBoundary(t *testing.T) {
	for _, directory := range []string{"relative", "", string(filepath.Separator)} {
		if err := InitializeHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalIdentity) {
			t.Fatalf("path %q: %v", directory, err)
		}
	}
	directory := filepath.Join(t.TempDir(), "not-created")
	if other, err := OpenHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalIdentity) {
		if other != nil {
			other.Close()
		}
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("open created missing directory")
	}
	if err := InitializeHostJournal(directory, "private-invalid-installation"); !errors.Is(err, ErrHostJournalIdentity) {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("invalid ID created directory")
	}
	if err := createPrivateHostDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if err := InitializeHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalIdentity) {
		t.Fatal("existing directory adopted", err)
	}
	if err := os.WriteFile(filepath.Join(directory, hostJournalName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if other, err := OpenHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalCorrupt) {
		if other != nil {
			other.Close()
		}
		t.Fatal("empty journal adopted", err)
	}
}
