package release

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"

	"ai-access-gateway/internal/database"
)

var (
	ErrHostRecoveryUnavailable = errors.New("release runtime recovery material is unavailable or mismatched")
	ErrHostRecoveryAction      = errors.New("release runtime recovery action failed")
)

const maxHostRecoveryBytes = 32 << 10

// HostRuntimeRecoveryBinding is a caller-verified database identity and its
// PRE-fence epoch. The host owner epoch is separate. Restored databases require
// distinct bindings even when their copied application control records match.
type HostRuntimeRecoveryBinding struct {
	DatabaseSHA256 string `json:"database_sha256"`
	ExpectedEpoch  int64  `json:"expected_epoch"`
	// Explicit lineage for a copied active control record, not permission to
	// restore a database. The source is the operation's original DB binding.
	RestoreSourceSHA256 string `json:"restore_source_sha256,omitempty"`
}

type hostRecoveryContext struct {
	Version      int                        `json:"version"`
	Installation string                     `json:"installation"`
	Root         string                     `json:"root"`
	Journal      string                     `json:"journal"`
	File         string                     `json:"file"`
	Operation    HostOperationSpec          `json:"operation"`
	Binding      HostRuntimeRecoveryBinding `json:"binding"`
}

type hostRecoveryPayload struct {
	Context hostRecoveryContext `json:"context"`
	Token   string              `json:"token"`
}

type hostRecoveryEnvelope struct {
	Version    int    `json:"version"`
	Protection string `json:"protection"`
	Data       []byte `json:"data"`
}

func validHostRecoveryBinding(binding HostRuntimeRecoveryBinding) bool {
	return validHostHash(binding.DatabaseSHA256) && binding.ExpectedEpoch >= 0 && binding.ExpectedEpoch < math.MaxInt64 &&
		(binding.RestoreSourceSHA256 == "" || (validHostHash(binding.RestoreSourceSHA256) && binding.RestoreSourceSHA256 != binding.DatabaseSHA256))
}

func hostRecoveryDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (j *HostJournal) recoveryTarget(binding HostRuntimeRecoveryBinding) string {
	body, _ := json.Marshal(struct {
		Version      int                        `json:"version"`
		Installation string                     `json:"installation"`
		Operation    HostOperationSpec          `json:"operation"`
		Binding      HostRuntimeRecoveryBinding `json:"binding"`
	}{1, j.state.InstallationID, j.state.Operation, binding})
	return hostRecoveryDigest(body)
}

func (j *HostJournal) recoveryName(binding HostRuntimeRecoveryBinding) string {
	// One file per destination/epoch; a different lineage must not create a
	// second, conflicting credential for the same destination.
	binding.RestoreSourceSHA256 = ""
	return ".runtime-recovery-" + j.recoveryTarget(binding) + ".json"
}

func (j *HostJournal) recoverySourceToken(binding HostRuntimeRecoveryBinding) (string, error) {
	if binding.RestoreSourceSHA256 == "" {
		return "", nil
	}
	source := HostRuntimeRecoveryBinding{DatabaseSHA256: binding.RestoreSourceSHA256, ExpectedEpoch: binding.ExpectedEpoch}
	receipt, err := j.recoveryReceipt(source)
	if err != nil || receipt == "" {
		return "", ErrHostRecoveryUnavailable
	}
	request, _, err := j.readRuntimeRecovery(source, receipt)
	if err != nil {
		return "", err
	}
	return request.Token, nil
}

func (j *HostJournal) recoveryState(binding HostRuntimeRecoveryBinding) error {
	if err := j.validate(); err != nil {
		return err
	}
	if !validHostRecoveryBinding(binding) || j.state.Operation.ID == "" || j.state.Finished {
		return ErrHostJournalState
	}
	return nil
}

// recoveryReceipt finds the one committed preparation for this exact binding.
// Never regenerate a lost token after a successful preparation, even if a later
// generic intent mistakenly asks to prepare the same binding again.
func (j *HostJournal) recoveryReceipt(binding HostRuntimeRecoveryBinding) (string, error) {
	target := j.recoveryTarget(binding)
	intents := make(map[uint64]bool)
	receipt := ""
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == HostPrepareRuntimeRecovery && event.TargetSHA256 == target {
			intents[event.Sequence] = true
		}
		if event.Kind == "observe" && intents[event.IntentSequence] && event.Observation == HostApplied {
			if receipt != "" {
				return "", ErrHostRecoveryUnavailable
			}
			receipt = event.EvidenceSHA256
		}
	}
	return receipt, nil
}

// PrepareRuntimeRecovery persists one random controller token before any DB
// fence can use it, or explicitly rebinds an original token for a copied control
// record. It never overwrites, rotates or deletes existing material.
// On interruption, use ReconcileRuntimeRecovery instead of repeating creation.
func (j *HostJournal) PrepareRuntimeRecovery(binding HostRuntimeRecoveryBinding) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.recoveryState(binding); err != nil {
		return err
	}
	if j.state.Pending.Sequence != 0 {
		return ErrHostJournalState
	}
	receipt, err := j.recoveryReceipt(binding)
	if err != nil {
		return err
	}
	if receipt != "" {
		return ErrHostJournalState
	}
	if _, err := j.root.Lstat(j.recoveryName(binding)); !errors.Is(err, os.ErrNotExist) {
		return ErrHostRecoveryUnavailable
	}
	if _, err := j.recoverySourceToken(binding); err != nil {
		return err
	}
	if err := j.append(hostRecord{Kind: "intent", Effect: HostPrepareRuntimeRecovery, Target: j.recoveryTarget(binding)}); err != nil {
		return err
	}
	sequence := j.state.Pending.Sequence
	digest, err := j.createRuntimeRecovery(binding)
	if err != nil {
		return err
	}
	return j.append(hostRecord{Kind: "observe", Intent: sequence, Observation: HostApplied, Evidence: digest})
}

// ReconcileRuntimeRecovery observes only the exact interrupted preparation.
// A complete bound file is accepted without recreating it. Absence is recorded
// as not_applied, without a retry. Partial/invalid files remain untouched and
// unresolved. This never resolves an uncertain database or service action.
func (j *HostJournal) ReconcileRuntimeRecovery(binding HostRuntimeRecoveryBinding) (HostObservation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.recoveryState(binding); err != nil {
		return "", err
	}
	pending := j.state.Pending
	if pending.Effect != HostPrepareRuntimeRecovery || pending.TargetSHA256 != j.recoveryTarget(binding) {
		return "", ErrHostJournalState
	}
	receipt, err := j.recoveryReceipt(binding)
	if err != nil {
		return "", err
	}
	if receipt != "" {
		return "", ErrHostJournalState
	}
	_, err = j.root.Lstat(j.recoveryName(binding))
	observation, evidence := HostApplied, ""
	if errors.Is(err, os.ErrNotExist) {
		observation = HostNotApplied
		evidence = hostRecoveryDigest([]byte("release-runtime-recovery/absent/v1:" + j.recoveryTarget(binding)))
	} else {
		if err != nil {
			return "", ErrHostRecoveryUnavailable
		}
		_, evidence, err = j.readRuntimeRecovery(binding, "")
		if err != nil {
			return "", err
		}
		if err := j.syncPrivateMaterial(j.recoveryName(binding), maxHostRecoveryBytes); err != nil {
			return "", err
		}
		if _, _, err := j.readRuntimeRecovery(binding, evidence); err != nil {
			return "", err
		}
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: pending.Sequence, Observation: observation, Evidence: evidence}); err != nil {
		return "", err
	}
	return observation, nil
}

// WithRuntimeRecovery supplies credentials only to a trusted in-process
// callback, while retaining the host mutex and OS lock. The callback must not
// call this journal recursively. It must freshly verify its DB binding and
// journal external effects separately; this method is not deployment approval.
// It neither opens a DB nor changes its fence, and callback failures are redacted.
// An error can occur AFTER the callback ran; it never proves an external effect
// was not applied. Recovery material is rechecked before returning success.
func (j *HostJournal) WithRuntimeRecovery(binding HostRuntimeRecoveryBinding, work func(database.RuntimeReleaseRequest) error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.recoveryState(binding); err != nil {
		return err
	}
	if work == nil {
		return ErrHostJournalState
	}
	receipt, err := j.recoveryReceipt(binding)
	if err != nil {
		return err
	}
	if receipt == "" {
		return ErrHostRecoveryUnavailable
	}
	request, _, err := j.readRuntimeRecovery(binding, receipt)
	if err != nil {
		return err
	}
	if err := work(request); err != nil {
		return ErrHostRecoveryAction
	}
	if _, _, err := j.readRuntimeRecovery(binding, receipt); err != nil {
		return err
	}
	return j.validate()
}

func (j *HostJournal) recoveryContext(file *os.File, binding HostRuntimeRecoveryBinding) (hostRecoveryContext, error) {
	id, err := hostFileIdentity(file)
	if err != nil {
		return hostRecoveryContext{}, ErrHostRecoveryUnavailable
	}
	return hostRecoveryContext{Version: 1, Installation: j.state.InstallationID, Root: j.rootID, Journal: j.fileID,
		File: id, Operation: j.state.Operation, Binding: binding}, nil
}

func (j *HostJournal) checkRecoveryFile(file *os.File, name string) error {
	return j.checkPrivateMaterialFile(file, name, maxHostRecoveryBytes)
}

func (j *HostJournal) checkPrivateMaterialFile(file *os.File, name string, maximum int64) error {
	if err := j.checkFiles(); err != nil {
		return err
	}
	named, err := j.root.Lstat(name)
	if err != nil || !named.Mode().IsRegular() || named.Size() > maximum {
		return ErrHostRecoveryUnavailable
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(named, opened) || privateHostFile(file, false) != nil {
		return ErrHostRecoveryUnavailable
	}
	return nil
}

// A process may have died after writing bytes but before flushing them. Recovery
// must establish durability before it records a successful file preparation.
func (j *HostJournal) syncPrivateMaterial(name string, maximum int64) error {
	file, err := j.root.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return ErrHostRecoveryUnavailable
	}
	checkErr := j.checkPrivateMaterialFile(file, name, maximum)
	if checkErr == nil {
		checkErr = errors.Join(file.Sync(), syncHostDirectory(j.dir))
	}
	checkErr = errors.Join(checkErr, file.Close())
	if checkErr != nil {
		j.poisoned = true
		return ErrHostJournalIO
	}
	return nil
}

func (j *HostJournal) createRuntimeRecovery(binding HostRuntimeRecoveryBinding) (string, error) {
	controllerToken, err := j.recoverySourceToken(binding)
	if err != nil {
		return "", err
	}
	name := j.recoveryName(binding)
	file, err := j.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", ErrHostRecoveryUnavailable
	}
	defer file.Close()
	if err := j.checkRecoveryFile(file, name); err != nil {
		return "", err
	}
	bound, err := j.recoveryContext(file, binding)
	if err != nil {
		return "", err
	}
	if controllerToken == "" {
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", ErrHostRecoveryUnavailable
		}
		controllerToken = hex.EncodeToString(token[:])
		clear(token[:])
	}
	plaintext, _ := json.Marshal(hostRecoveryPayload{Context: bound, Token: controllerToken})
	defer clear(plaintext)
	entropy, _ := json.Marshal(bound)
	sealed, err := protectHostRecovery(plaintext, entropy)
	if err != nil {
		return "", ErrHostRecoveryUnavailable
	}
	defer clear(sealed)
	payload, _ := json.Marshal(hostRecoveryEnvelope{Version: 1, Protection: hostRecoveryProtection, Data: sealed})
	defer clear(payload)
	if len(payload) > maxHostRecoveryBytes {
		return "", ErrHostRecoveryUnavailable
	}
	written, err := file.Write(payload)
	if err != nil || written != len(payload) {
		j.poisoned = true
		return "", ErrHostJournalIO
	}
	if err := errors.Join(file.Sync(), syncHostDirectory(j.dir)); err != nil {
		j.poisoned = true
		return "", ErrHostJournalIO
	}
	if err := j.checkRecoveryFile(file, name); err != nil {
		j.poisoned = true
		return "", ErrHostJournalIO
	}
	digest := hostRecoveryDigest(payload)
	// Read back through the same bound file identity before committing evidence.
	if _, actual, err := j.readRuntimeRecovery(binding, digest); err != nil || actual != digest {
		j.poisoned = true
		return "", ErrHostJournalIO
	}
	return digest, nil
}

func (j *HostJournal) readRuntimeRecovery(binding HostRuntimeRecoveryBinding, expectedSHA256 string) (database.RuntimeReleaseRequest, string, error) {
	var empty database.RuntimeReleaseRequest
	name := j.recoveryName(binding)
	info, err := j.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxHostRecoveryBytes {
		return empty, "", ErrHostRecoveryUnavailable
	}
	file, err := j.root.Open(name)
	if err != nil {
		return empty, "", ErrHostRecoveryUnavailable
	}
	defer file.Close()
	if err := j.checkRecoveryFile(file, name); err != nil {
		return empty, "", err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return empty, "", ErrHostRecoveryUnavailable
	}
	bound, err := j.recoveryContext(file, binding)
	if err != nil {
		return empty, "", err
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxHostRecoveryBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxHostRecoveryBytes {
		return empty, "", ErrHostRecoveryUnavailable
	}
	defer clear(payload)
	digest := hostRecoveryDigest(payload)
	if expectedSHA256 != "" && digest != expectedSHA256 {
		return empty, "", ErrHostRecoveryUnavailable
	}
	var envelope hostRecoveryEnvelope
	if json.Unmarshal(payload, &envelope) != nil || envelope.Version != 1 || envelope.Protection != hostRecoveryProtection {
		return empty, "", ErrHostRecoveryUnavailable
	}
	canonical, _ := json.Marshal(envelope)
	defer clear(canonical)
	defer clear(envelope.Data)
	if !bytes.Equal(payload, canonical) {
		return empty, "", ErrHostRecoveryUnavailable
	}
	entropy, _ := json.Marshal(bound)
	plaintext, err := unprotectHostRecovery(envelope.Data, entropy)
	if err != nil {
		return empty, "", ErrHostRecoveryUnavailable
	}
	defer clear(plaintext)
	var secret hostRecoveryPayload
	if json.Unmarshal(plaintext, &secret) != nil || secret.Context != bound || !validHostHash(secret.Token) {
		return empty, "", ErrHostRecoveryUnavailable
	}
	canonicalSecret, _ := json.Marshal(secret)
	defer clear(canonicalSecret)
	if !bytes.Equal(plaintext, canonicalSecret) {
		return empty, "", ErrHostRecoveryUnavailable
	}
	if err := j.checkRecoveryFile(file, name); err != nil {
		return empty, "", err
	}
	return database.RuntimeReleaseRequest{OperationID: bound.Operation.ID, Token: secret.Token, ExpectedEpoch: binding.ExpectedEpoch,
		PreviousManifestHash: bound.Operation.PreviousManifestSHA256, CandidateManifestHash: bound.Operation.CandidateManifestSHA256}, digest, nil
}
