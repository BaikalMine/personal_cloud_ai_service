package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
)

func hostRecoveryTestBinding() HostRuntimeRecoveryBinding {
	return HostRuntimeRecoveryBinding{DatabaseSHA256: strings.Repeat("d", 64), ExpectedEpoch: 0}
}

func recoveryTestJournal(t *testing.T) (string, *HostJournal) {
	t.Helper()
	directory, j := hostTestJournal(t)
	if err := j.Begin(hostTestSpec()); err != nil {
		t.Fatal(err)
	}
	return directory, j
}

func recoveryTestTokenHash(t *testing.T, j *HostJournal, binding HostRuntimeRecoveryBinding) string {
	t.Helper()
	digest := ""
	if err := j.WithRuntimeRecovery(binding, func(request database.RuntimeReleaseRequest) error {
		if !validHostHash(request.Token) || request.OperationID != hostTestSpec().ID || request.ExpectedEpoch != binding.ExpectedEpoch ||
			request.PreviousManifestHash != hostTestSpec().PreviousManifestSHA256 || request.CandidateManifestHash != hostTestSpec().CandidateManifestSHA256 {
			return errors.New("incorrect request")
		}
		digest = hostRecoveryDigest([]byte(request.Token))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestHostRecoveryLifecycle(t *testing.T) {
	directory, j := recoveryTestJournal(t)
	binding := hostRecoveryTestBinding()
	if err := j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error { t.Fatal("unprepared callback"); return nil }); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal(err)
	}
	if err := j.PrepareRuntimeRecovery(binding); err != nil {
		t.Fatal(err)
	}
	original := recoveryTestTokenHash(t, j, binding)
	_, history, err := j.OperationHistory()
	if err != nil || len(history) != 3 || history[1].Effect != HostPrepareRuntimeRecovery || history[2].Observation != HostApplied {
		t.Fatal("preparation not journalled")
	}
	before, err := os.ReadFile(filepath.Join(directory, j.recoveryName(binding)))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareRuntimeRecovery(binding); !errors.Is(err, ErrHostJournalState) {
		t.Fatal("token rotation accepted", err)
	}
	j.Close()
	j, err = OpenHostJournal(directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if recoveryTestTokenHash(t, j, binding) != original {
		t.Fatal("token changed across owner epochs")
	}
	after, err := os.ReadFile(filepath.Join(directory, j.recoveryName(binding)))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("credential was rewritten on read")
	}
	if err := j.Finish(strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if err := j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error { t.Fatal("finished callback"); return nil }); !errors.Is(err, ErrHostJournalState) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, j.recoveryName(binding))); err != nil {
		t.Fatal("finish deleted recovery material", err)
	}
}

func TestHostRecoveryBoundaryAndRedaction(t *testing.T) {
	directory, j := recoveryTestJournal(t)
	binding := hostRecoveryTestBinding()
	for _, invalid := range []HostRuntimeRecoveryBinding{{}, {DatabaseSHA256: "private-dsn", ExpectedEpoch: 0}, {DatabaseSHA256: binding.DatabaseSHA256, ExpectedEpoch: -1}} {
		before, _ := j.Snapshot()
		if err := j.PrepareRuntimeRecovery(invalid); !errors.Is(err, ErrHostJournalState) {
			t.Fatal(err)
		}
		after, err := j.Snapshot()
		if err != nil || before != after {
			t.Fatal("invalid input modified journal")
		}
	}
	if err := j.PrepareRuntimeRecovery(binding); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []HostRuntimeRecoveryBinding{{DatabaseSHA256: strings.Repeat("a", 64), ExpectedEpoch: 0}, {DatabaseSHA256: binding.DatabaseSHA256, ExpectedEpoch: 1}} {
		if err := j.WithRuntimeRecovery(wrong, func(database.RuntimeReleaseRequest) error { t.Fatal("wrong binding callback"); return nil }); !errors.Is(err, ErrHostRecoveryUnavailable) {
			t.Fatal(err)
		}
	}
	if err := j.WithRuntimeRecovery(binding, nil); !errors.Is(err, ErrHostJournalState) {
		t.Fatal(err)
	}
	intent, err := j.Intent(HostFence, binding.DatabaseSHA256)
	if err != nil {
		t.Fatal(err)
	}
	secret := ""
	err = j.WithRuntimeRecovery(binding, func(request database.RuntimeReleaseRequest) error {
		secret = request.Token
		return errors.New("private callback " + directory + " " + request.Token)
	})
	if !errors.Is(err, ErrHostRecoveryAction) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), directory) {
		t.Fatal("callback error was not redacted")
	}
	snapshot, history, err := j.OperationHistory()
	if err != nil || snapshot.Pending != intent {
		t.Fatal("callback error resolved uncertain external effect")
	}
	public, _ := json.Marshal(struct {
		Snapshot HostJournalSnapshot
		History  []HostJournalEvent
	}{snapshot, history})
	log := make([]byte, j.length)
	if _, err := j.file.ReadAt(log, 0); err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{public, log} {
		if bytes.Contains(payload, []byte(secret)) || bytes.Contains(payload, []byte(directory)) {
			t.Fatal("recovery secret or path leaked into journal/public data")
		}
	}
}

func TestHostRecoveryRetainsOwnershipDuringCallback(t *testing.T) {
	directory, j := recoveryTestJournal(t)
	binding := hostRecoveryTestBinding()
	if err := j.PrepareRuntimeRecovery(binding); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error { close(entered); <-release; return nil })
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- j.Close() }()
	select {
	case <-closed:
		close(release)
		<-done
		t.Fatal("close passed an active callback")
	case <-time.After(30 * time.Millisecond):
	}
	other, err := OpenHostJournal(directory, hostTestInstallation)
	if other != nil {
		other.Close()
	}
	close(release)
	if callbackErr := <-done; callbackErr != nil {
		t.Fatal(callbackErr)
	}
	if closeErr := <-closed; closeErr != nil {
		t.Fatal(closeErr)
	}
	if !errors.Is(err, ErrHostJournalBusy) {
		t.Fatalf("callback did not retain host lock: %v", err)
	}
}

func TestHostRecoveryChangedDuringCallback(t *testing.T) {
	directory, j := recoveryTestJournal(t)
	binding := hostRecoveryTestBinding()
	if err := j.PrepareRuntimeRecovery(binding); err != nil {
		t.Fatal(err)
	}
	intent, err := j.Intent(HostFence, binding.DatabaseSHA256)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error {
		called = true
		return os.Remove(filepath.Join(directory, j.recoveryName(binding)))
	})
	if !called || !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal("post-action recovery loss was not detected", err)
	}
	state, err := j.Snapshot()
	if err != nil || state.Pending != intent {
		t.Fatal("post-action failure resolved the external effect")
	}
}

func TestHostRecoveryInvalidFiles(t *testing.T) {
	for _, mode := range []string{"missing", "empty", "partial", "changed", "extra_field", "copy", "hardlink", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			directory, j := recoveryTestJournal(t)
			binding := hostRecoveryTestBinding()
			if err := j.PrepareRuntimeRecovery(binding); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(directory, j.recoveryName(binding))
			payload, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "empty":
				payload = nil
			case "partial":
				payload = payload[:len(payload)/2]
			case "changed":
				payload[len(payload)/2] ^= 1
			case "extra_field":
				payload = bytes.Replace(payload, []byte(`{"version":`), []byte(`{"unexpected":true,"version":`), 1)
			case "copy":
				if err := os.Rename(name, name+".old"); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(name, name+".alias"); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				payload = bytes.Repeat([]byte{'x'}, maxHostRecoveryBytes+1)
			}
			if mode != "missing" {
				if err := os.WriteFile(name, payload, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error { t.Fatal("invalid file callback"); return nil }); !errors.Is(err, ErrHostRecoveryUnavailable) {
				t.Fatal(err)
			}
			if err := j.PrepareRuntimeRecovery(binding); !errors.Is(err, ErrHostJournalState) {
				t.Fatal("lost material was regenerated", err)
			}
			if mode != "missing" {
				after, err := os.ReadFile(name)
				if err != nil || !bytes.Equal(after, payload) {
					t.Fatal("refused material modified")
				}
			}
		})
	}
}

func TestHostRecoveryReconcileRefusesPartial(t *testing.T) {
	directory, j := recoveryTestJournal(t)
	binding := hostRecoveryTestBinding()
	intent, err := j.Intent(HostPrepareRuntimeRecovery, j.recoveryTarget(binding))
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(directory, j.recoveryName(binding))
	if err := os.WriteFile(name, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.ReconcileRuntimeRecovery(binding); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal(err)
	}
	snapshot, err := j.Snapshot()
	if err != nil || snapshot.Pending != intent {
		t.Fatal("partial file cleared the intent")
	}
	if err := j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error { t.Fatal("unobserved file callback"); return nil }); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal(err)
	}
}

func TestHostRecoveryRejectsTransplantedContext(t *testing.T) {
	directory, j := recoveryTestJournal(t)
	binding := hostRecoveryTestBinding()
	if err := j.PrepareRuntimeRecovery(binding); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(filepath.Join(directory, j.recoveryName(binding)))
	if err != nil {
		t.Fatal(err)
	}
	other := HostRuntimeRecoveryBinding{DatabaseSHA256: strings.Repeat("f", 64), ExpectedEpoch: 4}
	intent, err := j.Intent(HostPrepareRuntimeRecovery, j.recoveryTarget(other))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, j.recoveryName(other)), old, 0o600); err != nil {
		t.Fatal(err)
	}
	// Even a caller-recorded matching byte hash is not sufficient: the actual
	// secret file identity, operation and database context must also match.
	if err := j.Observe(intent.Sequence, HostApplied, hostRecoveryDigest(old)); err != nil {
		t.Fatal(err)
	}
	if err := j.WithRuntimeRecovery(other, func(database.RuntimeReleaseRequest) error { t.Fatal("transplanted callback"); return nil }); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal(err)
	}
}

func TestHostRecoveryRestoreLineage(t *testing.T) {
	directory, j := recoveryTestJournal(t)
	source := hostRecoveryTestBinding()
	if err := j.PrepareRuntimeRecovery(source); err != nil {
		t.Fatal(err)
	}
	original := recoveryTestTokenHash(t, j, source)
	target := HostRuntimeRecoveryBinding{DatabaseSHA256: strings.Repeat("f", 64), ExpectedEpoch: source.ExpectedEpoch, RestoreSourceSHA256: source.DatabaseSHA256}
	for _, invalid := range []HostRuntimeRecoveryBinding{
		{DatabaseSHA256: source.DatabaseSHA256, ExpectedEpoch: 0, RestoreSourceSHA256: source.DatabaseSHA256},
		{DatabaseSHA256: target.DatabaseSHA256, ExpectedEpoch: 1, RestoreSourceSHA256: source.DatabaseSHA256},
		{DatabaseSHA256: target.DatabaseSHA256, ExpectedEpoch: 0, RestoreSourceSHA256: strings.Repeat("a", 64)},
	} {
		before, _ := j.Snapshot()
		if err := j.PrepareRuntimeRecovery(invalid); err == nil {
			t.Fatal("invalid restore lineage accepted")
		}
		after, err := j.Snapshot()
		if err != nil || before != after {
			t.Fatal("invalid lineage changed journal")
		}
	}
	if err := j.PrepareRuntimeRecovery(target); err != nil {
		t.Fatal(err)
	}
	if recoveryTestTokenHash(t, j, target) != original {
		t.Fatal("copied control record received a new token")
	}
	primary, err := os.ReadFile(filepath.Join(directory, j.recoveryName(source)))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(directory, j.recoveryName(target)))
	if err != nil || bytes.Equal(primary, restored) {
		t.Fatal("restore material was copied without rebinding")
	}
	fresh := target
	fresh.RestoreSourceSHA256 = ""
	if err := j.PrepareRuntimeRecovery(fresh); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal("conflicting credential created for same destination", err)
	}
	if err := j.WithRuntimeRecovery(fresh, func(database.RuntimeReleaseRequest) error { t.Fatal("lineage omitted"); return nil }); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, j.recoveryName(source))); err != nil {
		t.Fatal(err)
	}
	if recoveryTestTokenHash(t, j, target) != original {
		t.Fatal("prepared restore depended on later source-file availability")
	}
}

func TestHostRecoveryProcessHelper(t *testing.T) {
	mode := os.Getenv("RELEASE_HOST_RECOVERY_HELPER")
	if mode == "" {
		return
	}
	directory := os.Getenv("RELEASE_HOST_JOURNAL_DIRECTORY")
	j, err := OpenHostJournal(directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.Begin(hostTestSpec()); err != nil {
		t.Fatal(err)
	}
	binding := hostRecoveryTestBinding()
	switch mode {
	case "prepared", "database":
		if err := j.PrepareRuntimeRecovery(binding); err != nil {
			t.Fatal(err)
		}
	case "before_observe", "before_file":
		if _, err := j.Intent(HostPrepareRuntimeRecovery, j.recoveryTarget(binding)); err != nil {
			t.Fatal(err)
		}
		if mode == "before_observe" {
			if _, err := j.createRuntimeRecovery(binding); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatal("invalid helper mode")
	}
	if mode == "database" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		db, err := database.Open(ctx, os.Getenv("RELEASE_HOST_RECOVERY_DATABASE"))
		if err != nil {
			t.Fatal("fixture DB open failed")
		}
		defer db.Close()
		if _, err := j.Intent(HostFence, binding.DatabaseSHA256); err != nil {
			t.Fatal(err)
		}
		var controller *database.RuntimeRelease
		if err := j.WithRuntimeRecovery(binding, func(request database.RuntimeReleaseRequest) error {
			var err error
			controller, err = database.BeginRuntimeRelease(ctx, db, request)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		defer controller.Close()
	}
	digest := "absent"
	if mode != "before_file" {
		request, _, err := j.readRuntimeRecovery(binding, "")
		if err != nil {
			t.Fatal(err)
		}
		digest = hostRecoveryDigest([]byte(request.Token))
	}
	if err := os.WriteFile(filepath.Join(directory, "recovery-ready"), []byte(digest), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func hostRecoveryCrashChild(t *testing.T, directory, mode, dsn string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestHostRecoveryProcessHelper$", "-test.timeout=30s")
	hideHostRecoveryProcess(child)
	child.Env = append(os.Environ(), "RELEASE_HOST_JOURNAL_DIRECTORY="+directory, "RELEASE_HOST_RECOVERY_HELPER="+mode,
		"RELEASE_HOST_RECOVERY_DATABASE="+dsn)
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
	var ready []byte
	deadline := time.Now().Add(12 * time.Second)
	for {
		ready, err = os.ReadFile(filepath.Join(directory, "recovery-ready"))
		if err == nil && (string(ready) == "absent" || validHostHash(string(ready))) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery child did not reach the requested crash point")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if other, err := OpenHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalBusy) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("live recovery owner not exclusive: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	waited = true
	if strings.Contains(output.String(), "runtime recovery action failed") {
		t.Fatal("child recovery action failed")
	}
	return string(ready)
}

func TestHostRecoveryProcessCrash(t *testing.T) {
	for _, mode := range []string{"prepared", "before_observe", "before_file"} {
		t.Run(mode, func(t *testing.T) {
			directory, j := hostTestJournal(t)
			j.Close()
			digest := hostRecoveryCrashChild(t, directory, mode, "")
			j, err := OpenHostJournal(directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			binding := hostRecoveryTestBinding()
			var before []byte
			if mode == "before_observe" {
				before, err = os.ReadFile(filepath.Join(directory, j.recoveryName(binding)))
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode != "prepared" {
				if err := j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error { t.Fatal("unobserved preparation used"); return nil }); !errors.Is(err, ErrHostRecoveryUnavailable) {
					t.Fatal(err)
				}
				observed, err := j.ReconcileRuntimeRecovery(binding)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "before_file" {
					if observed != HostNotApplied {
						t.Fatal("absent file was treated as created")
					}
					if err := j.PrepareRuntimeRecovery(binding); err != nil {
						t.Fatal(err)
					}
				} else if observed != HostApplied {
					t.Fatal("complete file was not adopted")
				}
				if mode == "before_observe" {
					after, err := os.ReadFile(filepath.Join(directory, j.recoveryName(binding)))
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("reconciliation rewrote the secret")
					}
				}
			}
			recovered := recoveryTestTokenHash(t, j, binding)
			if mode != "before_file" && recovered != digest {
				t.Fatal("process recovery generated a different token")
			}
		})
	}
}

func TestHostRecoveryDatabaseProcessCrash(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" && os.Getenv("RELEASE_DOCKER_TESTS") == "1" {
		hostRecoveryNativePostgres(t)
	}
	db := releaseTestDatabase(t)
	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	dsn := releaseTestDSN(t, os.Getenv("TEST_DATABASE_URL"), name)
	directory, j := hostTestJournal(t)
	j.Close()
	digest := hostRecoveryCrashChild(t, directory, "database", dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	state, err := database.ReadRuntimeFence(ctx, db)
	if err != nil || !state.Active || state.Epoch != 1 {
		t.Fatal("committed DB fence did not survive controller death")
	}
	if runtimeDB, err := database.OpenRuntime(ctx, dsn); !errors.Is(err, database.ErrRuntimeFenced) {
		if runtimeDB != nil {
			runtimeDB.Close()
		}
		t.Fatal("normal runtime was not rejected by the durable fence")
	}
	j, err = OpenHostJournal(directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	binding := hostRecoveryTestBinding()
	if recoveryTestTokenHash(t, j, binding) != digest {
		t.Fatal("DB recovery token changed")
	}
	snapshot, err := j.Snapshot()
	if err != nil || snapshot.Pending.Effect != HostFence {
		t.Fatal("lost acknowledgement was incorrectly resolved")
	}
	var controller *database.RuntimeRelease
	for {
		var databaseError error
		err := j.WithRuntimeRecovery(binding, func(request database.RuntimeReleaseRequest) error {
			controller, databaseError = database.BeginRuntimeRelease(ctx, db, request)
			return databaseError
		})
		if err == nil {
			break
		}
		// PostgreSQL may not have processed the dead client's EOF yet. Retry only
		// that observed controller contention, never a changed fence or credential.
		if !errors.Is(databaseError, database.ErrRuntimeControllerBusy) || ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer controller.Close()
	if ok, err := controller.TryQuiesce(ctx); err != nil || !ok {
		t.Fatal("recovered owner could not acquire exclusion")
	}
	observed, err := database.ReadRuntimeFence(ctx, db)
	if err != nil || !reflect.DeepEqual(observed, state) {
		t.Fatal("recovery replaced the durable fence instead of resuming it")
	}
	if err := j.Observe(snapshot.Pending.Sequence, HostApplied, hostRecoveryDigest([]byte("fixture-fence-observed"))); err != nil {
		t.Fatal(err)
	}
	controller.Close()
	// Model the control-record part of an isolated restore, not a complete
	// database/files backup. The second database is also freshly test-created.
	restoredDB := releaseTestDatabase(t)
	var control string
	if err := db.QueryRowContext(ctx, `SELECT row_to_json(c)::text FROM release_runtime_control c WHERE singleton=1`).Scan(&control); err != nil {
		t.Fatal(err)
	}
	if _, err := restoredDB.ExecContext(ctx, `UPDATE release_runtime_control SET
	 (protocol_version,epoch,active,operation_id,token_hash,previous_manifest_hash,candidate_manifest_hash,requested_at,finished_at)=
	 (SELECT protocol_version,epoch,active,operation_id,token_hash,previous_manifest_hash,candidate_manifest_hash,requested_at,finished_at
	  FROM json_populate_record(NULL::release_runtime_control,$1::json)) WHERE singleton=1`, control); err != nil {
		t.Fatal("fixture control-record copy failed")
	}
	restoredBinding := HostRuntimeRecoveryBinding{DatabaseSHA256: strings.Repeat("f", 64), ExpectedEpoch: binding.ExpectedEpoch, RestoreSourceSHA256: binding.DatabaseSHA256}
	if err := j.PrepareRuntimeRecovery(restoredBinding); err != nil {
		t.Fatal(err)
	}
	if recoveryTestTokenHash(t, j, restoredBinding) != digest {
		t.Fatal("restore token continuity lost")
	}
	restoreIntent, err := j.Intent(HostFenceRecovery, restoredBinding.DatabaseSHA256)
	if err != nil {
		t.Fatal(err)
	}
	var restoredController *database.RuntimeRelease
	if err := j.WithRuntimeRecovery(restoredBinding, func(request database.RuntimeReleaseRequest) error {
		var err error
		restoredController, err = database.BeginRuntimeRelease(ctx, restoredDB, request)
		return err
	}); err != nil {
		t.Fatal("copied fence could not be resumed with rebound credentials")
	}
	defer restoredController.Close()
	if ok, err := restoredController.TryQuiesce(ctx); err != nil || !ok {
		t.Fatal("restored fixture could not acquire exclusion")
	}
	restoredState, err := database.ReadRuntimeFence(ctx, restoredDB)
	if err != nil || !reflect.DeepEqual(restoredState, state) {
		t.Fatal("restored fixture's copied fence changed")
	}
	if err := j.Observe(restoreIntent.Sequence, HostApplied, hostRecoveryDigest([]byte("fixture-restored-fence-observed"))); err != nil {
		t.Fatal(err)
	}
	restoredController.Close()
	if err := os.Remove(filepath.Join(directory, j.recoveryName(binding))); err != nil {
		t.Fatal(err)
	}
	if err := j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error {
		t.Fatal("lost token reached database callback")
		return nil
	}); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal(err)
	}
	if err := j.PrepareRuntimeRecovery(binding); !errors.Is(err, ErrHostJournalState) {
		t.Fatal("lost fenced-database token was rotated")
	}
	if recoveryTestTokenHash(t, j, restoredBinding) != digest {
		t.Fatal("rebound credential required the lost source file")
	}
	j.Close()
	observed, err = database.ReadRuntimeFence(ctx, db)
	if err != nil || !observed.Active || observed.Epoch != 1 {
		t.Fatal("cleanup released admission fence")
	}
	observed, err = database.ReadRuntimeFence(ctx, restoredDB)
	if err != nil || !observed.Active || observed.Epoch != 1 {
		t.Fatal("cleanup released restored admission fence")
	}
}
