package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

type hostPlanContext struct {
	Version      int               `json:"version"`
	Purpose      string            `json:"purpose"`
	Installation string            `json:"installation"`
	Root         string            `json:"root"`
	Journal      string            `json:"journal"`
	File         string            `json:"file"`
	Operation    HostOperationSpec `json:"operation"`
}

type hostPlanPayload struct {
	Context hostPlanContext    `json:"context"`
	Plan    dockerHostPlanData `json:"plan"`
}

func (j *HostJournal) planName() string {
	return ".docker-plan-" + j.state.Operation.PlanSHA256 + ".json"
}

func (j *HostJournal) planState() error {
	if err := j.validate(); err != nil {
		return err
	}
	if j.state.Operation.ID == "" || j.state.Finished {
		return ErrHostJournalState
	}
	return nil
}

func (j *HostJournal) planContext(file *os.File) (hostPlanContext, error) {
	id, err := hostFileIdentity(file)
	if err != nil {
		return hostPlanContext{}, ErrHostPlanUnavailable
	}
	return hostPlanContext{Version: 1, Purpose: dockerHostPlanScope, Installation: j.state.InstallationID,
		Root: j.rootID, Journal: j.fileID, File: id, Operation: j.state.Operation}, nil
}

func (j *HostJournal) planReceipt() (string, error) {
	intents := make(map[uint64]bool)
	receipt := ""
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == HostPrepareDockerPlan && event.TargetSHA256 == j.state.Operation.PlanSHA256 {
			intents[event.Sequence] = true
		}
		if event.Kind == "observe" && intents[event.IntentSequence] && event.Observation == HostApplied {
			if receipt != "" {
				return "", ErrHostPlanUnavailable
			}
			receipt = event.EvidenceSHA256
		}
	}
	return receipt, nil
}

// PrepareDockerPlan persists a previously observed immutable plan, not a fresh
// authorization. Begin must already bind exactly plan.Operation(). An intent
// is flushed before exclusive file creation, then the file is synced, read back
// and receipted. It never overwrites a file or regenerates a committed plan.
func (j *HostJournal) PrepareDockerPlan(plan DockerHostPlan) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.planState(); err != nil {
		return err
	}
	if j.state.Pending.Sequence != 0 || plan.Operation() != j.state.Operation {
		return ErrHostJournalState
	}
	receipt, err := j.planReceipt()
	if err != nil {
		return err
	}
	if receipt != "" {
		return ErrHostJournalState
	}
	if _, err := j.root.Lstat(j.planName()); !errors.Is(err, os.ErrNotExist) {
		return ErrHostPlanUnavailable
	}
	if err := j.append(hostRecord{Kind: "intent", Effect: HostPrepareDockerPlan, Target: j.state.Operation.PlanSHA256}); err != nil {
		return err
	}
	sequence := j.state.Pending.Sequence
	digest, err := j.createDockerPlan(plan)
	if err != nil {
		return err
	}
	return j.append(hostRecord{Kind: "observe", Intent: sequence, Observation: HostApplied, Evidence: digest})
}

// ReadDockerPlan checks original host/file identity and its committed receipt.
// It returns a historical plan only. Revalidate must precede any live use.
func (j *HostJournal) ReadDockerPlan() (DockerHostPlan, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.planState(); err != nil {
		return DockerHostPlan{}, err
	}
	receipt, err := j.planReceipt()
	if err != nil || receipt == "" {
		return DockerHostPlan{}, ErrHostPlanUnavailable
	}
	plan, _, err := j.readDockerPlan(receipt)
	return plan, err
}

// ReconcileDockerPlan resolves only an interrupted private-file preparation.
// Absence is recorded without retry; a complete context-bound file is synced
// and accepted. Partial, replaced and mismatched files remain unresolved.
func (j *HostJournal) ReconcileDockerPlan() (HostObservation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.planState(); err != nil {
		return "", err
	}
	pending := j.state.Pending
	if pending.Effect != HostPrepareDockerPlan || pending.TargetSHA256 != j.state.Operation.PlanSHA256 {
		return "", ErrHostJournalState
	}
	receipt, err := j.planReceipt()
	if err != nil || receipt != "" {
		return "", ErrHostPlanUnavailable
	}
	_, err = j.root.Lstat(j.planName())
	observation, evidence := HostApplied, ""
	if errors.Is(err, os.ErrNotExist) {
		observation = HostNotApplied
		evidence = hostRecoveryDigest([]byte("release-docker-plan/absent/v1:" + j.state.Operation.PlanSHA256))
	} else {
		if err != nil {
			return "", ErrHostPlanUnavailable
		}
		_, evidence, err = j.readDockerPlan("")
		if err != nil {
			return "", err
		}
		// A crash may have preceded the original fsync. Re-establish durability
		// before writing the successful observation, without modifying bytes.
		if err := j.syncPrivateMaterial(j.planName(), maxHostPlanBytes); err != nil {
			return "", err
		}
		if _, _, err = j.readDockerPlan(evidence); err != nil {
			return "", err
		}
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: pending.Sequence, Observation: observation, Evidence: evidence}); err != nil {
		return "", err
	}
	return observation, nil
}

func (j *HostJournal) createDockerPlan(plan DockerHostPlan) (string, error) {
	name := j.planName()
	file, err := j.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", ErrHostPlanUnavailable
	}
	defer file.Close()
	if err := j.checkPrivateMaterialFile(file, name, maxHostPlanBytes); err != nil {
		return "", ErrHostPlanUnavailable
	}
	bound, err := j.planContext(file)
	if err != nil {
		return "", err
	}
	plaintext, err := json.Marshal(hostPlanPayload{Context: bound, Plan: plan.data})
	if err != nil || len(plaintext) > maxHostPlanBytes {
		return "", ErrHostPlanUnavailable
	}
	defer clear(plaintext)
	entropy, _ := json.Marshal(bound)
	sealed, err := protectHostPlan(plaintext, entropy)
	if err != nil {
		return "", ErrHostPlanUnavailable
	}
	defer clear(sealed)
	payload, _ := json.Marshal(hostRecoveryEnvelope{Version: 1, Protection: hostRecoveryProtection, Data: sealed})
	defer clear(payload)
	if len(payload) > maxHostPlanBytes {
		return "", ErrHostPlanUnavailable
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
	digest := hostRecoveryDigest(payload)
	if _, actual, err := j.readDockerPlan(digest); err != nil || actual != digest {
		j.poisoned = true
		return "", ErrHostJournalIO
	}
	return digest, nil
}

func (j *HostJournal) readDockerPlan(expectedSHA256 string) (DockerHostPlan, string, error) {
	var empty DockerHostPlan
	name := j.planName()
	info, err := j.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxHostPlanBytes {
		return empty, "", ErrHostPlanUnavailable
	}
	file, err := j.root.Open(name)
	if err != nil {
		return empty, "", ErrHostPlanUnavailable
	}
	defer file.Close()
	if err := j.checkPrivateMaterialFile(file, name, maxHostPlanBytes); err != nil {
		return empty, "", ErrHostPlanUnavailable
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return empty, "", ErrHostPlanUnavailable
	}
	bound, err := j.planContext(file)
	if err != nil {
		return empty, "", err
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxHostPlanBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxHostPlanBytes {
		return empty, "", ErrHostPlanUnavailable
	}
	defer clear(payload)
	digest := hostRecoveryDigest(payload)
	if expectedSHA256 != "" && digest != expectedSHA256 {
		return empty, "", ErrHostPlanUnavailable
	}
	var envelope hostRecoveryEnvelope
	if json.Unmarshal(payload, &envelope) != nil || envelope.Version != 1 || envelope.Protection != hostRecoveryProtection {
		return empty, "", ErrHostPlanUnavailable
	}
	defer clear(envelope.Data)
	canonical, _ := json.Marshal(envelope)
	defer clear(canonical)
	if !bytes.Equal(canonical, payload) {
		return empty, "", ErrHostPlanUnavailable
	}
	entropy, _ := json.Marshal(bound)
	plaintext, err := unprotectHostPlan(envelope.Data, entropy)
	if err != nil {
		return empty, "", ErrHostPlanUnavailable
	}
	defer clear(plaintext)
	var secret hostPlanPayload
	if json.Unmarshal(plaintext, &secret) != nil || secret.Context != bound {
		return empty, "", ErrHostPlanUnavailable
	}
	canonicalSecret, _ := json.Marshal(secret)
	defer clear(canonicalSecret)
	if !bytes.Equal(plaintext, canonicalSecret) {
		return empty, "", ErrHostPlanUnavailable
	}
	plan := DockerHostPlan{data: secret.Plan}
	if plan.Operation() != j.state.Operation {
		return empty, "", ErrHostPlanUnavailable
	}
	if err := j.checkPrivateMaterialFile(file, name, maxHostPlanBytes); err != nil {
		return empty, "", ErrHostPlanUnavailable
	}
	return plan, digest, nil
}
