//go:build linux || windows

package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
)

func rejectPreparedRestore(ctx context.Context, options DatabaseRestoreOptions, env []string, directory, name string, input *os.File,
	prepare func(context.Context, databaseProcessIdentity) error) error {
	return runPreparedPostgreSQLRestore(ctx, options, env, directory, name, input, func(ctx context.Context, process databaseProcessIdentity) error {
		if err := prepare(ctx, process); err != nil {
			return err
		}
		return ErrDatabaseRestore
	})
}

func databaseRestoreRetryFixture(t *testing.T) (*dockerStopFixture, DatabaseArchiveOptions, DatabaseRestoreOptions, databaseRestoreMaterial, DatabaseRestoreOutcomeReceipt) {
	t.Helper()
	options := databaseRestoreTestOptions(t)
	f, archive, m := databaseRestoreAttemptFixture(t, options)
	ctx := context.Background()
	if _, err := f.j.restoreDockerDatabase(ctx, f.controller, 0, archive, options, f.open, rejectPreparedRestore); err == nil {
		t.Fatal("first attempt unexpectedly succeeded")
	}
	if outcome, err := f.j.reconcileDockerDatabaseRestore(ctx, f.controller, 0, archive, options, f.open); err != nil || outcome != HostNotApplied {
		t.Fatal("first rollback not resolved", outcome, err)
	}
	receipt, err := f.j.ReadDockerDatabaseRestoreOutcome(ctx, 0, archive, options)
	if err != nil || receipt.Outcome != HostNotApplied || receipt.TargetSHA256 != m.binding.Target {
		t.Fatal("missing exact rollback receipt", receipt, err)
	}
	return f, archive, options, m, receipt
}

func TestDatabaseRestoreRetry(t *testing.T) {
	for _, mode := range []string{"commit", "new-options", "new-tool", "lost-sql-response", "before-ack", "rollback", "before-writer",
		"wrong-receipt", "wrong-target", "wrong-outcome", "extra-object", "extra-session", "prior-partial", "prior-writer-missing", "prior-conflict"} {
		t.Run(mode, func(t *testing.T) {
			f, archive, options, original, approval := databaseRestoreRetryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			originalApproval := approval
			originalFiles := make(map[string]string)
			for _, phase := range []string{"prepared", "writer", "resolved"} {
				path := filepath.Join(f.directory, databaseRestoreRecordName(original.binding.Target, phase))
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				originalFiles[path] = hostRecoveryDigest(body)
			}
			switch mode {
			case "new-options":
				options.TimeoutSeconds++
			case "new-tool":
				body, err := os.ReadFile(options.Tool.Path)
				if err != nil {
					t.Fatal(err)
				}
				options.Tool.Path = filepath.Join(t.TempDir(), "pg_restore")
				if err := os.WriteFile(options.Tool.Path, body, 0700); err != nil {
					t.Fatal(err)
				}
			case "wrong-receipt":
				approval.EvidenceSHA256 = strings.Repeat("a", 64)
			case "wrong-target":
				approval.TargetSHA256 = strings.Repeat("b", 64)
			case "wrong-outcome":
				approval.Outcome = HostApplied
			case "prior-partial":
				if err := os.Truncate(filepath.Join(f.directory, databaseRestoreRecordName(original.binding.Target, "resolved")), 1); err != nil {
					t.Fatal(err)
				}
			case "prior-writer-missing":
				if err := os.Remove(filepath.Join(f.directory, databaseRestoreRecordName(original.binding.Target, "writer"))); err != nil {
					t.Fatal(err)
				}
			case "prior-conflict":
				if err := os.WriteFile(filepath.Join(f.directory, databaseRestoreRecordName(original.binding.Target, "completed")), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "extra-object", "extra-session":
				db, err := database.Open(ctx, original.destination.Configuration.URL)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if mode == "extra-object" {
					preflightExec(t, db, `CREATE TABLE public.unowned(body text)`)
					db.Close()
				} else if db.PingContext(ctx) != nil {
					t.Fatal("cannot open outsider session")
				}
			}
			calls := 0
			run := func(ctx context.Context, options DatabaseRestoreOptions, env []string, directory, name string, input *os.File,
				prepare func(context.Context, databaseProcessIdentity) error) error {
				calls++
				if mode == "before-writer" {
					return ErrDatabaseRestore
				}
				if mode == "rollback" {
					return rejectPreparedRestore(ctx, options, env, directory, name, input, prepare)
				}
				err := runPreparedPostgreSQLRestore(ctx, options, env, directory, name, input, prepare)
				if err == nil && mode == "lost-sql-response" {
					return ErrDatabaseRestore
				}
				return err
			}
			var result DatabaseRestoreReceipt
			var err error
			if mode == "before-ack" {
				f.j.mu.Lock()
				_, _, err = f.j.createDatabaseRestoreRetry(ctx, f.controller, 0, archive, options, approval, f.open, run)
				f.j.mu.Unlock()
			} else {
				result, err = f.j.retryDockerDatabase(ctx, f.controller, 0, archive, options, approval, f.open, run)
			}
			started := mode == "commit" || mode == "new-options" || mode == "new-tool" || mode == "lost-sql-response" || mode == "before-ack" || mode == "rollback" || mode == "before-writer"
			completed := mode == "commit" || mode == "new-options" || mode == "new-tool" || mode == "before-ack"
			expectedCalls := 0
			if started {
				expectedCalls = 1
			}
			if (err == nil) != completed || calls != expectedCalls {
				t.Fatal("unexpected retry dispatch", err, calls)
			}
			if !started {
				if f.j.state.Pending.Sequence != 0 {
					t.Fatal("rejected retry created an intent")
				}
				return
			}
			current, err := f.j.latestRestoreMaterial(ctx, 0, archive, options)
			if err != nil || current.binding.Target == original.binding.Target || current.binding.PreviousAttempt != original.binding.Target || current.binding.PreviousReceipt != originalApproval.EvidenceSHA256 {
				t.Fatal("retry lineage missing", err)
			}
			if mode == "commit" || mode == "new-options" || mode == "new-tool" {
				if !result.Restored || result.CoherentBackup || result.TargetSHA256 != current.binding.Target {
					t.Fatal("incorrect retry result", result)
				}
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
			if !completed || mode == "before-ack" {
				expected := HostApplied
				if mode == "rollback" {
					expected = HostNotApplied
				}
				if mode == "before-writer" {
					expected = HostUnknown
				}
				outcome, err := f.j.reconcileDockerDatabaseRestore(ctx, f.controller, 0, archive, options, f.open)
				if outcome != expected || (err != nil) != (expected == HostUnknown) {
					t.Fatal("retry recovery failed", outcome, err)
				}
			}
			outcome, readErr := f.j.ReadDockerDatabaseRestoreOutcome(ctx, 0, archive, options)
			if mode == "before-writer" {
				if readErr == nil {
					t.Fatal("unknown retry published as resolved")
				}
			} else if readErr != nil || outcome.TargetSHA256 != current.binding.Target || (outcome.Outcome == HostNotApplied) != (mode == "rollback") {
				t.Fatal("wrong latest outcome", outcome, readErr)
			}
			if _, err := f.j.retryDockerDatabase(ctx, f.controller, 0, archive, options, originalApproval, f.open, run); err == nil || calls != 1 {
				t.Fatal("stale approval repeated restore", err, calls)
			}
			if _, err := f.j.restoreDockerDatabase(ctx, f.controller, 0, archive, options, f.open, run); !errors.Is(err, ErrHostJournalState) {
				t.Fatal("ordinary restore repeated", err)
			}
			for path, expected := range originalFiles {
				body, err := os.ReadFile(path)
				if err != nil || hostRecoveryDigest(body) != expected {
					t.Fatal("retry changed predecessor", path, err)
				}
			}
			requireRestoreRuntimeBlocked(t, original.destination.Configuration.URL)
			var source string
			if f.db.QueryRowContext(ctx, `SELECT body FROM public.attempt_witness`).Scan(&source) != nil || source != "private-restored-data" || f.posts.Load() != 1 {
				t.Fatal("retry changed source/service")
			}
		})
	}
}

func TestDatabaseRestoreRetryMultipleAttempts(t *testing.T) {
	f, archive, options, m, approval := databaseRestoreRetryFixture(t)
	ctx := context.Background()
	first := approval
	for i := 0; i < 2; i++ {
		if _, err := f.j.retryDockerDatabase(ctx, f.controller, 0, archive, options, approval, f.open, rejectPreparedRestore); err == nil {
			t.Fatal("expected rollback")
		}
		if outcome, err := f.j.reconcileDockerDatabaseRestore(ctx, f.controller, 0, archive, options, f.open); err != nil || outcome != HostNotApplied {
			t.Fatal(outcome, err)
		}
		next, err := f.j.ReadDockerDatabaseRestoreOutcome(ctx, 0, archive, options)
		if err != nil || next.TargetSHA256 == approval.TargetSHA256 || next.EvidenceSHA256 == approval.EvidenceSHA256 {
			t.Fatal("attempt identity reused", next, err)
		}
		approval = next
	}
	result, err := f.j.retryDockerDatabase(ctx, f.controller, 0, archive, options, approval, f.open, runPreparedPostgreSQLRestore)
	if err != nil || !result.Restored {
		t.Fatal(result, err)
	}
	if _, err := f.j.ReadDockerDatabaseRestore(ctx, 0, archive, options); err != nil {
		t.Fatal("final chain not readable", err)
	}
	if err := os.Truncate(filepath.Join(f.directory, databaseRestoreRecordName(first.TargetSHA256, "resolved")), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.j.ReadDockerDatabaseRestore(ctx, 0, archive, options); err == nil {
		t.Fatal("damaged ancestor ignored")
	}
	requireRestoreRuntimeBlocked(t, m.destination.Configuration.URL)
}

func TestDatabaseRestoreRetryRequiresProvenRollback(t *testing.T) {
	options := databaseRestoreTestOptions(t)
	for _, mode := range []string{"unknown", "applied", "journal-only"} {
		t.Run(mode, func(t *testing.T) {
			f, archive, m := databaseRestoreAttemptFixture(t, options)
			ctx := context.Background()
			run := rejectPreparedRestore
			if mode == "applied" {
				run = runPreparedPostgreSQLRestore
			}
			_, err := f.j.restoreDockerDatabase(ctx, f.controller, 0, archive, options, f.open, run)
			if (err == nil) != (mode == "applied") {
				t.Fatal("unexpected initial result", err)
			}
			if mode == "journal-only" {
				_, digest, err := f.j.readDatabaseRestoreChain(ctx, m.binding.Target, "writer", "")
				if err != nil {
					t.Fatal(err)
				}
				if err := f.j.Observe(f.j.state.Pending.Sequence, HostNotApplied, digest); err != nil {
					t.Fatal(err)
				}
			}
			history, err := f.j.restoreHistory()
			if err != nil || len(history) != 1 {
				t.Fatal(history, err)
			}
			approval := restoreOutcomeReceipt(m, history[0])
			calls := 0
			runner := func(context.Context, DatabaseRestoreOptions, []string, string, string, *os.File, func(context.Context, databaseProcessIdentity) error) error {
				calls++
				return ErrDatabaseRestore
			}
			if _, err := f.j.retryDockerDatabase(ctx, f.controller, 0, archive, options, approval, f.open, runner); err == nil || calls != 0 {
				t.Fatal("unproven retry dispatched", err, calls)
			}
			if _, err := f.j.ReadDockerDatabaseRestoreOutcome(ctx, 0, archive, options); (err == nil) != (mode == "applied") {
				t.Fatal("unproven outcome exposed", err)
			}
			requireRestoreRuntimeBlocked(t, m.destination.Configuration.URL)
		})
	}
}
