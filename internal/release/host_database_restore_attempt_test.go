package release

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// File protection only: these records do not assert a real SQL restore.
func hostDatabaseRestoreAttemptFixture(t *testing.T) (string, *HostJournal, databaseRestoreRecord, string) {
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
	for _, phase := range []string{"prepared", "writer", "completed"} {
		r.Phase = phase
		if phase == "writer" {
			r.Process = databaseProcessIdentity{Platform: "windows", PID: 123, Started: 456}
		}
		if phase == "completed" {
			r.ResultOID, r.ObservationSHA256 = m.destination.BootstrapOID+1, strings.Repeat("b", 64)
		}
		digest, err := j.createDatabaseRestoreRecord(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if phase == "completed" {
			return directory, j, r, digest
		}
		r.Previous = digest
	}
	panic("unreachable")
}

func TestHostDatabaseRestoreAttemptFiles(t *testing.T) {
	for _, mode := range []string{"reopen", "prepared-missing", "writer-missing", "completed-missing", "writer-replaced", "partial", "append", "wrong-receipt", "password", "ambient", "archive", "target", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			directory, j, r, digest := hostDatabaseRestoreAttemptFixture(t)
			ctx := context.Background()
			path := filepath.Join(directory, databaseRestoreRecordName(r.Binding.Target, "completed"))
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "reopen":
				if err = j.Observe(r.Binding.Intent.Sequence, HostApplied, digest); err != nil {
					t.Fatal(err)
				}
				j.Close()
				j, err = OpenHostJournal(directory, hostTestInstallation)
				if err == nil {
					defer j.Close()
				}
			case "prepared-missing", "writer-missing", "completed-missing":
				err = os.Remove(filepath.Join(directory, databaseRestoreRecordName(r.Binding.Target, strings.TrimSuffix(mode, "-missing"))))
			case "writer-replaced":
				writer := filepath.Join(directory, databaseRestoreRecordName(r.Binding.Target, "writer"))
				data, readErr := os.ReadFile(writer)
				if readErr != nil || os.Rename(writer, writer+".original") != nil {
					t.Fatal("cannot prepare replacement fixture", readErr)
				}
				err = os.WriteFile(writer, data, 0600)
			case "partial":
				err = os.WriteFile(path, body[:len(body)/2], 0600)
			case "append":
				err = os.WriteFile(path, append(body, '\n'), 0600)
			case "wrong-receipt":
				digest = strings.Repeat("c", 64)
			case "password", "ambient":
				name, absent := databaseRestorePasswordNames(r.Binding.Target)
				if mode == "ambient" {
					name = absent
				}
				err = os.WriteFile(filepath.Join(directory, name), []byte("unexpected"), 0600)
			case "archive":
				m, materialErr := j.restoreMaterial(ctx, 0, r.Binding.ArchiveOptions, r.Binding.Options)
				if materialErr != nil {
					t.Fatal(materialErr)
				}
				name, _, _, _ := databaseArchiveNames(m.archive.Target)
				err = os.Remove(filepath.Join(directory, name))
			case "target":
				err = os.Remove(filepath.Join(directory, databaseTargetMetadataName(r.Binding.DatabaseTarget)))
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = j.readDatabaseRestoreChain(ctx, r.Binding.Target, "completed", digest)
			if mode == "reopen" {
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := j.ReadDockerDatabaseRestore(ctx, 0, r.Binding.ArchiveOptions, r.Binding.Options)
				if err != nil || !receipt.Restored || receipt.CoherentBackup || receipt.TargetSHA256 != r.Binding.Target {
					t.Fatal("historical receipt missing", err)
				}
			} else if err == nil {
				t.Fatal("changed record chain accepted")
			}
		})
	}
}

func TestHostDatabaseRestoreAttemptExclusivity(t *testing.T) {
	_, j, r, _ := hostDatabaseRestoreAttemptFixture(t)
	if _, err := j.createDatabaseRestoreRecord(context.Background(), r); err == nil {
		t.Fatal("completed record overwritten")
	}
	if _, err := j.ReadDockerDatabaseRestore(context.Background(), 0, r.Binding.ArchiveOptions, r.Binding.Options); err == nil {
		t.Fatal("unacknowledged restore published as historical success")
	}
	if _, _, err := j.readDatabaseRestoreChain(context.Background(), r.Binding.Target, "../writer", ""); err == nil {
		t.Fatal("invalid phase accepted")
	}
}
