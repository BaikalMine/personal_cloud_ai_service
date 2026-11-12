package release

import (
	"context"
	"encoding/json"
)

const databaseRestoreOutcomeScope = "postgres_restore_outcome_v1"

// DatabaseRestoreOutcomeReceipt is historical evidence for the latest attempt,
// not permission to retry, change databases or activate traffic. An explicit
// retry must name this exact receipt and pass fresh ownership/SQL checks.
type DatabaseRestoreOutcomeReceipt struct {
	Scope                string          `json:"scope"`
	TargetSHA256         string          `json:"target_sha256"`
	DatabaseTargetSHA256 string          `json:"database_target_sha256"`
	ArchiveSHA256        string          `json:"archive_sha256"`
	EvidenceSHA256       string          `json:"evidence_sha256"`
	Outcome              HostObservation `json:"outcome"`
}

type databaseRestoreHistoryEntry struct {
	Intent   HostPendingEffect
	Outcome  HostObservation
	Evidence string
}

// A successor is possible only after a terminal not_applied observation. Its
// actual protected resolution and lineage are checked by the record reader.
func (j *HostJournal) restoreHistory() ([]databaseRestoreHistoryEntry, error) {
	if j.validate() != nil {
		return nil, ErrDatabaseRestore
	}
	var history []databaseRestoreHistoryEntry
	seen := make(map[string]bool)
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == HostRestoreDatabase {
			if seen[event.TargetSHA256] || (len(history) > 0 && history[len(history)-1].Outcome != HostNotApplied) {
				return nil, ErrDatabaseRestore
			}
			seen[event.TargetSHA256] = true
			history = append(history, databaseRestoreHistoryEntry{Intent: HostPendingEffect{Sequence: event.Sequence,
				OwnerEpoch: event.OwnerEpoch, Effect: event.Effect, TargetSHA256: event.TargetSHA256}, Outcome: HostUnknown})
		}
		if event.Kind == "observe" && len(history) > 0 {
			last := &history[len(history)-1]
			if event.IntentSequence != last.Intent.Sequence {
				continue
			}
			if last.Outcome != HostUnknown {
				return nil, ErrDatabaseRestore
			}
			if event.Observation == HostApplied || event.Observation == HostNotApplied {
				last.Outcome, last.Evidence = event.Observation, event.EvidenceSHA256
			}
		}
	}
	return history, nil
}

func databaseRestoreRetryHash(base, previous, receipt string) string {
	body, _ := json.Marshal(struct{ Scope, Base, Previous, Receipt string }{
		"postgres_restore_retry_v1", base, previous, receipt})
	return hostRecoveryDigest(body)
}

func sameRestoreDestination(a, b databaseRestoreBinding) bool {
	return a.DatabaseTarget == b.DatabaseTarget && a.DatabaseReceipt == b.DatabaseReceipt &&
		a.Epoch == b.Epoch && a.ArchiveOptions == b.ArchiveOptions
}

func (j *HostJournal) expectedRestoreBinding(base databaseRestoreBinding, target, tool string) (databaseRestoreBinding, error) {
	history, err := j.restoreHistory()
	if err != nil || !validHostHash(tool) {
		return databaseRestoreBinding{}, ErrDatabaseRestore
	}
	for index, entry := range history {
		if entry.Intent.TargetSHA256 != target {
			continue
		}
		base.Intent, base.ToolIdentity = entry.Intent, tool
		if index > 0 {
			previous := history[index-1]
			base.PreviousAttempt, base.PreviousReceipt = previous.Intent.TargetSHA256, previous.Evidence
			base.Target = databaseRestoreRetryHash(base.Target, base.PreviousAttempt, base.PreviousReceipt)
		}
		if base.Target != target {
			return databaseRestoreBinding{}, ErrDatabaseRestore
		}
		return base, nil
	}
	return databaseRestoreBinding{}, ErrDatabaseRestore
}

func (j *HostJournal) latestRestoreMaterial(ctx context.Context, epoch int64, archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions) (databaseRestoreMaterial, error) {
	m, err := j.restoreMaterial(ctx, epoch, archiveOptions, options)
	if err != nil {
		return databaseRestoreMaterial{}, err
	}
	history, err := j.restoreHistory()
	if err != nil || len(history) == 0 {
		return databaseRestoreMaterial{}, ErrDatabaseRestore
	}
	last := history[len(history)-1]
	record, _, err := j.readDatabaseRestoreChain(ctx, last.Intent.TargetSHA256, "prepared", "")
	if err != nil || !sameRestoreDestination(m.binding, record.Binding) || options != record.Binding.Options {
		return databaseRestoreMaterial{}, ErrDatabaseRestore
	}
	m.binding = record.Binding
	return m, nil
}

func restoreOutcomeReceipt(m databaseRestoreMaterial, entry databaseRestoreHistoryEntry) DatabaseRestoreOutcomeReceipt {
	return DatabaseRestoreOutcomeReceipt{Scope: databaseRestoreOutcomeScope, TargetSHA256: entry.Intent.TargetSHA256,
		DatabaseTargetSHA256: m.destination.Target, ArchiveSHA256: m.archive.Archive.SHA256,
		EvidenceSHA256: entry.Evidence, Outcome: entry.Outcome}
}

// ReadDockerDatabaseRestoreOutcome verifies the latest terminal host receipt
// and its complete immutable lineage. It does not inspect live SQL or unfence.
func (j *HostJournal) ReadDockerDatabaseRestoreOutcome(ctx context.Context, epoch int64, archiveOptions DatabaseArchiveOptions,
	options DatabaseRestoreOptions) (DatabaseRestoreOutcomeReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	m, err := j.latestRestoreMaterial(ctx, epoch, archiveOptions, options)
	if err != nil {
		return DatabaseRestoreOutcomeReceipt{}, err
	}
	history, err := j.restoreHistory()
	if err != nil || len(history) == 0 {
		return DatabaseRestoreOutcomeReceipt{}, ErrDatabaseRestore
	}
	last := history[len(history)-1]
	if last.Outcome != HostApplied && last.Outcome != HostNotApplied {
		return DatabaseRestoreOutcomeReceipt{}, ErrDatabaseRestore
	}
	record, _, err := j.readDatabaseRestoreRecoveryHead(ctx, m.binding.Target, last.Evidence)
	if err != nil || (record.Phase != "resolved" && record.Phase != "completed") ||
		(record.Phase == "resolved" && record.Resolution != last.Outcome) ||
		(record.Phase == "completed" && last.Outcome != HostApplied) {
		return DatabaseRestoreOutcomeReceipt{}, ErrDatabaseRestore
	}
	return restoreOutcomeReceipt(m, last), nil
}
