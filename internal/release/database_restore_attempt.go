package release

import (
	"context"
	"encoding/json"
	"time"

	"ai-access-gateway/internal/database"
)

const databaseRestoreScope = "postgres_restore_attempt_v1"

// DatabaseRestoreReceipt is historical database-only evidence. It does not
// authorize activation, migration, coherent backup or reuse of a restore target.
type DatabaseRestoreReceipt struct {
	Scope                string `json:"scope"`
	TargetSHA256         string `json:"target_sha256"`
	DatabaseTargetSHA256 string `json:"database_target_sha256"`
	ArchiveSHA256        string `json:"archive_sha256"`
	Restored             bool   `json:"restored"`
	CoherentBackup       bool   `json:"coherent_backup"`
}

type databaseRestoreBinding struct {
	Target          string                 `json:"target"`
	DatabaseTarget  string                 `json:"database_target"`
	DatabaseReceipt string                 `json:"database_receipt"`
	Epoch           int64                  `json:"epoch"`
	ArchiveOptions  DatabaseArchiveOptions `json:"archive_options"`
	Options         DatabaseRestoreOptions `json:"options"`
	ToolIdentity    string                 `json:"tool_identity"`
	Intent          HostPendingEffect      `json:"intent"`
	PreviousAttempt string                 `json:"previous_attempt,omitempty"`
	PreviousReceipt string                 `json:"previous_receipt,omitempty"`
}

type databaseRestoreRecord struct {
	Context           hostPlanContext         `json:"context"`
	Binding           databaseRestoreBinding  `json:"binding"`
	Phase             string                  `json:"phase"`
	Previous          string                  `json:"previous"`
	Process           databaseProcessIdentity `json:"process"`
	Credential        *archiveFileProof       `json:"credential,omitempty"`
	ResultOID         int64                   `json:"result_oid"`
	ObservationSHA256 string                  `json:"observation_sha256"`
	Resolution        HostObservation         `json:"resolution,omitempty"`
	At                time.Time               `json:"at"`
}

type databaseRestoreMaterial struct {
	plan        DockerHostPlan
	request     database.RuntimeReleaseRequest
	archive     databaseArchivePayload
	destination databaseTargetPayload
	binding     databaseRestoreBinding
}

func databaseRestoreHash(plan DockerHostPlan, epoch int64, target, receipt string, options DatabaseRestoreOptions) string {
	body, _ := json.Marshal(struct {
		Scope   string
		Plan    HostOperationSpec
		Epoch   int64
		Target  string
		Receipt string
		Options DatabaseRestoreOptions
	}{databaseRestoreScope, plan.Operation(), epoch, target, receipt, options})
	return hostRecoveryDigest(body)
}

func databaseRestoreRecordName(target, phase string) string {
	return ".database-restore-" + target + "." + phase + ".json"
}

func databaseRestorePasswordNames(target string) (string, string) {
	return ".database-restore-" + target + ".pgpass", ".database-restore-" + target + ".absent"
}

func (j *HostJournal) restoreMaterial(ctx context.Context, epoch int64, archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions) (databaseRestoreMaterial, error) {
	var m databaseRestoreMaterial
	if !options.valid() || ctx.Err() != nil {
		return m, ErrDatabaseRestore
	}
	plan, request, _, archive, _, archiveReceipt, err := j.targetMaterial(ctx, epoch, archiveOptions)
	if err != nil {
		return m, ErrDatabaseRestore
	}
	target := databaseTargetHash(plan, epoch, archive.Target, archiveReceipt)
	receipt, err := j.databaseTargetReceipt(target)
	if err != nil || receipt == "" {
		return m, ErrDatabaseRestore
	}
	destination, _, err := j.readDatabaseTargetMetadata(ctx, target, receipt)
	if err != nil {
		return m, ErrDatabaseRestore
	}
	m = databaseRestoreMaterial{plan: plan, request: request, archive: archive, destination: destination,
		binding: databaseRestoreBinding{Target: databaseRestoreHash(plan, epoch, target, receipt, options),
			DatabaseTarget: target, DatabaseReceipt: receipt, Epoch: epoch, ArchiveOptions: archiveOptions, Options: options}}
	return m, nil
}

func (j *HostJournal) restoreIntent(target string) (HostPendingEffect, string, error) {
	history, err := j.restoreHistory()
	if err != nil {
		return HostPendingEffect{}, "", err
	}
	for _, attempt := range history {
		if attempt.Intent.TargetSHA256 == target {
			receipt := ""
			if attempt.Outcome == HostApplied {
				receipt = attempt.Evidence
			}
			return attempt.Intent, receipt, nil
		}
	}
	return HostPendingEffect{}, "", ErrDatabaseRestore
}

func restoreReceipt(m databaseRestoreMaterial) DatabaseRestoreReceipt {
	return DatabaseRestoreReceipt{Scope: databaseRestoreScope, TargetSHA256: m.binding.Target,
		DatabaseTargetSHA256: m.destination.Target, ArchiveSHA256: m.archive.Archive.SHA256, Restored: true}
}

// ReadDockerDatabaseRestore verifies the complete protected record chain and
// journal receipt, not current SQL state or permission to open runtime traffic.
func (j *HostJournal) ReadDockerDatabaseRestore(ctx context.Context, epoch int64, archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions) (DatabaseRestoreReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	m, err := j.latestRestoreMaterial(ctx, epoch, archiveOptions, options)
	if err != nil {
		return DatabaseRestoreReceipt{}, err
	}
	_, receipt, err := j.restoreIntent(m.binding.Target)
	if err != nil || receipt == "" {
		return DatabaseRestoreReceipt{}, ErrDatabaseRestore
	}
	record, _, err := j.readDatabaseRestoreRecoveryHead(ctx, m.binding.Target, receipt)
	if err != nil || (record.Phase != "completed" && (record.Phase != "resolved" || record.Resolution != HostApplied)) {
		return DatabaseRestoreReceipt{}, ErrDatabaseRestore
	}
	return restoreReceipt(m), nil
}
