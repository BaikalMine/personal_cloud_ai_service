//go:build linux || windows

package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"github.com/lib/pq"
)

func databaseRestoreTestOptions(t *testing.T) DatabaseRestoreOptions {
	t.Helper()
	_ = databaseArchiveTestOptions(t)
	path := os.Getenv("TEST_RELEASE_PG_RESTORE")
	body, err := os.ReadFile(path)
	if err != nil || !filepath.IsAbs(path) {
		t.Fatal("explicit restore executable required", err)
	}
	return DatabaseRestoreOptions{Tool: PostgreSQLClientTool{Path: path, SHA256: hostRecoveryDigest(body)}, TimeoutSeconds: 45}
}

func databaseRestoreAttemptFixture(t *testing.T, options DatabaseRestoreOptions) (*dockerStopFixture, DatabaseArchiveOptions, databaseRestoreMaterial) {
	t.Helper()
	archiveOptions := databaseArchiveTestOptions(t)
	f := newDockerStopFixtureWithFence(t, false)
	preflightExec(t, f.db, `CREATE TABLE public.attempt_witness(body text)`)
	preflightExec(t, f.db, `INSERT INTO public.attempt_witness VALUES('private-restored-data')`)
	prepareDatabaseArchiveFixture(t, f)
	ctx := context.Background()
	if _, err := f.j.archiveDockerDatabase(ctx, f.controller, 0, archiveOptions, f.open, runPostgreSQLDump); err != nil {
		t.Fatal(err)
	}
	target, err := f.j.createDockerRestoreDatabase(ctx, f.controller, 0, archiveOptions, f.open)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		preflightExec(t, f.db, `DROP DATABASE `+pq.QuoteIdentifier(databaseTargetName(target.TargetSHA256))+` WITH (FORCE)`)
	})
	m, err := f.j.restoreMaterial(ctx, 0, archiveOptions, options)
	if err != nil {
		t.Fatal(err)
	}
	return f, archiveOptions, m
}

func TestDatabaseRestoreAttemptJournal(t *testing.T) {
	options := databaseRestoreTestOptions(t)
	for _, mode := range []string{"commit", "before-ack", "before-writer", "password-replaced", "password-changed", "cancel-after-writer", "after-sql-error", "missing-record", "partial-record", "catalog-drift", "extra-object", "extra-session"} {
		t.Run(mode, func(t *testing.T) {
			f, archiveOptions, m := databaseRestoreAttemptFixture(t, options)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if mode == "extra-object" || mode == "extra-session" {
				db, err := database.Open(ctx, m.destination.Configuration.URL)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if mode == "extra-object" {
					preflightExec(t, db, `CREATE TABLE public.unowned(body text)`)
					db.Close()
				} else if db.PingContext(ctx) != nil {
					t.Fatal("cannot open outsider connection")
				}
			}
			calls := 0
			run := func(ctx context.Context, options DatabaseRestoreOptions, env []string, directory, name string, input *os.File,
				prepare func(context.Context, databaseProcessIdentity) error) error {
				calls++
				if mode == "before-writer" {
					return ErrDatabaseRestore
				}
				if mode == "password-replaced" || mode == "password-changed" {
					passwordName, _ := databaseRestorePasswordNames(m.binding.Target)
					path := filepath.Join(directory, passwordName)
					body, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					defer clear(body)
					if mode == "password-replaced" {
						if err := os.Rename(path, path+".original"); err != nil {
							return err
						}
					} else {
						body[0] ^= 1
					}
					if err := os.WriteFile(path, body, 0600); err != nil {
						return err
					}
				}
				childCtx, stop := context.WithCancel(ctx)
				defer stop()
				err := runPreparedPostgreSQLRestore(childCtx, options, env, directory, name, input,
					func(ctx context.Context, process databaseProcessIdentity) error {
						if err := prepare(ctx, process); err != nil {
							return err
						}
						if _, _, err := f.j.readDatabaseRestoreChain(ctx, m.binding.Target, "writer", ""); err != nil {
							return err
						}
						if mode == "cancel-after-writer" {
							stop()
						}
						return nil
					})
				if err == nil && mode == "after-sql-error" {
					return errors.New("lost completion response")
				}
				return err
			}
			var err error
			if mode == "commit" {
				_, err = f.j.restoreDockerDatabase(ctx, f.controller, 0, archiveOptions, options, f.open, run)
			} else {
				f.j.mu.Lock()
				_, _, err = f.j.createDatabaseRestoreAttempt(ctx, f.controller, 0, archiveOptions, options, f.open, run)
				f.j.mu.Unlock()
			}
			completed := mode == "commit" || mode == "before-ack" || mode == "missing-record" || mode == "partial-record" || mode == "catalog-drift"
			if (err == nil) != completed {
				t.Fatalf("unexpected attempt result: %v, pending=%+v", err, f.j.state.Pending)
			}
			if mode == "extra-object" || mode == "extra-session" {
				if calls != 0 || f.j.state.Pending.Sequence != 0 {
					t.Fatal("unsafe target reached restore intent/client")
				}
				return
			}
			if calls != 1 {
				t.Fatalf("restore invoked %d times", calls)
			}
			if mode == "missing-record" {
				if err := os.Remove(filepath.Join(f.directory, databaseRestoreRecordName(m.binding.Target, "completed"))); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "partial-record" {
				if err := os.Truncate(filepath.Join(f.directory, databaseRestoreRecordName(m.binding.Target, "completed")), 1); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "catalog-drift" {
				db, err := database.Open(ctx, m.destination.Configuration.URL)
				if err != nil {
					t.Fatal(err)
				}
				preflightExec(t, db, `UPDATE public.schema_migrations SET checksum=repeat('a',64) WHERE version=93`)
				db.Close()
			}
			f.controller.Close()
			f.j.Close()
			f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			f.controller, err = f.j.ResumeConfiguredDockerGatewayFence(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "commit" {
				observation, err := f.j.reconcileDockerDatabaseRestore(ctx, f.controller, 0, archiveOptions, options, f.open)
				expected := HostUnknown
				if mode == "before-ack" || mode == "after-sql-error" || mode == "missing-record" {
					expected = HostApplied
				} else if mode == "cancel-after-writer" {
					expected = HostNotApplied
				}
				if expected != HostUnknown {
					if err != nil || observation != expected || f.j.state.Pending.Sequence != 0 {
						t.Fatal("SQL outcome not reconciled", observation, err)
					}
				} else if err == nil || observation != HostUnknown || f.j.state.Pending.Effect != HostRestoreDatabase {
					t.Fatal("incomplete/changed restore acknowledged", observation, err)
				}
			}
			receipt, readErr := f.j.ReadDockerDatabaseRestore(ctx, 0, archiveOptions, options)
			if mode == "commit" || mode == "before-ack" || mode == "after-sql-error" || mode == "missing-record" {
				if readErr != nil || !receipt.Restored || receipt.CoherentBackup || receipt.ArchiveSHA256 != m.archive.Archive.SHA256 {
					t.Fatal("bad restore receipt", receipt, readErr)
				}
			} else if readErr == nil {
				t.Fatal("unresolved attempt exposed as completed")
			}
			if _, err := f.j.restoreDockerDatabase(ctx, f.controller, 0, archiveOptions, options, f.open, run); !errors.Is(err, ErrHostJournalState) || calls != 1 {
				t.Fatal("attempt repeated", err, calls)
			}
			db, err := database.Open(ctx, m.destination.Configuration.URL)
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.attempt_witness') IS NOT NULL`).Scan(&found); err != nil || found != (completed || mode == "after-sql-error") {
				t.Fatal("wrong SQL outcome", found, err)
			}
			if found {
				var value string
				if db.QueryRowContext(ctx, `SELECT body FROM public.attempt_witness`).Scan(&value) != nil || value != "private-restored-data" {
					t.Fatal("restored data differs")
				}
			}
			db.Close()
			requireRestoreRuntimeBlocked(t, m.destination.Configuration.URL)
			var original string
			if f.db.QueryRowContext(ctx, `SELECT body FROM public.attempt_witness`).Scan(&original) != nil || original != "private-restored-data" || f.posts.Load() != 1 {
				t.Fatal("attempt/recovery changed source or service")
			}
		})
	}
}

func TestDatabaseRestoreBootstrapRejectsExtraObjects(t *testing.T) {
	options := databaseRestoreTestOptions(t)
	_, _, m := databaseRestoreAttemptFixture(t, options)
	ctx := context.Background()
	db, err := database.Open(ctx, m.destination.Configuration.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := database.VerifyRuntimeRestoreBootstrap(ctx, conn, m.destination.BootstrapOID); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`CREATE TABLE public.extra(id int)`, `CREATE SCHEMA extra`,
		`CREATE FUNCTION public.extra() RETURNS int LANGUAGE sql AS 'SELECT 1'`, `CREATE DOMAIN public.extra AS int`,
		`CREATE INDEX extra ON public.release_runtime_control(epoch)`,
		`CREATE POLICY extra ON public.release_runtime_control USING(true)`} {
		if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
		if err := database.VerifyRuntimeRestoreBootstrap(ctx, conn, m.destination.BootstrapOID); err == nil {
			t.Fatalf("unexpected object accepted: %s", statement)
		}
		if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
			t.Fatal(err)
		}
		if err := database.VerifyRuntimeRestoreBootstrap(ctx, conn, m.destination.BootstrapOID); err != nil {
			t.Fatal("rollback did not preserve the bootstrap", err)
		}
	}
}
