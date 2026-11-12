package release

import (
	"bytes"
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
	"strings"
	"testing"

	"ai-access-gateway/internal/database"
	"github.com/lib/pq"
)

func databaseArchiveTestOptions(t *testing.T) DatabaseArchiveOptions {
	t.Helper()
	path := os.Getenv("TEST_RELEASE_PG_DUMP")
	if path == "" {
		t.Skip("TEST_RELEASE_PG_DUMP is not set; real PostgreSQL client tools are required")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("test pg_dump must be explicit")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return DatabaseArchiveOptions{Tool: PostgreSQLDumpTool{Path: path, SHA256: hostRecoveryDigest(body)}, MaxBytes: 16 << 20, TimeoutSeconds: 60}
}

func prepareDatabaseArchiveFixture(t *testing.T, f *dockerStopFixture) {
	prepareDatabaseArchiveFixtureURL(t, f, dockerConfigurationFixtureURL(t, f))
}

func prepareDatabaseArchiveFixtureURL(t *testing.T, f *dockerStopFixture, raw string) {
	t.Helper()
	ctx := context.Background()
	if err := f.j.PrepareDockerDatabaseConfiguration(ctx, raw); err != nil {
		t.Fatal(err)
	}
	var err error
	f.controller, err = f.j.configuredDockerGatewayFence(ctx, "docker-binding-session-secret", 0, false, f.open)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.j.dockerStop(ctx, f.controller, 0, 2, false, f.open); err != nil || result != HostApplied {
		t.Fatal(result, err)
	}
}

func TestDatabaseArchiveRoundTrip(t *testing.T) {
	options := databaseArchiveTestOptions(t)
	f := newDockerStopFixtureWithFence(t, false)
	preflightExec(t, f.db, `CREATE TABLE public.archive_witness(id bigserial PRIMARY KEY, body text NOT NULL)`)
	preflightExec(t, f.db, `INSERT INTO public.archive_witness(body) VALUES('before-snapshot-private-value')`)
	prepareDatabaseArchiveFixture(t, f)
	ctx := context.Background()
	var snapshotObserved bool
	dump := func(ctx context.Context, options DatabaseArchiveOptions, environment []string, directory, snapshot string, writer io.Writer) error {
		state, history, err := f.j.readOwnedArchiveHistory()
		if err != nil || state.Pending.Effect != HostArchiveDatabase || !bytes.Contains(history, []byte(`"effect":"archive_database"`)) {
			t.Fatal("dump dispatched before durable intent", err)
		}
		// This deliberately unmanaged SQL writer proves that pg_dump imports the
		// owner's snapshot instead of taking a later, independent snapshot.
		preflightExec(t, f.db, `UPDATE public.archive_witness SET body='after-snapshot-private-value' WHERE id=1`)
		snapshotObserved = postgresSnapshotID.MatchString(snapshot)
		return runPostgreSQLDump(ctx, options, environment, directory, snapshot, writer)
	}
	receipt, err := f.j.archiveDockerDatabase(ctx, f.controller, 0, options, f.open, dump)
	if err != nil || !snapshotObserved || receipt.Bytes == 0 || receipt.CoherentBackup || receipt.RestoreTested || receipt.Scope != databaseArchiveScope {
		t.Fatal("database archive", receipt, err)
	}
	if _, err := f.j.archiveDockerDatabase(ctx, f.controller, 0, options, f.open, runPostgreSQLDump); !errors.Is(err, ErrHostJournalState) {
		t.Fatal("duplicate dump allowed", err)
	}
	changed := options
	changed.MaxBytes++
	if _, err := f.j.archiveDockerDatabase(ctx, f.controller, 0, changed, f.open, runPostgreSQLDump); !errors.Is(err, ErrHostJournalState) {
		t.Fatal("second archive with changed options allowed", err)
	}
	archiveName, _, passwordName, absentName := databaseArchiveNames(receipt.TargetSHA256)
	for _, name := range []string{passwordName, absentName} {
		if _, err := os.Stat(filepath.Join(f.directory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("credential/ambient file retained after successful dump")
		}
	}
	state, history, err := f.j.readOwnedArchiveHistory()
	if err != nil || state.Pending.Sequence != 0 {
		t.Fatal("archive not acknowledged", err)
	}
	public, _ := json.Marshal(receipt)
	u, _ := url.Parse(dockerConfigurationFixtureURL(t, f))
	password, _ := u.User.Password()
	for _, secret := range []string{password, u.String(), f.plan.data.Observed.Database.DatabaseName, "before-snapshot-private-value", "after-snapshot-private-value"} {
		if bytes.Contains(history, []byte(secret)) || bytes.Contains(public, []byte(secret)) {
			t.Fatal("private database material leaked into receipt/journal")
		}
	}
	verifyDatabaseArchiveRestore(t, f, filepath.Join(f.directory, archiveName))
	f.controller.Close()
	f.j.Close()
	f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	read, err := f.j.ReadDockerDatabaseArchive(ctx, 0, options)
	if err != nil || read != receipt {
		t.Fatal("reopen changed archive", err)
	}
	active, err := database.ReadRuntimeFence(ctx, f.db)
	if err != nil || !active.Active || f.posts.Load() != 1 {
		t.Fatal("archive changed service or admission", err)
	}
}

// Read through the existing journal owner handle on Windows, where another
// handle is correctly excluded by the byte-range lock.
func (j *HostJournal) readOwnedArchiveHistory() (HostJournalSnapshot, []byte, error) {
	info, err := j.file.Stat()
	if err != nil {
		return HostJournalSnapshot{}, nil, err
	}
	body, err := io.ReadAll(io.NewSectionReader(j.file, 0, info.Size()))
	return j.state, body, err
}

func verifyDatabaseArchiveRestore(t *testing.T, f *dockerStopFixture, archivePath string) {
	t.Helper()
	tool := os.Getenv("TEST_RELEASE_PG_RESTORE")
	if tool == "" || !filepath.IsAbs(tool) {
		t.Fatal("real archive gate requires explicit TEST_RELEASE_PG_RESTORE")
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "release_archive_restore_" + hex.EncodeToString(suffix[:])
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
	body, err := databaseArchivePassword(configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(body)
	passwordName := "restore-test.pgpass"
	if err := f.j.writeArchivePassword(passwordName, body); err != nil {
		t.Fatal(err)
	}
	proof, err := f.j.archiveProof(context.Background(), passwordName, maxDockerDatabaseURLBytes*2)
	if err != nil {
		t.Fatal(err)
	}
	defer f.j.removeArchivePassword(passwordName, proof)
	environment, err := databaseArchiveEnvironment(configuration, f.directory, filepath.Join(f.directory, passwordName), filepath.Join(f.directory, "absent-restore"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(tool, "--exit-on-error", "--single-transaction", "--dbname="+name, archivePath)
	command.Env, command.Dir = environment, f.directory
	command.Stdout, command.Stderr = io.Discard, io.Discard
	configureReleaseCommand(command)
	if err := command.Run(); err != nil {
		t.Fatal("actual archive could not be restored", err)
	}
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var actual string
	if err := db.QueryRow(`SELECT body FROM public.archive_witness WHERE id=1`).Scan(&actual); err != nil || actual != "before-snapshot-private-value" {
		t.Fatal("restored data did not come from the exported snapshot", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM public.schema_migrations`).Scan(&count); err != nil || count != len(database.ReleaseMigrations()) {
		t.Fatal("migration catalog not restored", err)
	}
	active, err := database.ReadRuntimeFence(context.Background(), db)
	if err != nil || !active.Active {
		t.Fatal("restored archive lost its active admission fence", err)
	}
}

func TestDatabaseArchiveRefusalsBeforeIntent(t *testing.T) {
	options := databaseArchiveTestOptions(t)
	for _, mode := range []string{"tool_hash", "configuration", "catalog", "stopped_identity", "runtime_busy"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			var runtime *sql.DB
			if mode == "runtime_busy" {
				var err error
				runtime, err = database.OpenRuntime(context.Background(), dockerConfigurationFixtureURL(t, f))
				if err != nil {
					t.Fatal(err)
				}
				defer runtime.Close()
				if err := runtime.Ping(); err != nil {
					t.Fatal(err)
				}
			}
			prepareDatabaseArchiveFixture(t, f)
			selected := options
			switch mode {
			case "tool_hash":
				selected.Tool.SHA256 = strings.Repeat("f", 64)
			case "configuration":
				if err := os.Remove(filepath.Join(f.directory, f.j.databaseConfigName())); err != nil {
					t.Fatal(err)
				}
			case "catalog":
				preflightExec(t, f.db, `DELETE FROM public.schema_migrations WHERE version=1`)
			case "stopped_identity":
				f.mode.Store("restarted")
			}
			called := false
			_, err := f.j.archiveDockerDatabase(context.Background(), f.controller, 0, selected, f.open,
				func(context.Context, DatabaseArchiveOptions, []string, string, string, io.Writer) error {
					called = true
					return nil
				})
			if err == nil || called {
				t.Fatal("unsafe archive dispatched")
			}
			state, _, err := f.j.OperationHistory()
			if err != nil || state.Pending.Sequence != 0 {
				t.Fatal("preflight refusal created an intent", err)
			}
		})
	}
}

func TestDatabaseArchiveEscapedCredentials(t *testing.T) {
	options := databaseArchiveTestOptions(t)
	f := newDockerStopFixtureWithFence(t, false)
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	role := `archive:role\` + hex.EncodeToString(suffix[:])
	password := `fixture:password\with'quote`
	preflightExec(t, f.db, `CREATE ROLE `+pq.QuoteIdentifier(role)+` LOGIN SUPERUSER PASSWORD `+pq.QuoteLiteral(password))
	t.Cleanup(func() {
		if f.controller != nil {
			f.controller.Close()
		}
		if _, err := f.db.Exec(`DROP ROLE ` + pq.QuoteIdentifier(role)); err != nil {
			t.Error(err)
		}
	})
	u, _ := url.Parse(dockerConfigurationFixtureURL(t, f))
	u.User = url.UserPassword(role, password)
	prepareDatabaseArchiveFixtureURL(t, f, u.String())
	if _, err := f.j.archiveDockerDatabase(context.Background(), f.controller, 0, options, f.open, runPostgreSQLDump); err != nil {
		t.Fatal("pgpass did not preserve explicit escaped credentials", err)
	}
}

func TestDatabaseArchiveUnknownOutput(t *testing.T) {
	options := databaseArchiveTestOptions(t)
	for _, mode := range []string{"size", "bad_snapshot", "connection_lost", "partial", "password_error", "ambient_file", "package_changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			prepareDatabaseArchiveFixture(t, f)
			selected := options
			if mode == "size" {
				selected.MaxBytes = 1024
			}
			dump := func(ctx context.Context, options DatabaseArchiveOptions, environment []string, directory, snapshot string, output io.Writer) error {
				if mode == "partial" {
					_, _ = output.Write([]byte("PGDMPpartial"))
					return errors.New("private child failure")
				}
				if mode == "bad_snapshot" {
					snapshot = "FFFFFFFF-FFFFFFFF-1"
				}
				if mode == "password_error" {
					environment = append(environment, "PGPASSWORD=wrong-private-password")
				}
				err := runPostgreSQLDump(ctx, options, environment, directory, snapshot, output)
				if mode == "connection_lost" {
					preflightExec(t, f.db, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND mode='ExclusiveLock' AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`)
				}
				if mode == "ambient_file" {
					_, _, _, absent := databaseArchiveNames(databaseArchiveTarget(f.plan, 0, options))
					if err := os.WriteFile(filepath.Join(directory, absent), []byte("unexpected libpq material"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "package_changed" {
					_, manifest, err := observeHostPackage(HostPackageReference{f.plan.data.Previous.Directory, f.plan.data.Previous.ManifestSHA256})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(f.plan.data.Previous.Directory, manifest.Source.Name), []byte("changed package"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return err
			}
			_, err := f.j.archiveDockerDatabase(context.Background(), f.controller, 0, selected, f.open, dump)
			if !errors.Is(err, ErrDatabaseArchive) || strings.Contains(err.Error(), "private") {
				t.Fatal("dump failure not redacted", err)
			}
			state, err := f.j.Snapshot()
			if err != nil || state.Pending.Effect != HostArchiveDatabase {
				t.Fatal("failed dump lost unknown outcome", err)
			}
			if mode == "ambient_file" {
				_, metadata, _, absent := databaseArchiveNames(state.Pending.TargetSHA256)
				if _, err := os.Stat(filepath.Join(f.directory, metadata)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("invalid completion received a protected marker")
				}
				if err := os.Remove(filepath.Join(f.directory, absent)); err != nil {
					t.Fatal(err)
				}
			}
			f.controller.Close()
			f.controller, err = f.j.ResumeConfiguredDockerGatewayFence(context.Background(), 0)
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.j.reconcileDockerDatabaseArchive(context.Background(), f.controller, 0, selected, f.open)
			if err == nil || result != HostUnknown {
				t.Fatal("partial output accepted", result, err)
			}
			if _, err := f.j.archiveDockerDatabase(context.Background(), f.controller, 0, selected, f.open, runPostgreSQLDump); !errors.Is(err, ErrHostJournalState) {
				t.Fatal("unknown dump was retried", err)
			}
			active, err := database.ReadRuntimeFence(context.Background(), f.db)
			if err != nil || !active.Active {
				t.Fatal("failed archive unfenced DB", err)
			}
		})
	}
}
