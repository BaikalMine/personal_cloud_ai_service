//go:build linux || windows

package release

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Protected-file fixtures only. These artificial outcomes do not assert SQL.
func hostRestoreRetryRecord(t *testing.T, j *HostJournal, previous databaseRestoreRecord, receipt string, outcome HostObservation) (databaseRestoreRecord, string) {
	t.Helper()
	ctx := context.Background()
	options := previous.Binding.Options
	options.TimeoutSeconds++
	m, err := j.restoreMaterial(ctx, previous.Binding.Epoch, previous.Binding.ArchiveOptions, options)
	if err != nil {
		t.Fatal(err)
	}
	m.binding.PreviousAttempt, m.binding.PreviousReceipt = previous.Binding.Target, receipt
	m.binding.Target = databaseRestoreRetryHash(m.binding.Target, previous.Binding.Target, receipt)
	m.binding.ToolIdentity = previous.Binding.ToolIdentity
	m.binding.Intent, err = j.Intent(HostRestoreDatabase, m.binding.Target)
	if err != nil {
		t.Fatal(err)
	}
	r := databaseRestoreRecord{Binding: m.binding, Phase: "prepared", At: time.Now().UTC()}
	digest, err := j.createDatabaseRestoreRecord(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	password, err := databaseArchivePassword(m.destination.Configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(password)
	name, _ := databaseRestorePasswordNames(m.binding.Target)
	if err := j.writeArchivePassword(name, password); err != nil {
		t.Fatal(err)
	}
	proof, err := j.archiveProof(ctx, name, maxDockerDatabaseURLBytes*2)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase, r.Previous, r.Credential, r.Process, r.At = "writer", digest, &proof, previous.Process, time.Now().UTC()
	r.Process.PID++
	digest, err = j.createDatabaseRestoreRecord(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.removeArchivePassword(name, proof); err != nil {
		t.Fatal(err)
	}
	r.Phase, r.Previous, r.At, r.Resolution = "resolved", digest, time.Now().UTC(), outcome
	r.ResultOID, r.ObservationSHA256 = m.destination.BootstrapOID, strings.Repeat("b", 64)
	if outcome == HostApplied {
		r.ResultOID++
	}
	digest, err = j.createDatabaseRestoreRecord(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Observe(r.Binding.Intent.Sequence, outcome, digest); err != nil {
		t.Fatal(err)
	}
	return r, digest
}

func TestHostDatabaseRestoreRetryLineage(t *testing.T) {
	for _, mode := range []string{"reopen", "first-partial", "middle-writer-missing", "middle-conflict", "first-password", "wrong-last-options"} {
		t.Run(mode, func(t *testing.T) {
			directory, j, first, digest, err := hostRestoreResolutionFixture(t, HostNotApplied, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := j.Observe(first.Binding.Intent.Sequence, HostNotApplied, digest); err != nil {
				t.Fatal(err)
			}
			middle, digest := hostRestoreRetryRecord(t, j, first, digest, HostNotApplied)
			last, _ := hostRestoreRetryRecord(t, j, middle, digest, HostApplied)
			options := last.Binding.Options
			switch mode {
			case "first-partial":
				err = os.Truncate(filepath.Join(directory, databaseRestoreRecordName(first.Binding.Target, "resolved")), 1)
			case "middle-writer-missing":
				err = os.Remove(filepath.Join(directory, databaseRestoreRecordName(middle.Binding.Target, "writer")))
			case "middle-conflict":
				err = os.WriteFile(filepath.Join(directory, databaseRestoreRecordName(middle.Binding.Target, "completed")), []byte("{}"), 0600)
			case "first-password":
				name, _ := databaseRestorePasswordNames(first.Binding.Target)
				err = os.WriteFile(filepath.Join(directory, name), []byte("unexpected"), 0600)
			case "wrong-last-options":
				options = first.Binding.Options
			}
			if err != nil {
				t.Fatal(err)
			}
			j.Close()
			j, err = OpenHostJournal(directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			result, err := j.ReadDockerDatabaseRestore(context.Background(), 0, last.Binding.ArchiveOptions, options)
			if (err == nil) != (mode == "reopen") {
				t.Fatal("incorrect linked restore result", result, err)
			}
			outcome, err := j.ReadDockerDatabaseRestoreOutcome(context.Background(), 0, last.Binding.ArchiveOptions, options)
			if (err == nil) != (mode == "reopen") {
				t.Fatal("incorrect linked outcome", outcome, err)
			}
			if mode == "reopen" && (outcome.Outcome != HostApplied || outcome.TargetSHA256 != last.Binding.Target || result.TargetSHA256 != last.Binding.Target) {
				t.Fatal("historical reader selected an ancestor")
			}
		})
	}
}

func TestHostDatabaseRestoreRetryRejectsUnlinkedHistory(t *testing.T) {
	for _, mode := range []string{"duplicate-target", "after-applied", "wrong-link", "wrong-receipt"} {
		t.Run(mode, func(t *testing.T) {
			outcome := HostNotApplied
			if mode == "after-applied" {
				outcome = HostApplied
			}
			_, j, previous, digest, err := hostRestoreResolutionFixture(t, outcome, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := j.Observe(previous.Binding.Intent.Sequence, outcome, digest); err != nil {
				t.Fatal(err)
			}
			m, err := j.restoreMaterial(context.Background(), 0, previous.Binding.ArchiveOptions, previous.Binding.Options)
			if err != nil {
				t.Fatal(err)
			}
			m.binding.PreviousAttempt, m.binding.PreviousReceipt = previous.Binding.Target, digest
			m.binding.Target = databaseRestoreRetryHash(m.binding.Target, previous.Binding.Target, digest)
			m.binding.ToolIdentity = previous.Binding.ToolIdentity
			if mode == "duplicate-target" {
				m.binding.Target = previous.Binding.Target
			}
			m.binding.Intent, err = j.Intent(HostRestoreDatabase, m.binding.Target)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wrong-link" {
				m.binding.PreviousAttempt = strings.Repeat("c", 64)
			}
			if mode == "wrong-receipt" {
				m.binding.PreviousReceipt = strings.Repeat("d", 64)
			}
			if _, err := j.createDatabaseRestoreRecord(context.Background(), databaseRestoreRecord{Binding: m.binding, Phase: "prepared", At: time.Now().UTC()}); err == nil {
				t.Fatal("unlinked history accepted")
			}
		})
	}
}
