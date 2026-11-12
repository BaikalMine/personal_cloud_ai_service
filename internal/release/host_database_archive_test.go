package release

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This fixture exercises private-file identity only. It does not represent an
// executed dump; the PostgreSQL round-trip/process gates cover that boundary.
func hostDatabaseArchiveFixture(t *testing.T) (string, *HostJournal, DatabaseArchiveOptions, DatabaseArchiveReceipt) {
	return hostDatabaseArchivePropertiesFixture(t, nil)
}

func hostDatabaseArchivePropertiesFixture(t *testing.T, properties *postgresDatabaseProperties) (string, *HostJournal, DatabaseArchiveOptions, DatabaseArchiveReceipt) {
	t.Helper()
	directory, j, _ := hostDatabaseConfigurationFixture(t)
	plan, err := j.ReadDockerPlan()
	if err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareRuntimeRecovery(plan.RuntimeRecoveryBinding(0)); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []struct {
		effect HostEffect
		target string
	}{{HostFence, j.recoveryTarget(plan.RuntimeRecoveryBinding(0))}, {HostStopService, dockerStopTarget(plan, 0, 2)}} {
		pending, err := j.Intent(stage.effect, stage.target)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.Observe(pending.Sequence, HostApplied, strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
	}
	options := DatabaseArchiveOptions{Tool: PostgreSQLDumpTool{Path: filepath.Join(directory, "pg_dump"), SHA256: strings.Repeat("b", 64)}, MaxBytes: 1 << 20, TimeoutSeconds: 60}
	target := databaseArchiveTarget(plan, 0, options)
	pending, err := j.Intent(HostArchiveDatabase, target)
	if err != nil {
		t.Fatal(err)
	}
	archiveName, metadataName, _, _ := databaseArchiveNames(target)
	if err := os.WriteFile(filepath.Join(directory, archiveName), []byte("PGDMPprivate-file-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	proof, err := j.archiveProof(context.Background(), archiveName, options.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	configurationReceipt, _ := j.databaseConfigReceipt()
	payload := databaseArchivePayload{Target: target, Options: options, Database: plan.data.Observed.Database,
		Epoch: 0, ConfigurationReceipt: configurationReceipt, ToolIdentity: strings.Repeat("c", 64), PostgresMajor: 16,
		ObservationSHA256: strings.Repeat("d", 64), Archive: proof, CompletedAt: time.Now().UTC(), DatabaseProperties: properties}
	digest, err := j.createDatabaseArchiveMetadata(context.Background(), metadataName, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Observe(pending.Sequence, HostApplied, digest); err != nil {
		t.Fatal(err)
	}
	return directory, j, options, archiveReceipt(payload)
}

func TestHostDatabaseArchiveFileContext(t *testing.T) {
	for _, mode := range []string{"reopen", "archive_content", "archive_replace", "archive_missing", "metadata_partial", "metadata_replace", "configuration_missing", "password_present", "ambient_present", "wrong_receipt", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			directory, j, options, expected := hostDatabaseArchiveFixture(t)
			archiveName, metadataName, passwordName, absentName := databaseArchiveNames(expected.TargetSHA256)
			archivePath, metadataPath := filepath.Join(directory, archiveName), filepath.Join(directory, metadataName)
			before, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "reopen":
				j.Close()
				j, err = OpenHostJournal(directory, hostTestInstallation)
				if err != nil {
					t.Fatal(err)
				}
				defer j.Close()
			case "archive_content":
				err = os.WriteFile(archivePath, []byte("PGDMPchanged-file-fixture"), 0o600)
			case "archive_replace", "metadata_replace":
				path := archivePath
				if mode == "metadata_replace" {
					path = metadataPath
				}
				body, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(path, body, 0o600)
			case "archive_missing":
				err = os.Remove(archivePath)
			case "metadata_partial":
				err = os.WriteFile(metadataPath, []byte("{"), 0o600)
			case "configuration_missing":
				err = os.Remove(filepath.Join(directory, j.databaseConfigName()))
			case "password_present":
				err = j.writeArchivePassword(passwordName, []byte("unresolved-private-password"))
			case "ambient_present":
				err = os.WriteFile(filepath.Join(directory, absentName), []byte("ambient-file"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if mode == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if mode == "wrong_receipt" {
				if _, _, err := j.readDatabaseArchiveMetadata(ctx, expected.TargetSHA256, strings.Repeat("f", 64)); err == nil {
					t.Fatal("wrong archive receipt accepted")
				}
				return
			}
			actual, err := j.ReadDockerDatabaseArchive(ctx, 0, options)
			if mode == "reopen" {
				after, readErr := os.ReadFile(metadataPath)
				if err != nil || readErr != nil || actual != expected || !bytes.Equal(before, after) {
					t.Fatal("archive changed on reopen", err, readErr)
				}
			} else if err == nil {
				t.Fatal("changed archive material accepted")
			}
		})
	}
}
