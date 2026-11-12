//go:build linux || windows

package release

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
)

func TestDatabaseRestoreAttemptProcessHelper(t *testing.T) {
	mode := os.Getenv("RELEASE_RESTORE_ATTEMPT_HELPER_MODE")
	if mode == "" {
		return
	}
	options := databaseRestoreTestOptions(t)
	archiveOptions := databaseArchiveTestOptions(t)
	j, err := OpenHostJournal(os.Getenv("RELEASE_RESTORE_ATTEMPT_HELPER_DIRECTORY"), hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if mode == "cleanup-only" {
		if err := j.CleanupDockerDatabaseRestorePassword(ctx, 0, archiveOptions, options); err != nil {
			t.Fatal(err)
		}
		os.Exit(77)
	}
	retry := strings.HasPrefix(mode, "retry-")
	mode = strings.TrimPrefix(mode, "retry-")
	var approval DatabaseRestoreOutcomeReceipt
	if retry {
		approval, err = j.ReadDockerDatabaseRestoreOutcome(ctx, 0, archiveOptions, options)
		if err != nil {
			t.Fatal(err)
		}
	}
	controller, err := j.ResumeConfiguredDockerGatewayFence(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, options DatabaseRestoreOptions, env []string, directory, name string, input *os.File,
		prepare func(context.Context, databaseProcessIdentity) error) error {
		if mode == "before-writer" {
			os.Exit(77)
		}
		err := runPreparedPostgreSQLRestore(ctx, options, env, directory, name, input,
			func(ctx context.Context, process databaseProcessIdentity) error {
				if err := prepare(ctx, process); err != nil {
					return err
				}
				if mode == "writer" {
					os.Exit(77)
				}
				if mode == "resolved-not-applied" {
					return ErrDatabaseRestore
				}
				return nil
			})
		if err == nil && mode == "after-sql" {
			os.Exit(77)
		}
		if err == nil && mode == "resolved-applied" {
			return ErrDatabaseRestore
		}
		return err
	}
	open := archiveProcessObserver(os.Getenv("RELEASE_RESTORE_ATTEMPT_HELPER_ENGINE"))
	if mode == "acknowledged" {
		if retry {
			_, err = j.retryDockerDatabase(ctx, controller, 0, archiveOptions, options, approval, open, run)
		} else {
			_, err = j.restoreDockerDatabase(ctx, controller, 0, archiveOptions, options, open, run)
		}
	} else {
		j.mu.Lock()
		if retry {
			_, _, err = j.createDatabaseRestoreRetry(ctx, controller, 0, archiveOptions, options, approval, open, run)
		} else {
			_, _, err = j.createDatabaseRestoreAttempt(ctx, controller, 0, archiveOptions, options, open, run)
		}
	}
	if mode == "resolved-applied" || mode == "resolved-not-applied" {
		if err == nil {
			t.Fatal("expected incomplete writer result")
		}
		m, materialErr := j.latestRestoreMaterial(ctx, 0, archiveOptions, options)
		if materialErr != nil {
			t.Fatal(materialErr)
		}
		record, digest, readErr := j.readDatabaseRestoreRecoveryHead(ctx, m.binding.Target, "")
		if readErr != nil || record.Phase != "writer" {
			t.Fatal("missing writer", readErr)
		}
		err = withDatabaseRestoreOwnership(ctx, controller, m, open, func(source, target *sql.Conn, _ *dockerObserver) error {
			_, _, err := j.resolveDatabaseRestore(ctx, source, target, m, record, digest)
			return err
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(77)
}

func TestDatabaseRestoreAttemptProcessCrash(t *testing.T) {
	options := databaseRestoreTestOptions(t)
	var modes []string
	for _, mode := range []string{"before-writer", "writer", "after-sql", "completed", "acknowledged", "resolved-applied", "resolved-not-applied"} {
		modes = append(modes, mode, "retry-"+mode)
	}
	for _, testMode := range modes {
		t.Run(testMode, func(t *testing.T) {
			retry := strings.HasPrefix(testMode, "retry-")
			mode := strings.TrimPrefix(testMode, "retry-")
			var f *dockerStopFixture
			var archiveOptions DatabaseArchiveOptions
			var m databaseRestoreMaterial
			if retry {
				var approval DatabaseRestoreOutcomeReceipt
				f, archiveOptions, _, m, approval = databaseRestoreRetryFixture(t)
				m.binding.PreviousAttempt, m.binding.PreviousReceipt = approval.TargetSHA256, approval.EvidenceSHA256
				m.binding.Target = databaseRestoreRetryHash(m.binding.Target, approval.TargetSHA256, approval.EvidenceSHA256)
			} else {
				f, archiveOptions, m = databaseRestoreAttemptFixture(t, options)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			d, err := f.open(m.plan.data.EngineEndpoint)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := d.transport.DialContext(ctx, "tcp", "docker")
			if err != nil {
				t.Fatal(err)
			}
			address := probe.RemoteAddr().String()
			probe.Close()
			d.close()
			f.controller.Close()
			f.j.Close()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDatabaseRestoreAttemptProcessHelper$", "-test.v")
			command.Env = append(os.Environ(), "RELEASE_RESTORE_ATTEMPT_HELPER_MODE="+testMode,
				"RELEASE_RESTORE_ATTEMPT_HELPER_DIRECTORY="+f.directory, "RELEASE_RESTORE_ATTEMPT_HELPER_ENGINE="+address)
			command.WaitDelay = 5 * time.Second
			configureReleaseCommand(command)
			output, err := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 77 || bytes.Contains(output, []byte("WARNING: DATA RACE")) {
				t.Fatalf("restore helper missed crash boundary: %v\n%s", err, output)
			}
			f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			f.controller, err = f.j.ResumeConfiguredDockerGatewayFence(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "before-writer" {
				writer, _, err := f.j.readDatabaseRestoreChain(ctx, m.binding.Target, "writer", "")
				if err != nil {
					t.Fatal("durable writer identity missing", err)
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					exited, err := databaseProcessExited(writer.Process)
					if err != nil {
						t.Fatal(err)
					}
					if exited {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("restore child survived coordinator death")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			if mode != "acknowledged" {
				observation, err := f.j.reconcileDockerDatabaseRestore(ctx, f.controller, 0, archiveOptions, options, f.open)
				if mode == "completed" || mode == "resolved-applied" || mode == "resolved-not-applied" {
					expected := HostApplied
					if mode == "resolved-not-applied" {
						expected = HostNotApplied
					}
					if err != nil || observation != expected || f.j.state.Pending.Sequence != 0 {
						t.Fatal("completed record not recovered", observation, err)
					}
				} else if err == nil || observation != HostUnknown || f.j.state.Pending.Effect != HostRestoreDatabase {
					t.Fatal("uncertain restore outcome was adopted", observation, err)
				}
			}
			_, readErr := f.j.ReadDockerDatabaseRestore(ctx, 0, archiveOptions, options)
			if (readErr == nil) != (mode == "completed" || mode == "acknowledged" || mode == "resolved-applied") {
				t.Fatal("incorrect historical restore status", readErr)
			}
			passwordName, _ := databaseRestorePasswordNames(m.binding.Target)
			if mode == "before-writer" {
				if err := f.j.CleanupDockerDatabaseRestorePassword(ctx, 0, archiveOptions, options); err == nil {
					t.Fatal("unrecorded writer credential was adopted")
				}
				if _, err := f.j.archiveProof(ctx, passwordName, maxDockerDatabaseURLBytes*2); err != nil {
					t.Fatal("unproven credential removed", err)
				}
			}
			if mode == "writer" || mode == "after-sql" {
				pending := f.j.state.Pending
				if _, err := f.j.archiveProof(ctx, passwordName, maxDockerDatabaseURLBytes*2); err != nil {
					t.Fatal("crash did not leave the original password", err)
				}
				f.controller.Close()
				f.j.Close()
				cleanup := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDatabaseRestoreAttemptProcessHelper$", "-test.v")
				cleanup.Env = append(os.Environ(), "RELEASE_RESTORE_ATTEMPT_HELPER_MODE=cleanup-only",
					"RELEASE_RESTORE_ATTEMPT_HELPER_DIRECTORY="+f.directory)
				cleanup.WaitDelay = 5 * time.Second
				configureReleaseCommand(cleanup)
				output, err := cleanup.CombinedOutput()
				if !errors.As(err, &exit) || exit.ExitCode() != 77 || bytes.Contains(output, []byte("WARNING: DATA RACE")) {
					t.Fatalf("cleanup helper missed crash boundary: %v\n%s", err, output)
				}
				f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
				if err != nil {
					t.Fatal(err)
				}
				f.controller, err = f.j.ResumeConfiguredDockerGatewayFence(ctx, 0)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.j.CleanupDockerDatabaseRestorePassword(ctx, 0, archiveOptions, options); err != nil {
					t.Fatal("cleanup could not resume after process exit", err)
				}
				if _, err := f.j.root.Lstat(passwordName); !errors.Is(err, os.ErrNotExist) || f.j.state.Pending != pending {
					t.Fatal("cleanup did not preserve the unknown SQL outcome", err)
				}
				expected := HostApplied
				if mode == "writer" {
					expected = HostNotApplied
				}
				if outcome, err := f.j.reconcileDockerDatabaseRestore(ctx, f.controller, 0, archiveOptions, options, f.open); err != nil || outcome != expected || f.j.state.Pending.Sequence != 0 {
					t.Fatal("live SQL outcome not recovered after cleanup", outcome, err)
				}
				_, readErr := f.j.ReadDockerDatabaseRestore(ctx, 0, archiveOptions, options)
				if (readErr == nil) != (expected == HostApplied) {
					t.Fatal("incorrect resolved receipt", readErr)
				}
			}
			if _, err := f.j.restoreDockerDatabase(ctx, f.controller, 0, archiveOptions, options, f.open, runPreparedPostgreSQLRestore); !errors.Is(err, ErrHostJournalState) {
				t.Fatal("crashed attempt could be repeated", err)
			}
			db, err := database.Open(ctx, m.destination.Configuration.URL)
			if err != nil {
				t.Fatal(err)
			}
			var exists bool
			if db.QueryRowContext(ctx, `SELECT to_regclass('public.attempt_witness') IS NOT NULL`).Scan(&exists) != nil || exists != (mode != "before-writer" && mode != "writer" && mode != "resolved-not-applied") {
				t.Fatal("SQL outcome does not match crash boundary")
			}
			db.Close()
			requireRestoreRuntimeBlocked(t, m.destination.Configuration.URL)
			if f.posts.Load() != 1 {
				t.Fatal("restore crash/recovery changed service")
			}
		})
	}
}
