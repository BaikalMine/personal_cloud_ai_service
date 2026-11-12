package release

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"ai-access-gateway/internal/database"
)

func (j *HostJournal) databaseArchiveReceipt(target string) (string, error) {
	intents := make(map[uint64]bool)
	receipt := ""
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == HostArchiveDatabase && event.TargetSHA256 == target {
			intents[event.Sequence] = true
		}
		if event.Kind == "observe" && intents[event.IntentSequence] && event.Observation == HostApplied {
			if receipt != "" {
				return "", ErrDatabaseArchive
			}
			receipt = event.EvidenceSHA256
		}
	}
	return receipt, nil
}

// ReadDockerDatabaseArchive checks the committed original metadata and every
// archive byte. It is a historical archive receipt, not a live restore permit.
func (j *HostJournal) ReadDockerDatabaseArchive(ctx context.Context, epoch int64, options DatabaseArchiveOptions) (DatabaseArchiveReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var empty DatabaseArchiveReceipt
	if !options.valid() {
		return empty, ErrDatabaseArchive
	}
	plan, _, _, _, err := j.archiveMaterial(epoch)
	if err != nil {
		return empty, err
	}
	target := databaseArchiveTarget(plan, epoch, options)
	receipt, err := j.databaseArchiveReceipt(target)
	if err != nil || receipt == "" {
		return empty, ErrDatabaseArchive
	}
	payload, _, err := j.readDatabaseArchiveMetadata(ctx, target, receipt)
	if err != nil {
		return empty, err
	}
	return archiveReceipt(payload), nil
}

// ReconcileDockerDatabaseArchive never starts pg_dump, rewrites bytes or cleans
// up partial output. Only an original protected completion record can prove a
// successful child exit. An archive without that record remains unknown even
// when it has a valid header or can be listed by pg_restore.
func (j *HostJournal) ReconcileDockerDatabaseArchive(ctx context.Context, controller *database.RuntimeRelease, epoch int64, options DatabaseArchiveOptions) (HostObservation, error) {
	return j.reconcileDockerDatabaseArchive(ctx, controller, epoch, options, newDockerObserver)
}

func (j *HostJournal) reconcileDockerDatabaseArchive(ctx context.Context, controller *database.RuntimeRelease, epoch int64, options DatabaseArchiveOptions,
	open func(string) (*dockerObserver, error)) (HostObservation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if controller == nil || !options.valid() {
		return "", ErrDatabaseArchive
	}
	plan, request, _, _, err := j.archiveMaterial(epoch)
	if err != nil {
		return "", err
	}
	target := databaseArchiveTarget(plan, epoch, options)
	pending := j.state.Pending
	if pending.Effect != HostArchiveDatabase || pending.TargetSHA256 != target {
		return "", ErrHostJournalState
	}
	if err := controller.WithActiveFence(ctx, request, plan.data.Observed.Database, func(database.RuntimeFenceState) error { return nil }); err != nil {
		return HostUnknown, ErrDatabaseArchive
	}
	quiescent, err := controller.TryQuiesce(ctx)
	if err != nil || !quiescent {
		return HostUnknown, ErrDatabaseArchive
	}
	d, err := open(plan.data.EngineEndpoint)
	if err != nil {
		return HostUnknown, ErrDatabaseArchive
	}
	defer d.close()
	observation, evidence := HostApplied, ""
	err = controller.WithQuiescentConnection(ctx, func(*sql.Conn) error {
		if _, err := d.observePlannedStop(ctx, plan); err != nil {
			return ErrDatabaseArchive
		}
		archiveName, metadataName, passwordName, absentName := databaseArchiveNames(target)
		allAbsent := true
		for _, name := range []string{archiveName, metadataName, passwordName, absentName} {
			if _, err := j.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				allAbsent = false
			}
		}
		if allAbsent {
			observation = HostNotApplied
			evidence = hostRecoveryDigest([]byte("database-archive/absent/v1:" + target))
			return nil
		}
		payload, digest, err := j.readDatabaseArchiveMetadata(ctx, target, "")
		if err != nil {
			return ErrDatabaseArchive
		}
		if j.syncPrivateMaterial(archiveName, payload.Options.MaxBytes) != nil || j.syncPrivateMaterial(metadataName, maxDatabaseArchiveMetadataBytes) != nil {
			return ErrDatabaseArchive
		}
		if _, _, err := j.readDatabaseArchiveMetadata(ctx, target, digest); err != nil {
			return ErrDatabaseArchive
		}
		evidence = digest
		return nil
	})
	if err != nil {
		return HostUnknown, ErrDatabaseArchive
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: pending.Sequence, Observation: observation, Evidence: evidence}); err != nil {
		return HostUnknown, err
	}
	return observation, nil
}
