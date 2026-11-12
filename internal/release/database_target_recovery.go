package release

import (
	"context"
	"database/sql"

	"ai-access-gateway/internal/database"
)

// ReconcileDockerRestoreDatabase never runs CREATE, installs a fence, changes
// properties or deletes partial output. Without the original complete record,
// even an absent target remains unknown and requires explicit operator recovery.
func (j *HostJournal) ReconcileDockerRestoreDatabase(ctx context.Context, controller *database.RuntimeRelease, epoch int64, options DatabaseArchiveOptions) (HostObservation, error) {
	return j.reconcileDockerRestoreDatabase(ctx, controller, epoch, options, newDockerObserver)
}

func (j *HostJournal) reconcileDockerRestoreDatabase(ctx context.Context, controller *database.RuntimeRelease, epoch int64, options DatabaseArchiveOptions, open func(string) (*dockerObserver, error)) (HostObservation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if controller == nil {
		return HostUnknown, ErrDatabaseTarget
	}
	plan, request, _, archive, _, receipt, err := j.targetMaterial(ctx, epoch, options)
	if err != nil {
		return HostUnknown, err
	}
	target := databaseTargetHash(plan, epoch, archive.Target, receipt)
	pending := j.state.Pending
	if pending.Effect != HostCreateRestoreDatabase || pending.TargetSHA256 != target {
		return HostUnknown, ErrHostJournalState
	}
	if controller.WithActiveFence(ctx, request, plan.data.Observed.Database, func(database.RuntimeFenceState) error { return nil }) != nil {
		return HostUnknown, ErrDatabaseTarget
	}
	if quiet, err := controller.TryQuiesce(ctx); err != nil || !quiet {
		return HostUnknown, ErrDatabaseTarget
	}
	d, err := open(plan.data.EngineEndpoint)
	if err != nil {
		return HostUnknown, ErrDatabaseTarget
	}
	defer d.close()
	evidence := ""
	err = controller.WithQuiescentConnection(ctx, func(conn *sql.Conn) error {
		if validateDatabaseTargetSource(ctx, conn, d, plan, archive) != nil {
			return ErrDatabaseTarget
		}
		payload, digest, err := j.readDatabaseTargetMetadata(ctx, target, "")
		if err != nil {
			return ErrDatabaseTarget
		}
		db, err := openDockerDatabase(ctx, payload.Configuration, payload.Database)
		if err != nil {
			return ErrDatabaseTarget
		}
		defer db.Close()
		targetController, err := database.ResumeRuntimeReleaseForDatabase(ctx, db, request, payload.Database)
		if err != nil {
			return ErrDatabaseTarget
		}
		defer targetController.Close()
		if quiet, err := targetController.TryQuiesce(ctx); err != nil || !quiet {
			return ErrDatabaseTarget
		}
		return targetController.WithQuiescentConnection(ctx, func(targetConn *sql.Conn) error {
			if verifyDatabaseTargetConnection(ctx, targetConn, payload, archive) != nil {
				return ErrDatabaseTarget
			}
			if j.syncPrivateMaterial(databaseTargetMetadataName(target), maxDatabaseTargetMetadataBytes) != nil {
				return ErrDatabaseTarget
			}
			if _, _, err := j.readDatabaseTargetMetadata(ctx, target, digest); err != nil {
				return ErrDatabaseTarget
			}
			if validateDatabaseTargetSource(ctx, conn, d, plan, archive) != nil {
				return ErrDatabaseTarget
			}
			evidence = digest
			return nil
		})
	})
	if err != nil {
		return HostUnknown, ErrDatabaseTarget
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: pending.Sequence, Observation: HostApplied, Evidence: evidence}); err != nil {
		return HostUnknown, err
	}
	return HostApplied, nil
}
