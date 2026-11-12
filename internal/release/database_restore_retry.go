package release

import (
	"context"
	"database/sql"

	"ai-access-gateway/internal/database"
)

// RetryDockerDatabase explicitly starts one successor of the exact supplied
// historical not_applied receipt. It never retries unknown/applied/stale attempts
// or replaces their records. The new attempt uses freshly verified options and
// retains the same archive, database target, operation and epoch.
func (j *HostJournal) RetryDockerDatabase(ctx context.Context, controller *database.RuntimeRelease, epoch int64,
	archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions, previous DatabaseRestoreOutcomeReceipt) (DatabaseRestoreReceipt, error) {
	return j.retryDockerDatabase(ctx, controller, epoch, archiveOptions, options, previous, newDockerObserver, runPreparedPostgreSQLRestore)
}

func (j *HostJournal) retryDockerDatabase(ctx context.Context, controller *database.RuntimeRelease, epoch int64,
	archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions, previous DatabaseRestoreOutcomeReceipt,
	open func(string) (*dockerObserver, error), run preparedDatabaseRestoreRunner) (DatabaseRestoreReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	m, digest, err := j.createDatabaseRestoreRetry(ctx, controller, epoch, archiveOptions, options, previous, open, run)
	if err != nil {
		return DatabaseRestoreReceipt{}, err
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: j.state.Pending.Sequence, Observation: HostApplied, Evidence: digest}); err != nil {
		return DatabaseRestoreReceipt{}, err
	}
	return restoreReceipt(m), nil
}

func (j *HostJournal) createDatabaseRestoreRetry(ctx context.Context, controller *database.RuntimeRelease, epoch int64,
	archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions, previous DatabaseRestoreOutcomeReceipt,
	open func(string) (*dockerObserver, error), run preparedDatabaseRestoreRunner) (databaseRestoreMaterial, string, error) {
	var empty databaseRestoreMaterial
	if j.state.Pending.Sequence != 0 {
		return empty, "", ErrHostJournalState
	}
	m, err := j.restoreMaterial(ctx, epoch, archiveOptions, options)
	if err != nil || controller == nil || run == nil {
		return empty, "", ErrDatabaseRestore
	}
	history, err := j.restoreHistory()
	if err != nil || len(history) == 0 {
		return empty, "", ErrHostJournalState
	}
	last := history[len(history)-1]
	if last.Outcome != HostNotApplied || previous != restoreOutcomeReceipt(m, last) {
		return empty, "", ErrHostJournalState
	}
	record, _, err := j.readDatabaseRestoreRecoveryHead(ctx, last.Intent.TargetSHA256, last.Evidence)
	if err != nil || record.Phase != "resolved" || record.Resolution != HostNotApplied || !sameRestoreDestination(m.binding, record.Binding) {
		return empty, "", ErrDatabaseRestore
	}
	m.binding.PreviousAttempt, m.binding.PreviousReceipt = last.Intent.TargetSHA256, last.Evidence
	m.binding.Target = databaseRestoreRetryHash(m.binding.Target, m.binding.PreviousAttempt, m.binding.PreviousReceipt)
	return j.executeDatabaseRestoreAttempt(ctx, controller, m, open, run)
}

// Called while both SQL owners are held, before intent and again before stdin.
// The old proof is checked using its own target-bound observation hash, never
// substituted with an observation of the new attempt's target hash.
func (j *HostJournal) verifyDatabaseRestorePredecessor(ctx context.Context, target *sql.Conn, m databaseRestoreMaterial) error {
	if m.binding.PreviousAttempt == "" && m.binding.PreviousReceipt == "" {
		return nil
	}
	record, _, err := j.readDatabaseRestoreRecoveryHead(ctx, m.binding.PreviousAttempt, m.binding.PreviousReceipt)
	if err != nil || record.Phase != "resolved" || record.Resolution != HostNotApplied || !sameRestoreDestination(m.binding, record.Binding) {
		return ErrDatabaseRestore
	}
	if exited, err := databaseProcessExited(record.Process); err != nil || !exited {
		return ErrDatabaseRestore
	}
	previous := m
	previous.binding = record.Binding
	outcome, oid, evidence, err := inspectDatabaseRestoreOutcome(ctx, target, previous)
	if err != nil || outcome != HostNotApplied || oid != record.ResultOID || evidence != record.ObservationSHA256 {
		return ErrDatabaseRestore
	}
	return nil
}
