//go:build linux || windows

package release

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
)

func TestDatabaseRestoreResolutionSQL(t *testing.T) {
	options := databaseRestoreTestOptions(t)
	for _, mode := range []string{"applied", "rollback", "resolved-applied", "resolved-rollback", "completed-partial",
		"resolved-partial", "writer-missing", "prepared-missing", "foreign-session", "catalog-drift", "rollback-extra-object",
		"resolved-catalog-drift", "resolved-rollback-extra-object"} {
		t.Run(mode, func(t *testing.T) {
			f, archiveOptions, m := databaseRestoreAttemptFixture(t, options)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			rollback := strings.Contains(mode, "rollback")
			calls := 0
			run := func(ctx context.Context, options DatabaseRestoreOptions, env []string, directory, name string, input *os.File,
				prepare func(context.Context, databaseProcessIdentity) error) error {
				calls++
				err := runPreparedPostgreSQLRestore(ctx, options, env, directory, name, input,
					func(ctx context.Context, process databaseProcessIdentity) error {
						if err := prepare(ctx, process); err != nil {
							return err
						}
						if rollback {
							return ErrDatabaseRestore
						}
						return nil
					})
				if (err != nil) != rollback {
					t.Error("unexpected real writer result", err)
				}
				return ErrDatabaseRestore
			}
			if _, err := f.j.restoreDockerDatabase(ctx, f.controller, 0, archiveOptions, options, f.open, run); err == nil {
				t.Fatal("injected lost completion did not fail")
			}
			pending := f.j.state.Pending
			if pending.Effect != HostRestoreDatabase || calls != 1 {
				t.Fatal("missing original pending attempt")
			}
			if strings.HasPrefix(mode, "resolved-") {
				record, digest, err := f.j.readDatabaseRestoreRecoveryHead(ctx, m.binding.Target, "")
				if err != nil {
					t.Fatal(err)
				}
				err = withDatabaseRestoreOwnership(ctx, f.controller, m, f.open, func(source, target *sql.Conn, _ *dockerObserver) error {
					_, _, err := f.j.resolveDatabaseRestore(ctx, source, target, m, record, digest)
					return err
				})
				if err != nil || f.j.state.Pending != pending {
					t.Fatal("resolution changed the pending journal intent", err)
				}
			}
			var outsider *sql.DB
			switch mode {
			case "completed-partial":
				if err := os.WriteFile(filepath.Join(f.directory, databaseRestoreRecordName(m.binding.Target, "completed")), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "resolved-partial":
				if err := os.Truncate(filepath.Join(f.directory, databaseRestoreRecordName(m.binding.Target, "resolved")), 1); err != nil {
					t.Fatal(err)
				}
			case "writer-missing", "prepared-missing":
				if err := os.Remove(filepath.Join(f.directory, databaseRestoreRecordName(m.binding.Target, strings.TrimSuffix(mode, "-missing")))); err != nil {
					t.Fatal(err)
				}
			case "foreign-session", "catalog-drift", "resolved-catalog-drift", "rollback-extra-object", "resolved-rollback-extra-object":
				var err error
				outsider, err = database.Open(ctx, m.destination.Configuration.URL)
				if err != nil {
					t.Fatal(err)
				}
				defer outsider.Close()
				if mode == "foreign-session" {
					if err := outsider.PingContext(ctx); err != nil {
						t.Fatal(err)
					}
				} else {
					statement := `UPDATE public.schema_migrations SET checksum=repeat('a',64) WHERE version=93`
					if rollback {
						statement = `CREATE TABLE public.unowned(body text)`
					}
					preflightExec(t, outsider, statement)
					outsider.Close()
				}
			}
			f.controller.Close()
			f.j.Close()
			var err error
			f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			f.controller, err = f.j.ResumeConfiguredDockerGatewayFence(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			expected := HostUnknown
			switch mode {
			case "applied", "resolved-applied":
				expected = HostApplied
			case "rollback", "resolved-rollback":
				expected = HostNotApplied
			}
			outcome, err := f.j.reconcileDockerDatabaseRestore(ctx, f.controller, 0, archiveOptions, options, f.open)
			if outcome != expected || (err != nil) != (expected == HostUnknown) {
				t.Fatal("unexpected SQL reconciliation", outcome, err)
			}
			if expected == HostUnknown && f.j.state.Pending != pending || expected != HostUnknown && f.j.state.Pending.Sequence != 0 {
				t.Fatal("incorrect pending intent after resolution")
			}
			if _, err := f.j.restoreDockerDatabase(ctx, f.controller, 0, archiveOptions, options, f.open, run); !errors.Is(err, ErrHostJournalState) || calls != 1 {
				t.Fatal("recovery authorized an implicit retry", err, calls)
			}
			_, readErr := f.j.ReadDockerDatabaseRestore(ctx, 0, archiveOptions, options)
			if (readErr == nil) != (expected == HostApplied) {
				t.Fatal("resolution published incorrect historical status", readErr)
			}
			if outsider != nil {
				outsider.Close()
			}
			requireRestoreRuntimeBlocked(t, m.destination.Configuration.URL)
			var original string
			if f.db.QueryRowContext(ctx, `SELECT body FROM public.attempt_witness`).Scan(&original) != nil || original != "private-restored-data" || f.posts.Load() != 1 {
				t.Fatal("resolution changed source data or service")
			}
		})
	}
}
