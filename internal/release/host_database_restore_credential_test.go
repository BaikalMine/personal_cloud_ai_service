//go:build linux || windows

package release

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The child is real; archive/target metadata are file-only fixtures, not SQL.
func hostRestoreCredentialFixture(t *testing.T, process databaseProcessIdentity, mode string) (string, *HostJournal, databaseRestoreRecord) {
	t.Helper()
	directory, j, archiveOptions, _ := hostDatabaseTargetFixture(t)
	options := DatabaseRestoreOptions{Tool: archiveOptions.Tool, TimeoutSeconds: 30}
	m, err := j.restoreMaterial(context.Background(), 0, archiveOptions, options)
	if err != nil {
		t.Fatal(err)
	}
	m.binding.ToolIdentity = strings.Repeat("a", 64)
	m.binding.Intent, err = j.Intent(HostRestoreDatabase, m.binding.Target)
	if err != nil {
		t.Fatal(err)
	}
	r := databaseRestoreRecord{Binding: m.binding, Phase: "prepared", At: time.Now().UTC()}
	prepared, err := j.createDatabaseRestoreRecord(context.Background(), r)
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
	proof, err := j.archiveProof(context.Background(), name, maxDockerDatabaseURLBytes*2)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase, r.Previous, r.Process, r.Credential, r.At = "writer", prepared, process, &proof, time.Now().UTC()
	if mode == "legacy" {
		r.Credential = nil
	}
	if _, err := j.createDatabaseRestoreRecord(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return directory, j, r
}

func TestHostDatabaseRestoreCredentialCleanup(t *testing.T) {
	for _, mode := range []string{"original", "absent", "replaced", "changed", "truncated", "directory", "ambient",
		"writer-missing", "writer-replaced", "writer-partial", "prepared-missing", "legacy", "wrong-options", "cancelled", "observed", "unknown-process", "permissions", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			childDir := t.TempDir()
			input := databaseWriterTestInput(t, childDir, []byte("fixture input"))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var directory string
			var j *HostJournal
			var record databaseRestoreRecord
			err := runPreparedDatabaseWriter(ctx, os.Args[0], []string{"-test.run=^TestDatabaseWriterHelper$"},
				databaseWriterTestEnvironment(childDir, "barrier"), childDir, input, func(ctx context.Context, process databaseProcessIdentity) error {
					if process.PID != databaseWriterTestPID(t, childDir) {
						return errors.New("wrong helper identity")
					}
					if mode == "unknown-process" {
						if process.Platform == "windows" {
							process = databaseProcessIdentity{Platform: "linux", PID: process.PID, Started: process.Started,
								BootID: "12345678-1234-1234-1234-123456789abc", PIDNamespace: "5:42"}
						} else {
							process = databaseProcessIdentity{Platform: "windows", PID: process.PID, Started: process.Started}
						}
					}
					directory, j, record = hostRestoreCredentialFixture(t, process, mode)
					if err := j.CleanupDockerDatabaseRestorePassword(ctx, 0, record.Binding.ArchiveOptions, record.Binding.Options); err == nil {
						return errors.New("cleanup accepted a live or unobservable writer")
					}
					name, _ := databaseRestorePasswordNames(record.Binding.Target)
					if _, err := j.archiveProof(ctx, name, maxDockerDatabaseURLBytes*2); err != nil {
						return errors.New("live writer lost its credential")
					}
					return nil
				})
			if err != nil || j == nil {
				t.Fatal("writer fixture failed", err)
			}
			name, absent := databaseRestorePasswordNames(record.Binding.Target)
			path := filepath.Join(directory, name)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(original)
			options := record.Binding.Options
			switch mode {
			case "absent":
				err = os.Remove(path)
			case "replaced":
				if err = os.Rename(path, path+".original"); err == nil {
					err = os.WriteFile(path, original, 0600)
				}
			case "changed":
				err = os.WriteFile(path, []byte(strings.Repeat("x", len(original))), 0600)
			case "truncated":
				err = os.Truncate(path, 0)
			case "directory":
				if err = os.Remove(path); err == nil {
					err = os.Mkdir(path, 0700)
				}
			case "ambient":
				err = os.WriteFile(filepath.Join(directory, absent), []byte("unowned"), 0600)
			case "permissions":
				broadenRestoreCredentialPermissions(t, path)
			case "hardlink":
				err = os.Link(path, path+".alias")
			case "writer-missing", "prepared-missing":
				err = os.Remove(filepath.Join(directory, databaseRestoreRecordName(record.Binding.Target, strings.TrimSuffix(mode, "-missing"))))
			case "writer-replaced":
				writer := filepath.Join(directory, databaseRestoreRecordName(record.Binding.Target, "writer"))
				body, readErr := os.ReadFile(writer)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if err = os.Rename(writer, writer+".original"); err == nil {
					err = os.WriteFile(writer, body, 0600)
				}
			case "writer-partial":
				err = os.Truncate(filepath.Join(directory, databaseRestoreRecordName(record.Binding.Target, "writer")), 1)
			case "wrong-options":
				options.TimeoutSeconds++
			case "observed":
				err = j.Observe(record.Binding.Intent.Sequence, HostNotApplied, strings.Repeat("c", 64))
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
			before, journalBefore, err := j.readOwnedArchiveHistory()
			if err != nil || bytes.Contains(journalBefore, original) ||
				(record.Credential != nil && bytes.Contains(journalBefore, []byte(record.Credential.SHA256))) {
				t.Fatal("credential leaked into the public journal", err)
			}
			if mode == "cancelled" {
				cancel()
			}
			for attempt := 0; attempt < 2; attempt++ {
				err = j.CleanupDockerDatabaseRestorePassword(ctx, 0, record.Binding.ArchiveOptions, options)
				if (err == nil) != (mode == "original" || mode == "absent") {
					t.Fatal("unexpected cleanup result", err)
				}
				_, statErr := os.Lstat(path)
				if (errors.Is(statErr, os.ErrNotExist)) != (mode == "original" || mode == "absent") {
					t.Fatal("cleanup removed an unowned or unproven file", statErr)
				}
				after, journalAfter, err := j.readOwnedArchiveHistory()
				if err != nil || after != before || !bytes.Equal(journalBefore, journalAfter) {
					t.Fatal("credential cleanup changed the SQL observation")
				}
			}
			if _, err := j.ReadDockerDatabaseRestore(context.Background(), 0, record.Binding.ArchiveOptions, record.Binding.Options); err == nil {
				t.Fatal("credential cleanup published restore success")
			}
		})
	}
}

func TestHostDatabaseRestoreCredentialChain(t *testing.T) {
	for _, mode := range []string{"missing", "identity", "hash", "size", "valid"} {
		t.Run(mode, func(t *testing.T) {
			_, j, writer := hostRestoreCredentialFixture(t, databaseProcessIdentity{Platform: "windows", PID: 123, Started: 456}, "")
			ctx := context.Background()
			_, digest, err := j.readDatabaseRestoreChain(ctx, writer.Binding.Target, "writer", "")
			if err != nil {
				t.Fatal(err)
			}
			m, err := j.restoreMaterial(ctx, 0, writer.Binding.ArchiveOptions, writer.Binding.Options)
			if err != nil {
				t.Fatal(err)
			}
			name, _ := databaseRestorePasswordNames(writer.Binding.Target)
			if err := j.removeArchivePassword(name, *writer.Credential); err != nil {
				t.Fatal(err)
			}
			writer.Phase, writer.Previous, writer.ResultOID, writer.At = "completed", digest, m.destination.BootstrapOID+1, time.Now().UTC()
			writer.ObservationSHA256 = strings.Repeat("b", 64)
			switch mode {
			case "missing":
				writer.Credential = nil
			case "identity":
				writer.Credential.Identity += "different"
			case "hash":
				writer.Credential.SHA256 = strings.Repeat("c", 64)
			case "size":
				writer.Credential.Bytes++
			}
			_, err = j.createDatabaseRestoreRecord(ctx, writer)
			if (err == nil) != (mode == "valid") {
				t.Fatal("credential binding changed across phases", err)
			}
		})
	}
}
