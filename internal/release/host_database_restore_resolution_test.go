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

// File-only evidence for protection/chain tests; no real SQL is asserted here.
func hostRestoreResolutionFixture(t *testing.T, outcome HostObservation, mode string) (string, *HostJournal, databaseRestoreRecord, string, error) {
	t.Helper()
	directory, j, r := hostRestoreCredentialFixture(t, databaseProcessIdentity{Platform: "windows", PID: 123, Started: 456}, "")
	ctx := context.Background()
	_, previous, err := j.readDatabaseRestoreChain(ctx, r.Binding.Target, "writer", "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := j.restoreMaterial(ctx, 0, r.Binding.ArchiveOptions, r.Binding.Options)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := databaseRestorePasswordNames(r.Binding.Target)
	if err := j.removeArchivePassword(name, *r.Credential); err != nil {
		t.Fatal(err)
	}
	r.Phase, r.Previous, r.At, r.Resolution = "resolved", previous, time.Now().UTC(), outcome
	r.ResultOID = m.destination.BootstrapOID
	if outcome == HostApplied {
		r.ResultOID++
	}
	r.ObservationSHA256 = strings.Repeat("b", 64)
	switch mode {
	case "wrong-outcome":
		r.Resolution = HostUnknown
	case "wrong-oid":
		if outcome == HostApplied {
			r.ResultOID--
		} else {
			r.ResultOID++
		}
	case "missing-credential":
		r.Credential = nil
	case "wrong-process":
		r.Process.PID++
	case "wrong-previous":
		r.Previous = strings.Repeat("c", 64)
	}
	digest, err := j.createDatabaseRestoreRecord(ctx, r)
	return directory, j, r, digest, err
}

func TestHostDatabaseRestoreResolutionFiles(t *testing.T) {
	for _, outcome := range []HostObservation{HostApplied, HostNotApplied} {
		for _, mode := range []string{"reopen", "partial", "replaced", "permissions", "writer-missing", "conflicting-completion", "wrong-outcome",
			"wrong-oid", "missing-credential", "wrong-process", "wrong-previous"} {
			t.Run(string(outcome)+"/"+mode, func(t *testing.T) {
				directory, j, r, digest, err := hostRestoreResolutionFixture(t, outcome, mode)
				if strings.HasPrefix(mode, "wrong-") || mode == "missing-credential" {
					if err == nil {
						t.Fatal("invalid resolution chain accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(directory, databaseRestoreRecordName(r.Binding.Target, "resolved"))
				switch mode {
				case "partial":
					err = os.Truncate(path, 1)
				case "replaced":
					body, readErr := os.ReadFile(path)
					if readErr != nil {
						t.Fatal(readErr)
					}
					if err = os.Rename(path, path+".original"); err == nil {
						err = os.WriteFile(path, body, 0600)
					}
				case "writer-missing":
					err = os.Remove(filepath.Join(directory, databaseRestoreRecordName(r.Binding.Target, "writer")))
				case "permissions":
					broadenRestoreCredentialPermissions(t, path)
				case "conflicting-completion":
					err = os.WriteFile(filepath.Join(directory, databaseRestoreRecordName(r.Binding.Target, "completed")), []byte("{}"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := j.Observe(r.Binding.Intent.Sequence, outcome, digest); err != nil {
					t.Fatal(err)
				}
				j.Close()
				j, err = OpenHostJournal(directory, hostTestInstallation)
				if err != nil {
					t.Fatal(err)
				}
				defer j.Close()
				ctx := context.Background()
				_, _, err = j.readDatabaseRestoreRecoveryHead(ctx, r.Binding.Target, digest)
				if (err == nil) != (mode == "reopen") {
					t.Fatal("incorrect resolution record status", err)
				}
				_, err = j.ReadDockerDatabaseRestore(ctx, 0, r.Binding.ArchiveOptions, r.Binding.Options)
				if (err == nil) != (mode == "reopen" && outcome == HostApplied) {
					t.Fatal("incorrect historical restore status", err)
				}
			})
		}
	}
}
