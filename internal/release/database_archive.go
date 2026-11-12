package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"ai-access-gateway/internal/database"
)

const databaseArchiveScope = "postgres_database_archive_v1"
const maxDatabaseArchiveMetadataBytes = 128 << 10

// DatabaseArchiveReceipt describes one database archive, not a coherent
// database/files backup, a tested restore or permission to migrate/activate.
type DatabaseArchiveReceipt struct {
	Scope          string `json:"scope"`
	TargetSHA256   string `json:"target_sha256"`
	ArchiveSHA256  string `json:"archive_sha256"`
	Bytes          int64  `json:"bytes"`
	CoherentBackup bool   `json:"coherent_backup"`
	RestoreTested  bool   `json:"restore_tested"`
}

type archiveFileProof struct {
	Identity string `json:"identity"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
}

type databaseArchivePayload struct {
	Context              hostPlanContext                  `json:"context"`
	Target               string                           `json:"target"`
	Options              DatabaseArchiveOptions           `json:"options"`
	Database             database.RuntimeDatabaseIdentity `json:"database"`
	Epoch                int64                            `json:"epoch"`
	ConfigurationReceipt string                           `json:"configuration_receipt"`
	ToolIdentity         string                           `json:"tool_identity"`
	PostgresMajor        int                              `json:"postgres_major"`
	ObservationSHA256    string                           `json:"observation_sha256"`
	Archive              archiveFileProof                 `json:"archive"`
	CompletedAt          time.Time                        `json:"completed_at"`
	DatabaseProperties   *postgresDatabaseProperties      `json:"database_properties,omitempty"`
}

func databaseArchiveTarget(plan DockerHostPlan, epoch int64, options DatabaseArchiveOptions) string {
	body, _ := json.Marshal(struct {
		Scope   string
		Plan    HostOperationSpec
		Epoch   int64
		Options DatabaseArchiveOptions
	}{databaseArchiveScope, plan.Operation(), epoch, options})
	return hostRecoveryDigest(body)
}

func databaseArchiveNames(target string) (archive, metadata, password, absent string) {
	base := ".database-archive-" + target
	return base + ".dump", base + ".json", base + ".pgpass", base + ".absent"
}

func (j *HostJournal) archiveProof(ctx context.Context, name string, maximum int64) (archiveFileProof, error) {
	var empty archiveFileProof
	file, err := j.root.Open(name)
	if err != nil {
		return empty, ErrDatabaseArchive
	}
	defer file.Close()
	if j.checkPrivateMaterialFile(file, name, maximum) != nil {
		return empty, ErrDatabaseArchive
	}
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 {
		return empty, ErrDatabaseArchive
	}
	id, err := hostFileIdentity(file)
	if err != nil {
		return empty, ErrDatabaseArchive
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(archiveContextReader{ctx, file}, maximum+1))
	if err != nil || size != info.Size() || j.checkPrivateMaterialFile(file, name, maximum) != nil {
		return empty, ErrDatabaseArchive
	}
	return archiveFileProof{Identity: id, SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: size}, nil
}

func (j *HostJournal) archiveMaterial(epoch int64) (DockerHostPlan, database.RuntimeReleaseRequest, dockerDatabaseConfiguration, string, error) {
	plan, request, err := j.dockerServiceMaterial(epoch)
	if err != nil {
		return plan, request, dockerDatabaseConfiguration{}, "", err
	}
	receipt, err := j.databaseConfigReceipt()
	if err != nil || receipt == "" {
		return plan, request, dockerDatabaseConfiguration{}, "", ErrDatabaseArchive
	}
	configuration, _, err := j.readDockerDatabaseConfiguration(receipt)
	if err != nil || !j.hasAppliedEffect(HostFence, j.recoveryTarget(plan.RuntimeRecoveryBinding(epoch))) {
		return plan, request, configuration, "", ErrDatabaseArchive
	}
	stopped := false
	for grace := 0; grace <= 120; grace++ {
		stopped = stopped || j.hasAppliedEffect(HostStopService, dockerStopTarget(plan, epoch, grace))
	}
	if !stopped {
		return plan, request, configuration, "", ErrDatabaseArchive
	}
	return plan, request, configuration, receipt, nil
}

func archiveReceipt(payload databaseArchivePayload) DatabaseArchiveReceipt {
	return DatabaseArchiveReceipt{Scope: databaseArchiveScope, TargetSHA256: payload.Target,
		ArchiveSHA256: payload.Archive.SHA256, Bytes: payload.Archive.Bytes}
}

// ArchiveDockerDatabase holds the original host/controller ownership and an
// exported SQL snapshot until pg_dump finishes. It never calls backup.ps1,
// changes a service, grants privileges or treats an archive as a tested restore.
// Privileged SQL/host actors must be excluded operationally; cooperative runtime
// locks cannot stop an unmanaged SQL writer or independently fence agent files.
func (j *HostJournal) ArchiveDockerDatabase(ctx context.Context, controller *database.RuntimeRelease, epoch int64, options DatabaseArchiveOptions) (DatabaseArchiveReceipt, error) {
	return j.archiveDockerDatabase(ctx, controller, epoch, options, newDockerObserver, runPostgreSQLDump)
}

func (j *HostJournal) archiveDockerDatabase(ctx context.Context, controller *database.RuntimeRelease, epoch int64, options DatabaseArchiveOptions,
	open func(string) (*dockerObserver, error), dump func(context.Context, DatabaseArchiveOptions, []string, string, string, io.Writer) error) (DatabaseArchiveReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	created, err := j.createDockerDatabaseArchive(ctx, controller, epoch, options, open, dump)
	if err != nil {
		return DatabaseArchiveReceipt{}, err
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: j.state.Pending.Sequence, Observation: HostApplied, Evidence: created.evidence}); err != nil {
		return DatabaseArchiveReceipt{}, err
	}
	return created.receipt, nil
}

type createdDatabaseArchive struct {
	receipt  DatabaseArchiveReceipt
	evidence string
}

// The host mutex must enclose the effect and its later journal acknowledgement.
// A complete protected record can survive a crash between these two steps.
func (j *HostJournal) createDockerDatabaseArchive(ctx context.Context, controller *database.RuntimeRelease, epoch int64, options DatabaseArchiveOptions,
	open func(string) (*dockerObserver, error), dump func(context.Context, DatabaseArchiveOptions, []string, string, string, io.Writer) error) (createdDatabaseArchive, error) {
	var empty createdDatabaseArchive
	if controller == nil || !options.valid() {
		return empty, ErrDatabaseArchive
	}
	plan, request, configuration, configurationReceipt, err := j.archiveMaterial(epoch)
	if err != nil {
		return empty, err
	}
	target := databaseArchiveTarget(plan, epoch, options)
	if j.state.Pending.Sequence != 0 || j.hasAppliedEffect(HostArchiveDatabase, target) {
		return empty, ErrHostJournalState
	}
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == HostArchiveDatabase && event.TargetSHA256 != target {
			return empty, ErrHostJournalState
		}
	}
	archiveName, metadataName, passwordName, absentName := databaseArchiveNames(target)
	for _, name := range []string{archiveName, metadataName, passwordName, absentName} {
		if _, err := j.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return empty, ErrDatabaseArchive
		}
	}
	toolIdentity, err := observePostgreSQLDumpTool(options.Tool)
	if err != nil {
		return empty, err
	}
	password, err := databaseArchivePassword(configuration)
	if err != nil {
		return empty, err
	}
	defer clear(password)
	environment, err := databaseArchiveEnvironment(configuration, j.directory, filepath.Join(j.directory, passwordName), filepath.Join(j.directory, absentName))
	if err != nil {
		return empty, err
	}
	if err = controller.WithActiveFence(ctx, request, plan.data.Observed.Database, func(database.RuntimeFenceState) error { return nil }); err != nil {
		return empty, ErrDatabaseArchive
	}
	quiescent, err := controller.TryQuiesce(ctx)
	if err != nil || !quiescent {
		return empty, ErrDatabaseArchive
	}
	d, err := open(plan.data.EngineEndpoint)
	if err != nil {
		return empty, ErrDatabaseArchive
	}
	defer d.close()
	if _, err := d.observePlannedStop(ctx, plan); err != nil {
		return empty, ErrDatabaseArchive
	}
	previousRef, previous, err := observeHostPackage(HostPackageReference{plan.data.Previous.Directory, plan.data.Previous.ManifestSHA256})
	if err != nil || previousRef != plan.data.Previous {
		return empty, ErrDatabaseArchive
	}
	candidateRef, candidate, err := observeHostPackage(HostPackageReference{plan.data.Candidate.Directory, plan.data.Candidate.ManifestSHA256})
	if err != nil || candidateRef != plan.data.Candidate {
		return empty, ErrDatabaseArchive
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(options.TimeoutSeconds)*time.Second)
	defer cancel()
	payload := databaseArchivePayload{Target: target, Options: options, Database: plan.data.Observed.Database,
		Epoch: epoch, ConfigurationReceipt: configurationReceipt, ToolIdentity: toolIdentity}
	err = controller.WithQuiescentConnection(ctx, func(conn *sql.Conn) error {
		report, err := InspectDatabaseOnConnection(ctx, conn, previous, candidate)
		if err != nil || !report.DatabaseChecksPassed {
			return ErrDatabaseArchive
		}
		body, _ := json.Marshal(report)
		payload.ObservationSHA256 = hostRecoveryDigest(body)
		var version int
		if conn.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&version) != nil {
			return ErrDatabaseArchive
		}
		payload.PostgresMajor = version / 10000
		if payload.PostgresMajor < 10 || payload.PostgresMajor > 99 {
			return ErrDatabaseArchive
		}
		if checkPostgreSQLDumpVersion(ctx, options.Tool, environment, j.directory, payload.PostgresMajor) != nil {
			return ErrDatabaseArchive
		}
		id, err := observePostgreSQLDumpTool(options.Tool)
		if err != nil || id != toolIdentity {
			return ErrDatabaseArchive
		}
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return ErrDatabaseArchive
		}
		defer tx.Rollback()
		if payload.PostgresMajor == 16 {
			properties, err := readPostgresDatabaseProperties(ctx, tx, payload.Database.DatabaseOID)
			if err != nil {
				return ErrDatabaseArchive
			}
			payload.DatabaseProperties = &properties
		}
		var snapshot string
		if tx.QueryRowContext(ctx, `SELECT pg_export_snapshot()`).Scan(&snapshot) != nil || !postgresSnapshotID.MatchString(snapshot) {
			return ErrDatabaseArchive
		}
		if err := j.append(hostRecord{Kind: "intent", Effect: HostArchiveDatabase, Target: target}); err != nil {
			return err
		}
		if err := j.writeArchivePassword(passwordName, password); err != nil {
			return err
		}
		passwordProof, err := j.archiveProof(ctx, passwordName, maxDockerDatabaseURLBytes*2)
		if err != nil {
			return err
		}
		defer j.removeArchivePassword(passwordName, passwordProof)
		file, err := j.root.OpenFile(archiveName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return ErrDatabaseArchive
		}
		defer file.Close()
		if j.checkPrivateMaterialFile(file, archiveName, options.MaxBytes) != nil || configuration.validate() != nil {
			return ErrDatabaseArchive
		}
		if err := dump(ctx, options, environment, j.directory, snapshot, file); err != nil {
			return ErrDatabaseArchive
		}
		if tx.Commit() != nil || j.checkPrivateMaterialFile(file, archiveName, options.MaxBytes) != nil || file.Sync() != nil || syncHostDirectory(j.dir) != nil {
			return ErrDatabaseArchive
		}
		payload.Archive, err = j.archiveProof(ctx, archiveName, options.MaxBytes)
		if err != nil || payload.Archive.Bytes < 5 {
			return ErrDatabaseArchive
		}
		var magic [5]byte
		if _, err := file.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "PGDMP" {
			return ErrDatabaseArchive
		}
		id, err = observePostgreSQLDumpTool(options.Tool)
		if err != nil || id != toolIdentity || configuration.validate() != nil {
			return ErrDatabaseArchive
		}
		if _, err := d.observePlannedStop(ctx, plan); err != nil {
			return ErrDatabaseArchive
		}
		if _, _, _, _, err := j.archiveMaterial(epoch); err != nil {
			return ErrDatabaseArchive
		}
		previousAfter, _, err := observeHostPackage(HostPackageReference{plan.data.Previous.Directory, plan.data.Previous.ManifestSHA256})
		if err != nil || previousAfter != previousRef {
			return ErrDatabaseArchive
		}
		candidateAfter, _, err := observeHostPackage(HostPackageReference{plan.data.Candidate.Directory, plan.data.Candidate.ManifestSHA256})
		if err != nil || candidateAfter != candidateRef {
			return ErrDatabaseArchive
		}
		if _, err := j.root.Lstat(absentName); !errors.Is(err, os.ErrNotExist) {
			return ErrDatabaseArchive
		}
		if payload.DatabaseProperties != nil {
			current, err := readPostgresDatabaseProperties(ctx, conn, payload.Database.DatabaseOID)
			if err != nil || !payload.DatabaseProperties.same(current) {
				return ErrDatabaseArchive
			}
		}
		if err := j.removeArchivePassword(passwordName, passwordProof); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return empty, ErrDatabaseArchive
	}
	payload.CompletedAt = time.Now().UTC()
	digest, err := j.createDatabaseArchiveMetadata(ctx, metadataName, payload)
	if err != nil {
		return empty, err
	}
	return createdDatabaseArchive{receipt: archiveReceipt(payload), evidence: digest}, nil
}

var postgresSnapshotID = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{8}-[1-9][0-9]*$`)

type archiveContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r archiveContextReader) Read(body []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(body)
}

func (j *HostJournal) writeArchivePassword(name string, body []byte) error {
	file, err := j.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrDatabaseArchive
	}
	defer file.Close()
	if j.checkPrivateMaterialFile(file, name, maxDockerDatabaseURLBytes*2) != nil {
		return ErrDatabaseArchive
	}
	n, err := file.Write(body)
	if err != nil || n != len(body) || file.Sync() != nil || syncHostDirectory(j.dir) != nil {
		return ErrDatabaseArchive
	}
	return nil
}

func (j *HostJournal) removeArchivePassword(name string, expected archiveFileProof) error {
	if _, err := j.root.Lstat(name); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	observed, err := j.archiveProof(context.Background(), name, maxDockerDatabaseURLBytes*2)
	if err != nil || observed != expected || j.root.Remove(name) != nil || syncHostDirectory(j.dir) != nil {
		return ErrDatabaseArchive
	}
	return nil
}

func (j *HostJournal) createDatabaseArchiveMetadata(ctx context.Context, name string, payload databaseArchivePayload) (string, error) {
	file, err := j.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", ErrDatabaseArchive
	}
	defer file.Close()
	if j.checkPrivateMaterialFile(file, name, maxDatabaseArchiveMetadataBytes) != nil {
		return "", ErrDatabaseArchive
	}
	payload.Context, err = j.planContext(file)
	if err != nil {
		return "", ErrDatabaseArchive
	}
	payload.Context.Purpose = databaseArchiveScope
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return "", ErrDatabaseArchive
	}
	defer clear(plaintext)
	entropy, _ := json.Marshal(payload.Context)
	sealed, err := protectHostPlan(plaintext, entropy)
	if err != nil {
		return "", ErrDatabaseArchive
	}
	defer clear(sealed)
	body, _ := json.Marshal(hostRecoveryEnvelope{Version: 1, Protection: hostRecoveryProtection, Data: sealed})
	defer clear(body)
	if len(body) > maxDatabaseArchiveMetadataBytes {
		return "", ErrDatabaseArchive
	}
	n, err := file.Write(body)
	if err != nil || n != len(body) || file.Sync() != nil || syncHostDirectory(j.dir) != nil {
		return "", ErrDatabaseArchive
	}
	digest := hostRecoveryDigest(body)
	if _, _, err := j.readDatabaseArchiveMetadata(ctx, payload.Target, digest); err != nil {
		return "", ErrDatabaseArchive
	}
	return digest, nil
}

func (j *HostJournal) readDatabaseArchiveMetadata(ctx context.Context, target, expected string) (databaseArchivePayload, string, error) {
	var empty databaseArchivePayload
	if !validHostHash(target) {
		return empty, "", ErrDatabaseArchive
	}
	archiveName, metadataName, passwordName, absentName := databaseArchiveNames(target)
	for _, name := range []string{passwordName, absentName} {
		if _, err := j.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return empty, "", ErrDatabaseArchive
		}
	}
	file, err := j.root.Open(metadataName)
	if err != nil {
		return empty, "", ErrDatabaseArchive
	}
	defer file.Close()
	if j.checkPrivateMaterialFile(file, metadataName, maxDatabaseArchiveMetadataBytes) != nil {
		return empty, "", ErrDatabaseArchive
	}
	bound, err := j.planContext(file)
	if err != nil {
		return empty, "", ErrDatabaseArchive
	}
	bound.Purpose = databaseArchiveScope
	body, err := io.ReadAll(io.LimitReader(file, maxDatabaseArchiveMetadataBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxDatabaseArchiveMetadataBytes {
		return empty, "", ErrDatabaseArchive
	}
	defer clear(body)
	digest := hostRecoveryDigest(body)
	if expected != "" && digest != expected {
		return empty, "", ErrDatabaseArchive
	}
	var envelope hostRecoveryEnvelope
	if json.Unmarshal(body, &envelope) != nil || envelope.Version != 1 || envelope.Protection != hostRecoveryProtection {
		return empty, "", ErrDatabaseArchive
	}
	defer clear(envelope.Data)
	canonical, _ := json.Marshal(envelope)
	defer clear(canonical)
	if !bytes.Equal(body, canonical) {
		return empty, "", ErrDatabaseArchive
	}
	entropy, _ := json.Marshal(bound)
	plaintext, err := unprotectHostPlan(envelope.Data, entropy)
	if err != nil {
		return empty, "", ErrDatabaseArchive
	}
	defer clear(plaintext)
	var payload databaseArchivePayload
	if json.Unmarshal(plaintext, &payload) != nil || payload.Context != bound || payload.Target != target || !payload.Options.valid() ||
		!validHostHash(payload.ToolIdentity) || !validHostHash(payload.ConfigurationReceipt) || !validHostHash(payload.ObservationSHA256) ||
		payload.PostgresMajor < 10 || payload.PostgresMajor > 99 || payload.CompletedAt.IsZero() {
		return empty, "", ErrDatabaseArchive
	}
	if payload.DatabaseProperties != nil && (payload.PostgresMajor != 16 || !payload.DatabaseProperties.valid()) {
		return empty, "", ErrDatabaseArchive
	}
	canonicalPayload, _ := json.Marshal(payload)
	defer clear(canonicalPayload)
	if !bytes.Equal(plaintext, canonicalPayload) {
		return empty, "", ErrDatabaseArchive
	}
	plan, _, _, receipt, err := j.archiveMaterial(payload.Epoch)
	if err != nil || receipt != payload.ConfigurationReceipt || payload.Database != plan.data.Observed.Database ||
		databaseArchiveTarget(plan, payload.Epoch, payload.Options) != target {
		return empty, "", ErrDatabaseArchive
	}
	proof, err := j.archiveProof(ctx, archiveName, payload.Options.MaxBytes)
	if err != nil || proof != payload.Archive || j.checkPrivateMaterialFile(file, metadataName, maxDatabaseArchiveMetadataBytes) != nil {
		return empty, "", ErrDatabaseArchive
	}
	return payload, digest, nil
}
