//go:build linux || windows

package release

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"github.com/lib/pq"
)

type databaseRestoreProcessFixture struct {
	Options      DatabaseRestoreOptions
	Environment  []string
	Directory    string
	DatabaseName string
	ArchivePath  string
	IdentityPath string
}

func TestDatabaseRestoreProcessHelper(t *testing.T) {
	path := os.Getenv("RELEASE_RESTORE_HELPER_FILE")
	if path == "" {
		return
	}
	body, err := os.ReadFile(path)
	var fixture databaseRestoreProcessFixture
	if err != nil || json.Unmarshal(body, &fixture) != nil {
		os.Exit(51)
	}
	input, err := os.Open(fixture.ArchivePath)
	if err != nil {
		os.Exit(52)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if runPreparedPostgreSQLRestore(ctx, fixture.Options, fixture.Environment, fixture.Directory, fixture.DatabaseName, input,
		func(_ context.Context, identity databaseProcessIdentity) error {
			body, err := json.Marshal(identity)
			if err != nil {
				return err
			}
			return os.WriteFile(fixture.IdentityPath, body, 0600)
		}) != nil {
		os.Exit(53)
	}
	os.Exit(0)
}

func TestDatabaseRestorePreparedTarget(t *testing.T) {
	archiveOptions := databaseArchiveTestOptions(t)
	path := os.Getenv("TEST_RELEASE_PG_RESTORE")
	if !filepath.IsAbs(path) {
		t.Fatal("explicit TEST_RELEASE_PG_RESTORE is required")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	options := DatabaseRestoreOptions{Tool: PostgreSQLClientTool{Path: path, SHA256: hostRecoveryDigest(body)}, TimeoutSeconds: 30}
	for _, mode := range []string{"commit", "prepare-reject", "archive-error", "cancel-during-ddl", "parent-death-during-ddl"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			preflightExec(t, f.db, `CREATE TABLE public.restore_witness(id bigserial PRIMARY KEY, body text NOT NULL)`)
			preflightExec(t, f.db, `INSERT INTO public.restore_witness(body) VALUES('original-private-value')`)
			prepareDatabaseArchiveFixture(t, f)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			receipt, err := f.j.archiveDockerDatabase(ctx, f.controller, 0, archiveOptions, f.open, runPostgreSQLDump)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := f.j.databaseArchiveReceipt(receipt.TargetSHA256)
			if err != nil {
				t.Fatal(err)
			}
			payload, _, err := f.j.readDatabaseArchiveMetadata(ctx, receipt.TargetSHA256, digest)
			if err != nil {
				t.Fatal(err)
			}
			spool, err := f.j.databaseRestoreSpool(ctx, payload)
			if err != nil {
				t.Fatal(err)
			}
			defer spool.Close()
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				t.Fatal(err)
			}
			name := "gateway_restore_" + hex.EncodeToString(nonce[:])
			preflightExec(t, f.db, `CREATE DATABASE `+pq.QuoteIdentifier(name)+` TEMPLATE template0`)
			t.Cleanup(func() {
				if _, err := f.db.Exec(`DROP DATABASE ` + pq.QuoteIdentifier(name) + ` WITH (FORCE)`); err != nil {
					t.Error(err)
				}
			})
			u, _ := url.Parse(dockerConfigurationFixtureURL(t, f))
			u.Path = "/" + name
			configuration, err := observeDockerDatabaseConfiguration(u.String())
			if err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(ctx, configuration.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			probe, err := database.BeginRuntimeBinding(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := probe.DatabaseIdentity(ctx)
			probe.Close()
			if err != nil {
				t.Fatal(err)
			}
			_, request, _, _, err := f.j.archiveMaterial(0)
			if err != nil {
				t.Fatal(err)
			}
			controller, bootstrapOID, err := database.BootstrapRuntimeRestore(ctx, db, request, identity, f.plan.data.Observed.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			requireRestoreRuntimeBlocked(t, configuration.URL)
			password, err := databaseArchivePassword(configuration)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(password)
			passwordName := "prepared-restore.pgpass"
			if err := f.j.writeArchivePassword(passwordName, password); err != nil {
				t.Fatal(err)
			}
			passwordProof, err := f.j.archiveProof(ctx, passwordName, maxDockerDatabaseURLBytes*2)
			if err != nil {
				t.Fatal(err)
			}
			defer f.j.removeArchivePassword(passwordName, passwordProof)
			env, err := databaseRestoreEnvironment(configuration, f.directory, filepath.Join(f.directory, passwordName), filepath.Join(f.directory, "absent-restore"))
			if err != nil || checkPostgreSQLRestoreVersion(ctx, options.Tool, env, f.directory, payload.PostgresMajor) != nil {
				t.Fatal("restore client configuration/version failed", err)
			}
			if _, err := observePostgreSQLClientTool(options.Tool); err != nil {
				t.Fatal(err)
			}
			if mode == "archive-error" {
				broken, err := os.CreateTemp(t.TempDir(), "truncated")
				if err != nil {
					t.Fatal(err)
				}
				defer broken.Close()
				if _, err := io.CopyN(broken, spool, payload.Archive.Bytes/2); err != nil {
					t.Fatal(err)
				}
				if _, err := broken.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
				spool = broken
			}
			runCtx, stop := context.WithCancel(ctx)
			defer stop()
			if mode == "cancel-during-ddl" || mode == "parent-death-during-ddl" {
				blocker, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback()
				if _, err := blocker.ExecContext(ctx, `LOCK TABLE public.release_runtime_control IN ACCESS SHARE MODE`); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				var owner *exec.Cmd
				identityPath := filepath.Join(f.directory, "restore-writer-identity.json")
				if mode == "parent-death-during-ddl" {
					archiveName, _, _, _ := databaseArchiveNames(receipt.TargetSHA256)
					fixture := databaseRestoreProcessFixture{options, env, f.directory, name, filepath.Join(f.directory, archiveName), identityPath}
					body, _ := json.Marshal(fixture)
					fixturePath := filepath.Join(f.directory, "restore-process-fixture.json")
					if err := os.WriteFile(fixturePath, body, 0600); err != nil {
						t.Fatal(err)
					}
					owner = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDatabaseRestoreProcessHelper$")
					owner.Env = []string{"RELEASE_RESTORE_HELPER_FILE=" + fixturePath, "GORACE=atexit_sleep_ms=0"}
					if runtime.GOOS == "windows" {
						owner.Env = append(owner.Env, "SystemRoot="+os.Getenv("SystemRoot"))
					}
					configureReleaseCommand(owner)
					if err := owner.Start(); err != nil {
						t.Fatal(err)
					}
					defer func() { _ = owner.Process.Kill(); _ = owner.Wait() }()
				} else {
					go func() { done <- runPostgreSQLRestore(runCtx, options, env, f.directory, name, spool) }()
				}
				deadline := time.Now().Add(10 * time.Second)
				waiting := false
				for time.Now().Before(deadline) {
					if db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
					 AND application_name='gateway-release-restore' AND wait_event_type='Lock')`).Scan(&waiting) != nil {
						t.Fatal("cannot observe restore client lock")
					}
					if waiting {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if owner != nil {
					var writerIdentity databaseProcessIdentity
					body, err := os.ReadFile(identityPath)
					if err != nil || json.Unmarshal(body, &writerIdentity) != nil || !writerIdentity.valid() {
						t.Fatal("restore child identity not recorded before DDL", err)
					}
					if exited, err := databaseProcessExited(writerIdentity); err != nil || exited {
						t.Fatal("restore child not alive at DDL barrier", err)
					}
					watch := databaseWriterTestWatch(t, writerIdentity.PID)
					if err := owner.Process.Kill(); err != nil {
						t.Fatal(err)
					}
					if err := owner.Wait(); err == nil {
						t.Fatal("restore coordinator was not forcibly terminated")
					}
					watch()
					if exited, err := databaseProcessExited(writerIdentity); err != nil || !exited {
						t.Fatal("restore child exit not proven after owner death", err)
					}
				} else {
					stop()
					if err := <-done; !errors.Is(err, ErrDatabaseRestore) {
						t.Fatalf("DDL cancellation succeeded: %v", err)
					}
				}
				if !waiting {
					t.Fatal("client was not blocked during DDL")
				}
				if err := blocker.Rollback(); err != nil {
					t.Fatal(err)
				}
				deadline = time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
					 AND application_name='gateway-release-restore')`).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if !waiting {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if waiting {
					t.Fatal("restore SQL session survived client/parent termination")
				}
			} else {
				var writerIdentity databaseProcessIdentity
				calls := 0
				err = runPreparedPostgreSQLRestore(runCtx, options, env, f.directory, name, spool,
					func(_ context.Context, identity databaseProcessIdentity) error {
						calls++
						writerIdentity = identity
						if exited, err := databaseProcessExited(identity); err != nil || exited {
							t.Error("pg_restore not alive at preparation barrier", err)
							return ErrDatabaseRestore
						}
						if offset, err := spool.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
							t.Error("pg_restore read archive before preparation", err)
							return ErrDatabaseRestore
						}
						if mode == "prepare-reject" {
							return errors.New("private preparation failure")
						}
						return nil
					})
				if calls != 1 {
					t.Fatalf("restore preparation calls: %d", calls)
				}
				if exited, err := databaseProcessExited(writerIdentity); err != nil || !exited {
					t.Fatal("restore returned before recorded child exit", err)
				}
				if mode == "prepare-reject" {
					if offset, err := spool.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
						t.Fatal("preparation refusal consumed archive", err)
					}
				}
				if (err == nil) != (mode == "commit") {
					t.Fatalf("restore result: %v", err)
				}
			}
			if err := controller.WithQuiescentConnection(ctx, func(conn *sql.Conn) error {
				var oid int64
				if err := conn.QueryRowContext(ctx, `SELECT 'public.release_runtime_control'::regclass::oid::bigint`).Scan(&oid); err != nil {
					return err
				}
				if (oid != bootstrapOID) != (mode == "commit") {
					t.Fatal("bootstrap relation does not identify atomic restore outcome")
				}
				var witness sql.NullString
				if err := conn.QueryRowContext(ctx, `SELECT to_regclass('public.restore_witness')::text`).Scan(&witness); err != nil {
					return err
				}
				if witness.Valid != (mode == "commit") {
					t.Fatal("partial restore became visible")
				}
				if mode == "commit" {
					var value string
					if err := conn.QueryRowContext(ctx, `SELECT body FROM public.restore_witness WHERE id=1`).Scan(&value); err != nil || value != "original-private-value" {
						t.Fatal("restored data differs", err)
					}
					var migrations int
					if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM public.schema_migrations`).Scan(&migrations); err != nil || migrations < 93 {
						t.Fatal("restored migration catalog missing", err)
					}
				}
				return nil
			}); err != nil {
				t.Fatal("controller did not survive transactional fence replacement", err)
			}
			controller.Close()
			requireRestoreRuntimeBlocked(t, configuration.URL)
			var value string
			if err := f.db.QueryRowContext(ctx, `SELECT body FROM public.restore_witness WHERE id=1`).Scan(&value); err != nil || value != "original-private-value" {
				t.Fatal("restore changed original data", err)
			}
			state, err := database.ReadRuntimeFence(ctx, f.db)
			if err != nil || !state.Active {
				t.Fatal("restore opened original runtime admission", err)
			}
		})
	}
}

func requireRestoreRuntimeBlocked(t *testing.T, dsn string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db, err := database.OpenRuntime(ctx, dsn)
	if db != nil {
		db.Close()
	}
	if !errors.Is(err, database.ErrRuntimeFenced) {
		t.Fatalf("prepared/restored target admitted runtime: %v", err)
	}
}
