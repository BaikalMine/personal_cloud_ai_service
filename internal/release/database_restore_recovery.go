package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"ai-access-gateway/internal/database"
)

// ReconcileDockerDatabaseRestore checks the original writer chain against live
// SQL under both exclusive owners. A missing completion can produce a separate
// resolution record, never a replacement completion. Orphan passwords require
// explicit cleanup first. This method never runs pg_restore or authorizes retry.
func (j *HostJournal) ReconcileDockerDatabaseRestore(ctx context.Context, controller *database.RuntimeRelease, epoch int64,
	archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions) (HostObservation, error) {
	return j.reconcileDockerDatabaseRestore(ctx, controller, epoch, archiveOptions, options, newDockerObserver)
}

func (j *HostJournal) reconcileDockerDatabaseRestore(ctx context.Context, controller *database.RuntimeRelease, epoch int64,
	archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions, open func(string) (*dockerObserver, error)) (HostObservation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	m, err := j.latestRestoreMaterial(ctx, epoch, archiveOptions, options)
	if err != nil || controller == nil {
		return HostUnknown, ErrDatabaseRestore
	}
	pending := j.state.Pending
	if pending.Sequence == 0 || pending.Effect != HostRestoreDatabase || pending.TargetSHA256 != m.binding.Target {
		return HostUnknown, ErrHostJournalState
	}
	record, digest, err := j.readDatabaseRestoreRecoveryHead(ctx, m.binding.Target, "")
	if err != nil || record.Binding.Intent != pending {
		return HostUnknown, ErrDatabaseRestore
	}
	if record.Phase == "writer" && (!restoreCredentialMatches(m, record.Credential) || j.restoreCredentialsAbsent(m.binding.Target) != nil) {
		return HostUnknown, ErrDatabaseRestore
	}
	if exited, err := databaseProcessExited(record.Process); err != nil || !exited {
		return HostUnknown, ErrDatabaseRestore
	}
	id, err := observePostgreSQLClientTool(options.Tool)
	if err != nil || id != record.Binding.ToolIdentity {
		return HostUnknown, ErrDatabaseRestore
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(options.TimeoutSeconds)*time.Second)
	defer cancel()
	outcome := HostUnknown
	err = withDatabaseRestoreOwnership(ctx, controller, m, open, func(source, target *sql.Conn, d *dockerObserver) error {
		var resolveErr error
		record, digest, resolveErr = j.resolveDatabaseRestore(ctx, source, target, m, record, digest)
		if resolveErr != nil {
			return resolveErr
		}
		outcome = HostApplied
		if record.Phase == "resolved" {
			outcome = record.Resolution
		}
		for _, phase := range []string{"prepared", "writer", record.Phase} {
			if j.syncPrivateMaterial(databaseRestoreRecordName(m.binding.Target, phase), maxDatabaseRestoreRecordBytes) != nil {
				return ErrDatabaseRestore
			}
		}
		if _, _, err := j.readDatabaseRestoreRecoveryHead(ctx, m.binding.Target, digest); err != nil {
			return err
		}
		if validateDatabaseTargetSource(ctx, source, d, m.plan, m.archive) != nil {
			return ErrDatabaseRestore
		}
		// Re-observe after the durable record, including when an earlier recovery
		// died before the host-journal acknowledgement.
		observed, oid, evidence, err := inspectDatabaseRestoreOutcome(ctx, target, m)
		if err != nil || observed != outcome || oid != record.ResultOID || evidence != record.ObservationSHA256 {
			return ErrDatabaseRestore
		}
		return nil
	})
	if err != nil {
		return HostUnknown, ErrDatabaseRestore
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: pending.Sequence, Observation: outcome, Evidence: digest}); err != nil {
		return HostUnknown, err
	}
	return outcome, nil
}

func (j *HostJournal) resolveDatabaseRestore(ctx context.Context, source, target *sql.Conn, m databaseRestoreMaterial,
	record databaseRestoreRecord, digest string) (databaseRestoreRecord, string, error) {
	if _, err := inspectRestoreCatalog(ctx, source, m); err != nil {
		return databaseRestoreRecord{}, "", err
	}
	outcome, oid, evidence, err := inspectDatabaseRestoreOutcome(ctx, target, m)
	if err != nil {
		return databaseRestoreRecord{}, "", err
	}
	if record.Phase != "writer" {
		expected := HostApplied
		if record.Phase == "resolved" {
			expected = record.Resolution
		}
		if outcome != expected || oid != record.ResultOID || evidence != record.ObservationSHA256 {
			return databaseRestoreRecord{}, "", ErrDatabaseRestore
		}
		return record, digest, nil
	}
	if _, _, err := j.readDatabaseRestoreRecoveryHead(ctx, m.binding.Target, digest); err != nil {
		return databaseRestoreRecord{}, "", err
	}
	record.Phase, record.Previous, record.At = "resolved", digest, time.Now().UTC()
	record.Resolution, record.ResultOID, record.ObservationSHA256 = outcome, oid, evidence
	digest, err = j.createDatabaseRestoreRecord(ctx, record)
	return record, digest, err
}

// The verified writer uses an unfiltered immutable archive and PostgreSQL's
// --single-transaction restore. After that exact writer and all SQL sessions
// exit, the target is either the original bootstrap or a fully restored catalog.
// An OID change alone is insufficient: ownership, properties and catalog checks
// remain mandatory. Privileged out-of-band SQL is outside the cooperative fence.
func inspectDatabaseRestoreOutcome(ctx context.Context, target *sql.Conn, m databaseRestoreMaterial) (HostObservation, int64, string, error) {
	var oid int64
	if checkRestoreSessions(ctx, target) != nil || target.QueryRowContext(ctx,
		`SELECT 'public.release_runtime_control'::pg_catalog.regclass::oid::bigint`).Scan(&oid) != nil || oid <= 0 {
		return HostUnknown, 0, "", ErrDatabaseRestore
	}
	if oid != m.destination.BootstrapOID {
		restoredOID, digest, err := inspectRestoredDatabase(ctx, target, m)
		if err != nil {
			return HostUnknown, 0, "", ErrDatabaseRestore
		}
		return HostApplied, restoredOID, digest, nil
	}
	if verifyDatabaseTargetConnection(ctx, target, m.destination, m.archive) != nil ||
		database.VerifyRuntimeRestoreBootstrap(ctx, target, oid) != nil {
		return HostUnknown, 0, "", ErrDatabaseRestore
	}
	body, _ := json.Marshal(struct {
		Scope        string
		Target       string
		BootstrapOID int64
	}{databaseRestoreScope + "/bootstrap_unchanged_v1", m.binding.Target, oid})
	return HostNotApplied, oid, hostRecoveryDigest(body), nil
}
