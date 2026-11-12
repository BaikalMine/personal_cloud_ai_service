package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"ai-access-gateway/internal/database"
)

type preparedDatabaseRestoreRunner func(context.Context, DatabaseRestoreOptions, []string, string, string, *os.File,
	func(context.Context, databaseProcessIdentity) error) error

// RestoreDockerDatabase restores one committed archive into its separately
// receipted target. No existing attempt, even a failed one, is retried here.
// Failure may follow a committed transaction and leaves the intent unresolved.
func (j *HostJournal) RestoreDockerDatabase(ctx context.Context, controller *database.RuntimeRelease, epoch int64,
	archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions) (DatabaseRestoreReceipt, error) {
	return j.restoreDockerDatabase(ctx, controller, epoch, archiveOptions, options, newDockerObserver, runPreparedPostgreSQLRestore)
}

func (j *HostJournal) restoreDockerDatabase(ctx context.Context, controller *database.RuntimeRelease, epoch int64,
	archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions, open func(string) (*dockerObserver, error), run preparedDatabaseRestoreRunner) (DatabaseRestoreReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	m, digest, err := j.createDatabaseRestoreAttempt(ctx, controller, epoch, archiveOptions, options, open, run)
	if err != nil {
		return DatabaseRestoreReceipt{}, err
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: j.state.Pending.Sequence, Observation: HostApplied, Evidence: digest}); err != nil {
		return DatabaseRestoreReceipt{}, err
	}
	return restoreReceipt(m), nil
}

func (j *HostJournal) createDatabaseRestoreAttempt(ctx context.Context, controller *database.RuntimeRelease, epoch int64,
	archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions, open func(string) (*dockerObserver, error), run preparedDatabaseRestoreRunner) (databaseRestoreMaterial, string, error) {
	var empty databaseRestoreMaterial
	m, err := j.restoreMaterial(ctx, epoch, archiveOptions, options)
	if err != nil || controller == nil || run == nil {
		return empty, "", ErrDatabaseRestore
	}
	if j.state.Pending.Sequence != 0 {
		return empty, "", ErrHostJournalState
	}
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == HostRestoreDatabase {
			return empty, "", ErrHostJournalState
		}
	}
	return j.executeDatabaseRestoreAttempt(ctx, controller, m, open, run)
}

func (j *HostJournal) executeDatabaseRestoreAttempt(ctx context.Context, controller *database.RuntimeRelease, m databaseRestoreMaterial,
	open func(string) (*dockerObserver, error), run preparedDatabaseRestoreRunner) (databaseRestoreMaterial, string, error) {
	var empty databaseRestoreMaterial
	if controller == nil || run == nil || j.state.Pending.Sequence != 0 || ctx.Err() != nil {
		return empty, "", ErrDatabaseRestore
	}
	options := m.binding.Options
	passwordName, absentName := databaseRestorePasswordNames(m.binding.Target)
	for _, name := range []string{passwordName, absentName, databaseRestoreRecordName(m.binding.Target, "prepared"),
		databaseRestoreRecordName(m.binding.Target, "writer"), databaseRestoreRecordName(m.binding.Target, "completed"),
		databaseRestoreRecordName(m.binding.Target, "resolved")} {
		if _, err := j.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return empty, "", ErrDatabaseRestore
		}
	}
	toolIdentity, err := observePostgreSQLClientTool(options.Tool)
	if err != nil {
		return empty, "", ErrDatabaseRestore
	}
	m.binding.ToolIdentity = toolIdentity
	password, err := databaseArchivePassword(m.destination.Configuration)
	if err != nil {
		return empty, "", ErrDatabaseRestore
	}
	defer clear(password)
	environment, err := databaseRestoreEnvironment(m.destination.Configuration, j.directory,
		filepath.Join(j.directory, passwordName), filepath.Join(j.directory, absentName))
	if err != nil || checkPostgreSQLRestoreVersion(ctx, options.Tool, environment, j.directory, m.archive.PostgresMajor) != nil {
		return empty, "", ErrDatabaseRestore
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(options.TimeoutSeconds)*time.Second)
	defer cancel()
	evidence := ""
	err = withDatabaseRestoreOwnership(ctx, controller, m, open, func(source, target *sql.Conn, d *dockerObserver) error {
		if verifyDatabaseTargetConnection(ctx, target, m.destination, m.archive) != nil ||
			database.VerifyRuntimeRestoreBootstrap(ctx, target, m.destination.BootstrapOID) != nil || checkRestoreSessions(ctx, target) != nil {
			return ErrDatabaseRestore
		}
		if _, err := inspectRestoreCatalog(ctx, source, m); err != nil {
			return err
		}
		if j.verifyDatabaseRestorePredecessor(ctx, target, m) != nil {
			return ErrDatabaseRestore
		}
		spool, err := j.databaseRestoreSpool(ctx, m.archive)
		if err != nil {
			return err
		}
		defer spool.Close()
		if err := j.append(hostRecord{Kind: "intent", Effect: HostRestoreDatabase, Target: m.binding.Target}); err != nil {
			return err
		}
		m.binding.Intent = j.state.Pending
		record := databaseRestoreRecord{Binding: m.binding, Phase: "prepared", At: time.Now().UTC()}
		prepared, err := j.createDatabaseRestoreRecord(ctx, record)
		if err != nil || j.writeArchivePassword(passwordName, password) != nil {
			return ErrDatabaseRestore
		}
		proof, err := j.archiveProof(ctx, passwordName, maxDockerDatabaseURLBytes*2)
		if err != nil {
			return ErrDatabaseRestore
		}
		defer j.removeArchivePassword(passwordName, proof)
		writerReceipt := ""
		err = run(ctx, options, environment, j.directory, m.destination.Database.DatabaseName, spool,
			func(ctx context.Context, process databaseProcessIdentity) error {
				if writerReceipt != "" || !process.valid() {
					return ErrDatabaseRestore
				}
				if _, _, err := j.readDatabaseRestoreChain(ctx, m.binding.Target, "prepared", prepared); err != nil {
					return err
				}
				if validateDatabaseTargetSource(ctx, source, d, m.plan, m.archive) != nil ||
					m.destination.Configuration.validate() != nil || checkRestoreSessions(ctx, target) != nil ||
					database.VerifyRuntimeRestoreBootstrap(ctx, target, m.destination.BootstrapOID) != nil ||
					j.verifyDatabaseRestorePredecessor(ctx, target, m) != nil {
					return ErrDatabaseRestore
				}
				id, err := observePostgreSQLClientTool(options.Tool)
				if err != nil || id != m.binding.ToolIdentity {
					return ErrDatabaseRestore
				}
				current, err := j.archiveProof(ctx, passwordName, maxDockerDatabaseURLBytes*2)
				if err != nil || current != proof || !restoreCredentialMatches(m, &proof) {
					return ErrDatabaseRestore
				}
				record.Phase, record.Previous, record.Process, record.At = "writer", prepared, process, time.Now().UTC()
				record.Credential = &proof
				writerReceipt, err = j.createDatabaseRestoreRecord(ctx, record)
				return err
			})
		if err != nil || writerReceipt == "" {
			return ErrDatabaseRestore
		}
		if exited, err := databaseProcessExited(record.Process); err != nil || !exited {
			return ErrDatabaseRestore
		}
		if j.removeArchivePassword(passwordName, proof) != nil {
			return ErrDatabaseRestore
		}
		if _, err := j.root.Lstat(absentName); !errors.Is(err, os.ErrNotExist) {
			return ErrDatabaseRestore
		}
		if validateDatabaseTargetSource(ctx, source, d, m.plan, m.archive) != nil {
			return ErrDatabaseRestore
		}
		record.ResultOID, record.ObservationSHA256, err = inspectRestoredDatabase(ctx, target, m)
		if err != nil {
			return err
		}
		record.Phase, record.Previous, record.At = "completed", writerReceipt, time.Now().UTC()
		evidence, err = j.createDatabaseRestoreRecord(ctx, record)
		return err
	})
	if err != nil {
		return empty, "", ErrDatabaseRestore
	}
	return m, evidence, nil
}

func withDatabaseRestoreOwnership(ctx context.Context, controller *database.RuntimeRelease, m databaseRestoreMaterial,
	open func(string) (*dockerObserver, error), work func(*sql.Conn, *sql.Conn, *dockerObserver) error) error {
	if controller == nil || controller.WithActiveFence(ctx, m.request, m.plan.data.Observed.Database, func(database.RuntimeFenceState) error { return nil }) != nil {
		return ErrDatabaseRestore
	}
	if quiet, err := controller.TryQuiesce(ctx); err != nil || !quiet {
		return ErrDatabaseRestore
	}
	d, err := open(m.plan.data.EngineEndpoint)
	if err != nil {
		return ErrDatabaseRestore
	}
	defer d.close()
	db, err := openDockerDatabase(ctx, m.destination.Configuration, m.destination.Database)
	if err != nil {
		return ErrDatabaseRestore
	}
	defer db.Close()
	targetController, err := database.ResumeRuntimeReleaseForDatabase(ctx, db, m.request, m.destination.Database)
	if err != nil {
		return ErrDatabaseRestore
	}
	defer targetController.Close()
	if quiet, err := targetController.TryQuiesce(ctx); err != nil || !quiet {
		return ErrDatabaseRestore
	}
	return controller.WithQuiescentConnection(ctx, func(source *sql.Conn) error {
		if validateDatabaseTargetSource(ctx, source, d, m.plan, m.archive) != nil {
			return ErrDatabaseRestore
		}
		return targetController.WithQuiescentConnection(ctx, func(target *sql.Conn) error { return work(source, target, d) })
	})
}

func checkRestoreSessions(ctx context.Context, conn *sql.Conn) error {
	var quiet bool
	if conn.QueryRowContext(ctx, `SELECT
	 NOT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE datid=(SELECT oid FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()) AND pid<>pg_catalog.pg_backend_pid())
	 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_prepared_xacts WHERE database=pg_catalog.current_database())`).Scan(&quiet) != nil || !quiet {
		return ErrDatabaseRestore
	}
	return nil
}

func inspectRestoreCatalog(ctx context.Context, conn *sql.Conn, m databaseRestoreMaterial) (string, error) {
	previousRef, previous, err := observeHostPackage(HostPackageReference{m.plan.data.Previous.Directory, m.plan.data.Previous.ManifestSHA256})
	if err != nil || previousRef != m.plan.data.Previous {
		return "", ErrDatabaseRestore
	}
	candidateRef, candidate, err := observeHostPackage(HostPackageReference{m.plan.data.Candidate.Directory, m.plan.data.Candidate.ManifestSHA256})
	if err != nil || candidateRef != m.plan.data.Candidate {
		return "", ErrDatabaseRestore
	}
	report, err := InspectDatabaseOnConnection(ctx, conn, previous, candidate)
	if err != nil || !report.DatabaseChecksPassed {
		return "", ErrDatabaseRestore
	}
	report.SnapshotAt, report.FinishedAt = time.Time{}, time.Time{}
	body, _ := json.Marshal(report)
	return hostRecoveryDigest(body), nil
}

func inspectRestoredDatabase(ctx context.Context, conn *sql.Conn, m databaseRestoreMaterial) (int64, string, error) {
	var oid int64
	if checkRestoreSessions(ctx, conn) != nil ||
		conn.QueryRowContext(ctx, `SELECT 'public.release_runtime_control'::pg_catalog.regclass::oid::bigint`).Scan(&oid) != nil || oid <= 0 || oid == m.destination.BootstrapOID {
		return 0, "", ErrDatabaseRestore
	}
	properties, err := readPostgresDatabaseProperties(ctx, conn, m.destination.Database.DatabaseOID)
	if err != nil || !m.archive.DatabaseProperties.same(properties) {
		return 0, "", ErrDatabaseRestore
	}
	digest, err := inspectRestoreCatalog(ctx, conn, m)
	return oid, digest, err
}
