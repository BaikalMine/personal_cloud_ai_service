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

// This is a file-protection fixture, not an executed CREATE or archive.
// Real PostgreSQL and process-crash tests cover those effects separately.
func hostDatabaseTargetFixture(t *testing.T) (string, *HostJournal, DatabaseArchiveOptions, DatabaseTargetReceipt) {
	t.Helper()
	properties := &postgresDatabaseProperties{Version: 1, OwnerOID: 10, OwnerName: "fixture-owner", Encoding: "UTF8",
		LocaleProvider: "c", Collate: "C", CType: "C", TablespaceOID: 1663, TablespaceName: "pg_default", ConnectionLimit: -1,
		AllowConnections: true, DefaultACL: true, Grants: []postgresDatabaseGrant{}, Settings: []postgresDatabaseSetting{}, Labels: []postgresDatabaseLabel{}}
	directory, j, options, _ := hostDatabaseArchivePropertiesFixture(t, properties)
	plan, _, source, archive, configurationReceipt, archiveReceipt, err := j.targetMaterial(context.Background(), 0, options)
	if err != nil {
		t.Fatal(err)
	}
	target := databaseTargetHash(plan, 0, archive.Target, archiveReceipt)
	configuration, err := targetDatabaseConfiguration(source, target)
	if err != nil {
		t.Fatal(err)
	}
	identity := archive.Database
	identity.DatabaseName, identity.DatabaseOID = databaseTargetName(target), identity.DatabaseOID+1
	payload := databaseTargetPayload{Target: target, ArchiveTarget: archive.Target, ArchiveReceipt: archiveReceipt,
		ConfigurationReceipt: configurationReceipt, Configuration: configuration, Database: identity, BootstrapOID: 16384, CompletedAt: time.Now().UTC()}
	pending, err := j.Intent(HostCreateRestoreDatabase, target)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := j.createDatabaseTargetMetadata(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Observe(pending.Sequence, HostApplied, digest); err != nil {
		t.Fatal(err)
	}
	return directory, j, options, targetReceipt(payload)
}

func TestHostDatabaseTargetFileContext(t *testing.T) {
	for _, mode := range []string{"reopen", "partial", "replace", "missing", "append", "archive-changed", "configuration-missing", "wrong-receipt", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			directory, j, options, expected := hostDatabaseTargetFixture(t)
			path := filepath.Join(directory, databaseTargetMetadataName(expected.TargetSHA256))
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "reopen":
				j.Close()
				j, err = OpenHostJournal(directory, hostTestInstallation)
				if err == nil {
					defer j.Close()
				}
			case "partial":
				err = os.WriteFile(path, before[:len(before)/2], 0o600)
			case "replace":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(path, before, 0o600)
			case "missing":
				err = os.Remove(path)
			case "append":
				err = os.WriteFile(path, append(before, '\n'), 0o600)
			case "archive-changed":
				archiveName, _, _, _ := databaseArchiveNames(expected.ArchiveTargetSHA256)
				err = os.WriteFile(filepath.Join(directory, archiveName), []byte("PGDMPchanged"), 0o600)
			case "configuration-missing":
				err = os.Remove(filepath.Join(directory, j.databaseConfigName()))
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "wrong-receipt" {
				if _, _, err := j.readDatabaseTargetMetadata(ctx, expected.TargetSHA256, strings.Repeat("f", 64)); err == nil {
					t.Fatal("wrong receipt accepted")
				}
				return
			}
			actual, err := j.ReadDockerRestoreDatabase(ctx, 0, options)
			if mode == "reopen" {
				after, readErr := os.ReadFile(path)
				if err != nil || readErr != nil || actual != expected || !bytes.Equal(before, after) {
					t.Fatal("target record changed on reopen", err, readErr)
				}
			} else if err == nil {
				t.Fatal("changed private target material accepted")
			}
		})
	}
}

func TestHostDatabaseTargetRequiresArchivedProperties(t *testing.T) {
	_, j, options, _ := hostDatabaseArchiveFixture(t)
	if _, _, _, _, _, _, err := j.targetMaterial(context.Background(), 0, options); err == nil {
		t.Fatal("old archive gained invented properties")
	}
	if _, err := j.ReadDockerDatabaseArchive(context.Background(), 0, options); err != nil {
		t.Fatal("archive-only compatibility lost", err)
	}
}
