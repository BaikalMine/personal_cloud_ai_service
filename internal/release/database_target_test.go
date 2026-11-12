package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"ai-access-gateway/internal/database"
	"github.com/lib/pq"
)

func TestDatabaseTargetJournaledCreation(t *testing.T) {
	options := databaseArchiveTestOptions(t)
	for _, mode := range []string{"commit", "before-ack", "missing-record", "changed-properties", "changed-fence", "replaced-record"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			preflightExec(t, f.db, `CREATE TABLE public.target_witness(body text)`)
			preflightExec(t, f.db, `INSERT INTO public.target_witness VALUES('source-private-content')`)
			prepareDatabaseArchiveFixture(t, f)
			ctx := context.Background()
			archive, err := f.j.archiveDockerDatabase(ctx, f.controller, 0, options, f.open, runPostgreSQLDump)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := f.j.committedDockerPlan()
			if err != nil {
				t.Fatal(err)
			}
			digest, err := f.j.databaseArchiveReceipt(archive.TargetSHA256)
			if err != nil || digest == "" {
				t.Fatal("archive receipt", err)
			}
			target := databaseTargetHash(plan, 0, archive.TargetSHA256, digest)
			name := databaseTargetName(target)
			created := false
			t.Cleanup(func() {
				if created {
					preflightExec(t, f.db, `DROP DATABASE `+pq.QuoteIdentifier(name)+` WITH (FORCE)`)
				}
			})
			var receipt DatabaseTargetReceipt
			if mode == "commit" {
				receipt, err = f.j.createDockerRestoreDatabase(ctx, f.controller, 0, options, f.open)
			} else {
				f.j.mu.Lock()
				payload, _, createErr := f.j.createDatabaseTarget(ctx, f.controller, 0, options, f.open)
				f.j.mu.Unlock()
				receipt, err = targetReceipt(payload), createErr
			}
			if err != nil {
				t.Fatal("create", err)
			}
			created = true
			if receipt.TargetSHA256 != target || receipt.ArchiveTargetSHA256 != archive.TargetSHA256 || receipt.Restored || receipt.Scope != databaseTargetScope {
				t.Fatal("creation claimed restore or wrong target", receipt)
			}
			payload, _, err := f.j.readDatabaseTargetMetadata(ctx, target, "")
			if err != nil {
				t.Fatal(err)
			}
			requireRestoreRuntimeBlocked(t, payload.Configuration.URL)
			targetDB, err := database.Open(ctx, payload.Configuration.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer targetDB.Close()
			var witnessExists bool
			if err := targetDB.QueryRow(`SELECT to_regclass('public.target_witness') IS NOT NULL`).Scan(&witnessExists); err != nil || witnessExists {
				t.Fatal("creation unexpectedly copied source data", err)
			}
			_, history, err := f.j.readOwnedArchiveHistory()
			if err != nil {
				t.Fatal(err)
			}
			public, _ := json.Marshal(receipt)
			u, _ := url.Parse(payload.Configuration.URL)
			password, _ := u.User.Password()
			for _, private := range []string{password, u.String(), name, "source-private-content"} {
				if bytes.Contains(history, []byte(private)) || bytes.Contains(public, []byte(private)) {
					t.Fatal("creation leaked private material")
				}
			}
			path := filepath.Join(f.directory, databaseTargetMetadataName(target))
			switch mode {
			case "missing-record":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "changed-properties":
				preflightExec(t, f.db, `COMMENT ON DATABASE `+pq.QuoteIdentifier(name)+` IS 'changed'`)
			case "changed-fence":
				preflightExec(t, targetDB, `UPDATE public.release_runtime_control SET active=false,finished_at=now()`)
			case "replaced-record":
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f.controller.Close()
			f.j.Close()
			f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			f.controller, err = f.j.ResumeDockerGatewayFence(ctx, f.db, 0)
			if err != nil {
				t.Fatal("resume source", err)
			}
			if mode != "commit" {
				observation, err := f.j.reconcileDockerRestoreDatabase(ctx, f.controller, 0, options, f.open)
				if mode == "before-ack" {
					if err != nil || observation != HostApplied {
						t.Fatal("valid completion not reconciled", observation, err)
					}
				} else {
					if err == nil || observation != HostUnknown || f.j.state.Pending.Effect != HostCreateRestoreDatabase {
						t.Fatal("uncertain target accepted", observation, err)
					}
					return
				}
			}
			read, err := f.j.ReadDockerRestoreDatabase(ctx, 0, options)
			if err != nil || read != receipt {
				t.Fatal("read creation", read, err)
			}
			if _, err := f.j.createDockerRestoreDatabase(ctx, f.controller, 0, options, f.open); !errors.Is(err, ErrHostJournalState) {
				t.Fatal("duplicate create permitted", err)
			}
			var source string
			if err := f.db.QueryRow(`SELECT body FROM public.target_witness`).Scan(&source); err != nil || source != "source-private-content" {
				t.Fatal("source changed", err)
			}
			if f.posts.Load() != 1 {
				t.Fatal("creation/recovery changed service")
			}
		})
	}
}

func TestDatabaseTargetRefusesExistingAndChangedSource(t *testing.T) {
	options := databaseArchiveTestOptions(t)
	for _, mode := range []string{"existing", "changed-source", "intent-only"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			prepareDatabaseArchiveFixture(t, f)
			ctx := context.Background()
			archive, err := f.j.archiveDockerDatabase(ctx, f.controller, 0, options, f.open, runPostgreSQLDump)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := f.j.committedDockerPlan()
			if err != nil {
				t.Fatal(err)
			}
			digest, err := f.j.databaseArchiveReceipt(archive.TargetSHA256)
			if err != nil || digest == "" {
				t.Fatal("archive receipt", err)
			}
			target := databaseTargetHash(plan, 0, archive.TargetSHA256, digest)
			name := databaseTargetName(target)
			switch mode {
			case "existing":
				preflightExec(t, f.db, `CREATE DATABASE `+pq.QuoteIdentifier(name)+` TEMPLATE template0`)
				t.Cleanup(func() { preflightExec(t, f.db, `DROP DATABASE `+pq.QuoteIdentifier(name)+` WITH (FORCE)`) })
			case "changed-source":
				preflightExec(t, f.db, `COMMENT ON DATABASE `+pq.QuoteIdentifier(f.plan.data.Observed.Database.DatabaseName)+` IS 'changed after archive'`)
			case "intent-only":
				if _, err := f.j.Intent(HostCreateRestoreDatabase, target); err != nil {
					t.Fatal(err)
				}
				observation, err := f.j.reconcileDockerRestoreDatabase(ctx, f.controller, 0, options, f.open)
				if err == nil || observation != HostUnknown {
					t.Fatal("absence invented completion", observation, err)
				}
				return
			}
			if _, err := f.j.createDockerRestoreDatabase(ctx, f.controller, 0, options, f.open); err == nil || f.j.state.Pending.Sequence != 0 {
				t.Fatal("unsafe creation passed preflight or wrote intent", err)
			}
			if _, err := f.j.root.Lstat(databaseTargetMetadataName(target)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refused target has metadata", err)
			}
		})
	}
}
