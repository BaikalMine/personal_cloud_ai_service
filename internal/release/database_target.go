package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"time"

	"ai-access-gateway/internal/database"
)

const databaseTargetScope = "postgres_restore_target_v1"

var ErrDatabaseTarget = errors.New("release restore database target is unavailable or changed")

// DatabaseTargetReceipt proves creation only. No archive data has been restored
// and no runtime traffic, migration or coherent backup is authorized by it.
type DatabaseTargetReceipt struct {
	Scope               string `json:"scope"`
	TargetSHA256        string `json:"target_sha256"`
	ArchiveTargetSHA256 string `json:"archive_target_sha256"`
	Restored            bool   `json:"restored"`
}

type databaseTargetPayload struct {
	Context              hostPlanContext                  `json:"context"`
	Target               string                           `json:"target"`
	ArchiveTarget        string                           `json:"archive_target"`
	ArchiveReceipt       string                           `json:"archive_receipt"`
	Epoch                int64                            `json:"epoch"`
	ConfigurationReceipt string                           `json:"configuration_receipt"`
	Configuration        dockerDatabaseConfiguration      `json:"configuration"`
	Database             database.RuntimeDatabaseIdentity `json:"database"`
	BootstrapOID         int64                            `json:"bootstrap_oid"`
	CompletedAt          time.Time                        `json:"completed_at"`
}

func databaseTargetHash(plan DockerHostPlan, epoch int64, archiveTarget, archiveReceipt string) string {
	body, _ := json.Marshal(struct {
		Scope          string
		Plan           HostOperationSpec
		Epoch          int64
		ArchiveTarget  string
		ArchiveReceipt string
	}{databaseTargetScope, plan.Operation(), epoch, archiveTarget, archiveReceipt})
	return hostRecoveryDigest(body)
}

func databaseTargetName(target string) string         { return "gateway_restore_" + target[:32] }
func databaseTargetMetadataName(target string) string { return ".database-target-" + target + ".json" }

func targetDatabaseConfiguration(source dockerDatabaseConfiguration, target string) (dockerDatabaseConfiguration, error) {
	if !validHostHash(target) || source.validate() != nil {
		return dockerDatabaseConfiguration{}, ErrDatabaseTarget
	}
	u, err := url.Parse(source.URL)
	if err != nil {
		return dockerDatabaseConfiguration{}, ErrDatabaseTarget
	}
	u.Path, u.RawPath = "/"+databaseTargetName(target), ""
	configuration := dockerDatabaseConfiguration{URL: u.String(), TLSFiles: source.TLSFiles}
	if configuration.validate() != nil {
		return dockerDatabaseConfiguration{}, ErrDatabaseTarget
	}
	return configuration, nil
}

func targetReceipt(p databaseTargetPayload) DatabaseTargetReceipt {
	return DatabaseTargetReceipt{Scope: databaseTargetScope, TargetSHA256: p.Target, ArchiveTargetSHA256: p.ArchiveTarget}
}

func (j *HostJournal) targetMaterial(ctx context.Context, epoch int64, options DatabaseArchiveOptions) (DockerHostPlan, database.RuntimeReleaseRequest, dockerDatabaseConfiguration, databaseArchivePayload, string, string, error) {
	plan, request, source, configReceipt, err := j.archiveMaterial(epoch)
	var archive databaseArchivePayload
	if err != nil || !options.valid() {
		return plan, request, source, archive, "", "", ErrDatabaseTarget
	}
	archiveTarget := databaseArchiveTarget(plan, epoch, options)
	archiveReceipt, err := j.databaseArchiveReceipt(archiveTarget)
	if err != nil || archiveReceipt == "" {
		return plan, request, source, archive, "", "", ErrDatabaseTarget
	}
	archive, _, err = j.readDatabaseArchiveMetadata(ctx, archiveTarget, archiveReceipt)
	if err != nil || archive.DatabaseProperties == nil || archive.PostgresMajor != 16 {
		return plan, request, source, archive, "", "", ErrDatabaseTarget
	}
	return plan, request, source, archive, configReceipt, archiveReceipt, nil
}

// CreateDockerRestoreDatabase creates a new template0 database on the original
// cluster and installs an active bootstrap fence. Existing or partially created
// targets are never adopted, dropped, overwritten or renamed.
func (j *HostJournal) CreateDockerRestoreDatabase(ctx context.Context, controller *database.RuntimeRelease, epoch int64, archiveOptions DatabaseArchiveOptions) (DatabaseTargetReceipt, error) {
	return j.createDockerRestoreDatabase(ctx, controller, epoch, archiveOptions, newDockerObserver)
}

func (j *HostJournal) createDockerRestoreDatabase(ctx context.Context, controller *database.RuntimeRelease, epoch int64, options DatabaseArchiveOptions, open func(string) (*dockerObserver, error)) (DatabaseTargetReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	payload, evidence, err := j.createDatabaseTarget(ctx, controller, epoch, options, open)
	if err != nil {
		return DatabaseTargetReceipt{}, err
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: j.state.Pending.Sequence, Observation: HostApplied, Evidence: evidence}); err != nil {
		return DatabaseTargetReceipt{}, err
	}
	return targetReceipt(payload), nil
}

// The caller owns j.mu across creation and acknowledgement. A durable protected
// completion record is the only recoverable proof of successful creation.
func (j *HostJournal) createDatabaseTarget(ctx context.Context, controller *database.RuntimeRelease, epoch int64, options DatabaseArchiveOptions, open func(string) (*dockerObserver, error)) (databaseTargetPayload, string, error) {
	var payload databaseTargetPayload
	if controller == nil {
		return payload, "", ErrDatabaseTarget
	}
	plan, request, source, archive, configReceipt, archiveReceipt, err := j.targetMaterial(ctx, epoch, options)
	if err != nil {
		return payload, "", err
	}
	target := databaseTargetHash(plan, epoch, archive.Target, archiveReceipt)
	if j.state.Pending.Sequence != 0 {
		return payload, "", ErrHostJournalState
	}
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == HostCreateRestoreDatabase {
			return payload, "", ErrHostJournalState
		}
	}
	name := databaseTargetName(target)
	configuration, err := targetDatabaseConfiguration(source, target)
	if err != nil {
		return payload, "", err
	}
	statement, err := archive.DatabaseProperties.createSQL(name)
	if err != nil {
		return payload, "", ErrDatabaseTarget
	}
	if _, err := j.root.Lstat(databaseTargetMetadataName(target)); !errors.Is(err, os.ErrNotExist) {
		return payload, "", ErrDatabaseTarget
	}
	if controller.WithActiveFence(ctx, request, plan.data.Observed.Database, func(database.RuntimeFenceState) error { return nil }) != nil {
		return payload, "", ErrDatabaseTarget
	}
	if quiet, err := controller.TryQuiesce(ctx); err != nil || !quiet {
		return payload, "", ErrDatabaseTarget
	}
	d, err := open(plan.data.EngineEndpoint)
	if err != nil {
		return payload, "", ErrDatabaseTarget
	}
	defer d.close()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(options.TimeoutSeconds)*time.Second)
	defer cancel()
	evidence := ""
	err = controller.WithQuiescentConnection(ctx, func(conn *sql.Conn) error {
		if err := validateDatabaseTargetSource(ctx, conn, d, plan, archive); err != nil {
			return err
		}
		if archive.DatabaseProperties.checkCreationPrivileges(ctx, conn, archive.Database.DatabaseOID) != nil {
			return ErrDatabaseTarget
		}
		var exists bool
		if conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_database WHERE datname=$1)`, name).Scan(&exists) != nil || exists {
			return ErrDatabaseTarget
		}
		if err := j.append(hostRecord{Kind: "intent", Effect: HostCreateRestoreDatabase, Target: target}); err != nil {
			return err
		}
		// CREATE DATABASE cannot run in a transaction. Keep it on the original
		// controller connection so ownership loss cannot silently reconnect it.
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return ErrDatabaseTarget
		}
		identity := archive.Database
		identity.DatabaseName = name
		if conn.QueryRowContext(ctx, `SELECT oid::bigint FROM pg_catalog.pg_database WHERE datname=$1`, name).Scan(&identity.DatabaseOID) != nil || identity.DatabaseOID == archive.Database.DatabaseOID {
			return ErrDatabaseTarget
		}
		db, err := openDockerDatabase(ctx, configuration, identity)
		if err != nil {
			return ErrDatabaseTarget
		}
		defer db.Close()
		targetController, bootstrapOID, err := database.BootstrapRuntimeRestore(ctx, db, request, identity, archive.Database)
		if err != nil {
			return ErrDatabaseTarget
		}
		defer targetController.Close()
		payload = databaseTargetPayload{Target: target, ArchiveTarget: archive.Target, ArchiveReceipt: archiveReceipt, Epoch: epoch,
			ConfigurationReceipt: configReceipt, Configuration: configuration, Database: identity, BootstrapOID: bootstrapOID}
		return targetController.WithQuiescentConnection(ctx, func(targetConn *sql.Conn) error {
			if archive.DatabaseProperties.apply(ctx, targetConn, name) != nil {
				return ErrDatabaseTarget
			}
			if verifyDatabaseTargetConnection(ctx, targetConn, payload, archive) != nil {
				return ErrDatabaseTarget
			}
			if validateDatabaseTargetSource(ctx, conn, d, plan, archive) != nil || configuration.validate() != nil {
				return ErrDatabaseTarget
			}
			payload.CompletedAt = time.Now().UTC()
			evidence, err = j.createDatabaseTargetMetadata(ctx, payload)
			return err
		})
	})
	if err != nil {
		return databaseTargetPayload{}, "", ErrDatabaseTarget
	}
	return payload, evidence, nil
}

func validateDatabaseTargetSource(ctx context.Context, conn *sql.Conn, d *dockerObserver, plan DockerHostPlan, archive databaseArchivePayload) error {
	if _, err := d.observePlannedStop(ctx, plan); err != nil {
		return ErrDatabaseTarget
	}
	for _, expected := range []hostPlanPackage{plan.data.Previous, plan.data.Candidate} {
		actual, _, err := observeHostPackage(HostPackageReference{expected.Directory, expected.ManifestSHA256})
		if err != nil || actual != expected {
			return ErrDatabaseTarget
		}
	}
	properties, err := readPostgresDatabaseProperties(ctx, conn, archive.Database.DatabaseOID)
	if err != nil || !archive.DatabaseProperties.same(properties) {
		return ErrDatabaseTarget
	}
	return nil
}

func verifyDatabaseTargetConnection(ctx context.Context, conn *sql.Conn, payload databaseTargetPayload, archive databaseArchivePayload) error {
	var oid int64
	if conn.QueryRowContext(ctx, `SELECT 'public.release_runtime_control'::pg_catalog.regclass::oid::bigint`).Scan(&oid) != nil || oid != payload.BootstrapOID {
		return ErrDatabaseTarget
	}
	properties, err := readPostgresDatabaseProperties(ctx, conn, payload.Database.DatabaseOID)
	if err != nil || !archive.DatabaseProperties.same(properties) {
		return ErrDatabaseTarget
	}
	return nil
}
