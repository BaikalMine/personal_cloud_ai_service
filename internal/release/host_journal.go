package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrHostJournalBusy     = errors.New("release host journal is owned")
	ErrHostJournalIdentity = errors.New("release host journal identity or permissions changed")
	ErrHostJournalCorrupt  = errors.New("release host journal is incomplete or invalid")
	ErrHostJournalState    = errors.New("release host journal transition refused")
	ErrHostJournalIO       = errors.New("release host journal persistence is uncertain; reopen and inspect")
	ErrHostJournalClosed   = errors.New("release host journal is closed or poisoned")
)

const hostJournalName = "host-journal.jsonl"
const maxHostJournalBytes = 16 << 20

// HostOperationSpec binds a non-secret operation ID and the reviewed plan to
// exact manifest bytes. PlanSHA256 must cover selected services, their live
// identities, database binding and persistent/recovery paths. This journal
// records those bindings; it does not independently verify or authorize them.
type HostOperationSpec struct {
	ID                      string `json:"id"`
	PreviousManifestSHA256  string `json:"previous_manifest_sha256"`
	CandidateManifestSHA256 string `json:"candidate_manifest_sha256"`
	PlanSHA256              string `json:"plan_sha256"`
}

type HostEffect string

const (
	HostFence                  HostEffect = "fence"
	HostStopService            HostEffect = "stop_service"
	HostBackup                 HostEffect = "backup"
	HostPrepareRestore         HostEffect = "prepare_restore"
	HostMigrate                HostEffect = "migrate"
	HostStartValidation        HostEffect = "start_validation"
	HostRevokeValidation       HostEffect = "revoke_validation"
	HostActivate               HostEffect = "activate"
	HostFenceRecovery          HostEffect = "fence_recovery"
	HostSelectRestore          HostEffect = "select_restore"
	HostPrepareRuntimeRecovery HostEffect = "prepare_runtime_recovery"
	HostPrepareDockerPlan      HostEffect = "prepare_docker_plan"
	HostPrepareDockerDatabase  HostEffect = "prepare_docker_database"
	HostArchiveDatabase        HostEffect = "archive_database"
	HostCreateRestoreDatabase  HostEffect = "create_restore_database"
	HostRestoreDatabase        HostEffect = "restore_database"
)

type HostObservation string

const (
	HostApplied    HostObservation = "applied"
	HostNotApplied HostObservation = "not_applied"
	HostUnknown    HostObservation = "unknown"
)

type HostPendingEffect struct {
	Sequence     uint64     `json:"sequence"`
	OwnerEpoch   uint64     `json:"owner_epoch"`
	Effect       HostEffect `json:"effect"`
	TargetSHA256 string     `json:"target_sha256"`
}

type HostJournalSnapshot struct {
	InstallationID string            `json:"installation_id"`
	OwnerEpoch     uint64            `json:"owner_epoch"`
	Sequence       uint64            `json:"sequence"`
	LastSHA256     string            `json:"last_sha256"`
	Operation      HostOperationSpec `json:"operation"`
	Pending        HostPendingEffect `json:"pending"`
	Finished       bool              `json:"finished"`
}

// HostJournalEvent is recorded evidence, not a fresh observation of a service.
// History includes owner changes and completed effects for the current operation
// so recovery cannot confuse pre-activation and post-activation failures.
type HostJournalEvent struct {
	Sequence       uint64          `json:"sequence"`
	OwnerEpoch     uint64          `json:"owner_epoch"`
	At             string          `json:"at"`
	Kind           string          `json:"kind"`
	Effect         HostEffect      `json:"effect,omitempty"`
	TargetSHA256   string          `json:"target_sha256,omitempty"`
	IntentSequence uint64          `json:"intent_sequence,omitempty"`
	Observation    HostObservation `json:"observation,omitempty"`
	EvidenceSHA256 string          `json:"evidence_sha256,omitempty"`
}

type hostRecord struct {
	Version      int               `json:"version"`
	Sequence     uint64            `json:"sequence"`
	Previous     string            `json:"previous"`
	At           string            `json:"at"`
	Kind         string            `json:"kind"`
	Epoch        uint64            `json:"epoch"`
	Installation string            `json:"installation,omitempty"`
	Root         string            `json:"root,omitempty"`
	File         string            `json:"file,omitempty"`
	Operation    HostOperationSpec `json:"operation"`
	Effect       HostEffect        `json:"effect,omitempty"`
	Target       string            `json:"target,omitempty"`
	Intent       uint64            `json:"intent,omitempty"`
	Observation  HostObservation   `json:"observation,omitempty"`
	Evidence     string            `json:"evidence,omitempty"`
}

type hostFrame struct {
	Record hostRecord `json:"record"`
	SHA256 string     `json:"sha256"`
}

// HostJournal owns one installation-wide OS lock until Close or process exit.
// Close never clears a pending intent, finishes an operation or changes runtime
// admission. All external effects remain the caller's responsibility.
type HostJournal struct {
	mu         sync.Mutex
	directory  string
	root       *os.Root
	dir        *os.File
	file       *os.File
	rootID     string
	fileID     string
	state      HostJournalSnapshot
	length     int64
	closed     bool
	poisoned   bool
	operations map[string]bool
	events     []HostJournalEvent
}

// InitializeHostJournal creates a NEW private directory. It never adopts an
// existing directory, repairs a damaged journal or changes existing ACLs.
// Keep this directory outside every database, package and restore destination.
func InitializeHostJournal(directory, installationID string) error {
	if !validHostDirectory(directory) || !validHostID(installationID) {
		return ErrHostJournalIdentity
	}
	if err := createPrivateHostDirectory(directory); err != nil {
		return ErrHostJournalIdentity
	}
	j, err := openHostFiles(directory, true)
	if err != nil {
		return err
	}
	defer j.Close()
	j.state.InstallationID = installationID
	if err := j.append(hostRecord{Kind: "initialize", Installation: installationID, Root: j.rootID, File: j.fileID}); err != nil {
		return err
	}
	if err := syncHostDirectory(j.dir); err != nil {
		return ErrHostJournalIO
	}
	return nil
}

// OpenHostJournal requires the original installation ID and existing journal.
// A new owner epoch is flushed before returning, including during recovery of
// an unresolved intent. An incomplete tail is retained and fails closed.
func OpenHostJournal(directory, installationID string) (*HostJournal, error) {
	if !validHostDirectory(directory) || !validHostID(installationID) {
		return nil, ErrHostJournalIdentity
	}
	j, err := openHostFiles(directory, false)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*HostJournal, error) { j.Close(); return nil, err }
	state, length, err := j.read()
	if err != nil {
		return fail(err)
	}
	if state.InstallationID != installationID {
		return fail(ErrHostJournalIdentity)
	}
	j.state, j.length = state, length
	if state.OwnerEpoch == ^uint64(0) {
		return fail(ErrHostJournalState)
	}
	if err := j.append(hostRecord{Kind: "owner", Epoch: state.OwnerEpoch + 1}); err != nil {
		return fail(err)
	}
	return j, nil
}

func validHostDirectory(directory string) bool {
	return filepath.IsAbs(directory) && filepath.Clean(directory) == directory && filepath.Dir(directory) != directory && localHostDirectory(directory)
}

func validHostID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value) && value != strings.Repeat("0", 32)
}

func validHostHash(value string) bool {
	return hashPattern.MatchString(value) && value != strings.Repeat("0", 64)
}

func validHostOperation(spec HostOperationSpec) bool {
	return validHostID(spec.ID) && validHostHash(spec.PreviousManifestSHA256) &&
		validHostHash(spec.CandidateManifestSHA256) && validHostHash(spec.PlanSHA256)
}

func validHostEffect(effect HostEffect) bool {
	switch effect {
	case HostFence, HostStopService, HostBackup, HostPrepareRestore, HostMigrate, HostStartValidation,
		HostRevokeValidation, HostActivate, HostFenceRecovery, HostSelectRestore:
		return true
	case HostPrepareRuntimeRecovery, HostPrepareDockerPlan, HostPrepareDockerDatabase, HostArchiveDatabase, HostCreateRestoreDatabase, HostRestoreDatabase:
		return true
	}
	return false
}

func openHostFiles(directory string, create bool) (*HostJournal, error) {
	j := &HostJournal{directory: directory}
	fail := func(err error) (*HostJournal, error) { j.Close(); return nil, err }
	var err error
	j.dir, err = os.Open(directory)
	if err != nil {
		return fail(ErrHostJournalIdentity)
	}
	j.root, err = os.OpenRoot(directory)
	if err != nil {
		return fail(ErrHostJournalIdentity)
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	j.file, err = j.root.OpenFile(hostJournalName, flags, 0o600)
	if err != nil {
		return fail(ErrHostJournalIdentity)
	}
	if err := lockHostJournal(j.file); err != nil {
		return fail(err)
	}
	j.rootID, err = hostFileIdentity(j.dir)
	if err != nil {
		return fail(ErrHostJournalIdentity)
	}
	j.fileID, err = hostFileIdentity(j.file)
	if err != nil {
		return fail(ErrHostJournalIdentity)
	}
	if err := j.checkFiles(); err != nil {
		return fail(err)
	}
	return j, nil
}

func (j *HostJournal) checkFiles() error {
	if j.closed || j.poisoned {
		return ErrHostJournalClosed
	}
	for _, entry := range []struct {
		file      *os.File
		name      string
		directory bool
		id        string
	}{
		{j.dir, j.directory, true, j.rootID}, {j.file, hostJournalName, false, j.fileID},
	} {
		opened, err := entry.file.Stat()
		if err != nil {
			return ErrHostJournalIdentity
		}
		var named os.FileInfo
		if entry.directory {
			named, err = os.Lstat(entry.name)
		} else {
			named, err = j.root.Lstat(entry.name)
		}
		if err != nil || !os.SameFile(named, opened) || named.Mode()&os.ModeSymlink != 0 ||
			(entry.directory && !opened.IsDir()) || (!entry.directory && !opened.Mode().IsRegular()) {
			return ErrHostJournalIdentity
		}
		id, err := hostFileIdentity(entry.file)
		if err != nil || id != entry.id || privateHostFile(entry.file, entry.directory) != nil {
			return ErrHostJournalIdentity
		}
	}
	// The rooted handle and separately opened directory must still name one object.
	rootInfo, err := j.root.Stat(".")
	dirInfo, dirErr := j.dir.Stat()
	if err != nil || dirErr != nil || !os.SameFile(rootInfo, dirInfo) {
		return ErrHostJournalIdentity
	}
	return nil
}

func (j *HostJournal) read() (HostJournalSnapshot, int64, error) {
	var state HostJournalSnapshot
	operations := make(map[string]bool)
	var events []HostJournalEvent
	if err := j.checkFiles(); err != nil {
		return state, 0, err
	}
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return state, 0, ErrHostJournalIO
	}
	payload, err := io.ReadAll(io.LimitReader(j.file, maxHostJournalBytes+1))
	if err != nil {
		return state, 0, ErrHostJournalIO
	}
	if len(payload) == 0 || len(payload) > maxHostJournalBytes || payload[len(payload)-1] != '\n' {
		return state, 0, ErrHostJournalCorrupt
	}
	for _, line := range bytes.Split(payload[:len(payload)-1], []byte{'\n'}) {
		if len(line) == 0 || len(line) > 4096 {
			return state, 0, ErrHostJournalCorrupt
		}
		var frame hostFrame
		if json.Unmarshal(line, &frame) != nil {
			return state, 0, ErrHostJournalCorrupt
		}
		canonical, _ := json.Marshal(frame)
		if !bytes.Equal(canonical, line) {
			return state, 0, ErrHostJournalCorrupt
		}
		body, _ := json.Marshal(frame.Record)
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != frame.SHA256 {
			return state, 0, ErrHostJournalCorrupt
		}
		if err := j.replay(&state, frame.Record); err != nil {
			return state, 0, err
		}
		if frame.Record.Kind == "begin" {
			id := frame.Record.Operation.ID
			if operations[id] {
				return state, 0, ErrHostJournalCorrupt
			}
			operations[id] = true
			events = nil
		}
		if state.Operation.ID != "" {
			r := frame.Record
			events = append(events, HostJournalEvent{Sequence: r.Sequence, OwnerEpoch: r.Epoch, At: r.At, Kind: r.Kind,
				Effect: r.Effect, TargetSHA256: r.Target, IntentSequence: r.Intent, Observation: r.Observation, EvidenceSHA256: r.Evidence})
		}
		state.LastSHA256 = frame.SHA256
	}
	j.operations = operations
	j.events = events
	return state, int64(len(payload)), nil
}

func (j *HostJournal) replay(state *HostJournalSnapshot, r hostRecord) error {
	if r.Version != 1 || state.Sequence == ^uint64(0) || r.Sequence != state.Sequence+1 || r.Previous != state.LastSHA256 {
		return ErrHostJournalCorrupt
	}
	at, err := time.Parse(time.RFC3339Nano, r.At)
	if err != nil || at.Location() != time.UTC || at.Format(time.RFC3339Nano) != r.At {
		return ErrHostJournalCorrupt
	}
	expected := hostRecord{Version: 1, Sequence: r.Sequence, Previous: r.Previous, At: r.At, Kind: r.Kind, Epoch: r.Epoch}
	if r.Kind != "initialize" && r.Kind != "owner" && (r.Epoch == 0 || r.Epoch != state.OwnerEpoch) {
		return ErrHostJournalCorrupt
	}
	switch r.Kind {
	case "initialize":
		if state.Sequence != 0 || r.Epoch != 0 || !validHostID(r.Installation) || r.Root != j.rootID || r.File != j.fileID {
			return ErrHostJournalIdentity
		}
		expected.Installation, expected.Root, expected.File = r.Installation, r.Root, r.File
		state.InstallationID = r.Installation
	case "owner":
		if state.Sequence == 0 || state.OwnerEpoch == ^uint64(0) || r.Epoch != state.OwnerEpoch+1 {
			return ErrHostJournalCorrupt
		}
		state.OwnerEpoch = r.Epoch
	case "begin":
		if !validHostOperation(r.Operation) || (state.Operation.ID != "" && !state.Finished) || r.Operation.ID == state.Operation.ID {
			return ErrHostJournalState
		}
		expected.Operation = r.Operation
		state.Operation, state.Finished = r.Operation, false
	case "intent":
		if state.Operation.ID == "" || state.Finished || state.Pending.Sequence != 0 || !validHostEffect(r.Effect) || !validHostHash(r.Target) {
			return ErrHostJournalState
		}
		expected.Effect, expected.Target = r.Effect, r.Target
		state.Pending = HostPendingEffect{Sequence: r.Sequence, OwnerEpoch: r.Epoch, Effect: r.Effect, TargetSHA256: r.Target}
	case "observe":
		if state.Pending.Sequence == 0 || state.Pending.Sequence != r.Intent || !validHostHash(r.Evidence) {
			return ErrHostJournalState
		}
		switch r.Observation {
		case HostApplied, HostNotApplied:
			state.Pending = HostPendingEffect{}
		case HostUnknown:
		default:
			return ErrHostJournalState
		}
		expected.Intent, expected.Observation, expected.Evidence = r.Intent, r.Observation, r.Evidence
	case "finish":
		if state.Operation.ID == "" || state.Finished || state.Pending.Sequence != 0 || !validHostHash(r.Evidence) {
			return ErrHostJournalState
		}
		expected.Evidence = r.Evidence
		state.Finished = true
	default:
		return ErrHostJournalCorrupt
	}
	if expected != r {
		return ErrHostJournalCorrupt
	}
	state.Sequence = r.Sequence
	return nil
}

func (j *HostJournal) validate() error {
	state, length, err := j.read()
	if err != nil {
		return err
	}
	if state != j.state || length != j.length {
		return ErrHostJournalIdentity
	}
	return nil
}

func (j *HostJournal) append(record hostRecord) error {
	if err := j.checkFiles(); err != nil {
		return err
	}
	if j.length != 0 {
		if err := j.validate(); err != nil {
			return err
		}
	}
	if record.Kind == "begin" && j.operations[record.Operation.ID] {
		return ErrHostJournalState
	}
	record.Version, record.Sequence, record.Previous = 1, j.state.Sequence+1, j.state.LastSHA256
	record.At = time.Now().UTC().Format(time.RFC3339Nano)
	if record.Kind != "initialize" && record.Kind != "owner" {
		record.Epoch = j.state.OwnerEpoch
	}
	next := j.state
	if err := j.replay(&next, record); err != nil {
		return err
	}
	body, _ := json.Marshal(record)
	digest := sha256.Sum256(body)
	next.LastSHA256 = hex.EncodeToString(digest[:])
	line, _ := json.Marshal(hostFrame{Record: record, SHA256: next.LastSHA256})
	line = append(line, '\n')
	if j.length+int64(len(line)) > maxHostJournalBytes {
		return ErrHostJournalState
	}
	position, err := j.file.Seek(0, io.SeekEnd)
	if err != nil || position != j.length {
		return ErrHostJournalIdentity
	}
	written, err := j.file.Write(line)
	if err != nil || written != len(line) {
		j.poisoned = true
		return ErrHostJournalIO
	}
	if err := j.file.Sync(); err != nil {
		j.poisoned = true
		return ErrHostJournalIO
	}
	if err := j.checkFiles(); err != nil {
		j.poisoned = true
		return ErrHostJournalIO
	}
	j.state, j.length = next, j.length+int64(len(line))
	return nil
}

func (j *HostJournal) Snapshot() (HostJournalSnapshot, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.validate(); err != nil {
		return HostJournalSnapshot{}, err
	}
	return j.state, nil
}

func (j *HostJournal) OperationHistory() (HostJournalSnapshot, []HostJournalEvent, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.validate(); err != nil {
		return HostJournalSnapshot{}, nil, err
	}
	return j.state, append([]HostJournalEvent(nil), j.events...), nil
}

func (j *HostJournal) Begin(spec HostOperationSpec) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.append(hostRecord{Kind: "begin", Operation: spec})
}

// Intent must succeed before dispatch. On any persistence error, do not dispatch.
func (j *HostJournal) Intent(effect HostEffect, targetSHA256 string) (HostPendingEffect, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.append(hostRecord{Kind: "intent", Effect: effect, Target: targetSHA256}); err != nil {
		return HostPendingEffect{}, err
	}
	return j.state.Pending, nil
}

// Observe accepts fresh evidence for the EXACT recorded intent. Unknown retains
// it across owner changes; neither an error nor a timeout permits a new effect.
func (j *HostJournal) Observe(sequence uint64, observation HostObservation, evidenceSHA256 string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.append(hostRecord{Kind: "observe", Intent: sequence, Observation: observation, Evidence: evidenceSHA256})
}

// Finish records a caller-verified terminal report hash, not deployment approval.
// The integrated coordinator must enforce stage, activation and recovery policy.
func (j *HostJournal) Finish(evidenceSHA256 string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.append(hostRecord{Kind: "finish", Evidence: evidenceSHA256})
}

func (j *HostJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	var err error
	if j.file != nil {
		err = errors.Join(err, j.file.Close())
	}
	if j.root != nil {
		err = errors.Join(err, j.root.Close())
	}
	if j.dir != nil {
		err = errors.Join(err, j.dir.Close())
	}
	if err != nil {
		return ErrHostJournalIO
	}
	return nil
}
