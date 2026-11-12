package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const dockerDatabasePurpose = "docker_gateway_database_connection_v1"

type dockerDatabasePayload struct {
	Context       hostPlanContext             `json:"context"`
	Configuration dockerDatabaseConfiguration `json:"configuration"`
}

func (j *HostJournal) databaseConfigName() string {
	return ".docker-database-" + j.state.Operation.PlanSHA256 + ".json"
}

func (j *HostJournal) databaseConfigReceipt() (string, error) {
	intents := make(map[uint64]bool)
	receipt := ""
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == HostPrepareDockerDatabase && event.TargetSHA256 == j.state.Operation.PlanSHA256 {
			intents[event.Sequence] = true
		}
		if event.Kind == "observe" && intents[event.IntentSequence] && event.Observation == HostApplied {
			if receipt != "" {
				return "", ErrDockerDatabaseConfiguration
			}
			receipt = event.EvidenceSHA256
		}
	}
	return receipt, nil
}

func (j *HostJournal) committedDockerPlan() (DockerHostPlan, error) {
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

// PrepareDockerDatabaseConfiguration stores an explicit, physically verified
// connection before any fence attempt. It does not infer login information from
// Docker Env, create a database, migrate a schema or grant privileges. The URL
// may contain a password and must never be passed through a public report.
func (j *HostJournal) PrepareDockerDatabaseConfiguration(ctx context.Context, connectionURL string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	plan, err := j.committedDockerPlan()
	if err != nil {
		return err
	}
	if j.state.Pending.Sequence != 0 {
		return ErrHostJournalState
	}
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == HostFence {
			return ErrHostJournalState
		}
	}
	receipt, err := j.databaseConfigReceipt()
	if err != nil {
		return err
	}
	if receipt != "" {
		return ErrHostJournalState
	}
	if _, err := j.root.Lstat(j.databaseConfigName()); !errors.Is(err, os.ErrNotExist) {
		return ErrDockerDatabaseConfiguration
	}
	configuration, err := observeDockerDatabaseConfiguration(connectionURL)
	if err != nil {
		return err
	}
	db, err := openDockerDatabase(ctx, configuration, plan.data.Observed.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := j.append(hostRecord{Kind: "intent", Effect: HostPrepareDockerDatabase, Target: j.state.Operation.PlanSHA256}); err != nil {
		return err
	}
	sequence := j.state.Pending.Sequence
	digest, err := j.createDockerDatabaseConfiguration(configuration)
	if err != nil {
		return err
	}
	return j.append(hostRecord{Kind: "observe", Intent: sequence, Observation: HostApplied, Evidence: digest})
}

// ReconcileDockerDatabaseConfiguration only recovers an interrupted private
// file preparation. A complete file is revalidated against the original DB and
// synced before acknowledgement. It neither repairs nor replaces configuration.
func (j *HostJournal) ReconcileDockerDatabaseConfiguration(ctx context.Context) (HostObservation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	plan, err := j.committedDockerPlan()
	if err != nil {
		return "", err
	}
	pending := j.state.Pending
	if pending.Effect != HostPrepareDockerDatabase || pending.TargetSHA256 != j.state.Operation.PlanSHA256 {
		return "", ErrHostJournalState
	}
	receipt, err := j.databaseConfigReceipt()
	if err != nil || receipt != "" {
		return "", ErrDockerDatabaseConfiguration
	}
	_, err = j.root.Lstat(j.databaseConfigName())
	observation, evidence := HostApplied, ""
	if errors.Is(err, os.ErrNotExist) {
		observation = HostNotApplied
		evidence = hostRecoveryDigest([]byte("docker-database/absent/v1:" + j.state.Operation.PlanSHA256))
	} else {
		if err != nil {
			return "", ErrDockerDatabaseConfiguration
		}
		configuration, digest, err := j.readDockerDatabaseConfiguration("")
		if err != nil {
			return "", err
		}
		db, err := openDockerDatabase(ctx, configuration, plan.data.Observed.Database)
		if err != nil {
			return "", err
		}
		db.Close()
		if err := j.syncPrivateMaterial(j.databaseConfigName(), maxDockerDatabaseConfigBytes); err != nil {
			return "", err
		}
		if _, _, err := j.readDockerDatabaseConfiguration(digest); err != nil {
			return "", err
		}
		evidence = digest
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: pending.Sequence, Observation: observation, Evidence: evidence}); err != nil {
		return "", err
	}
	return observation, nil
}

func (j *HostJournal) createDockerDatabaseConfiguration(configuration dockerDatabaseConfiguration) (string, error) {
	name := j.databaseConfigName()
	file, err := j.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", ErrDockerDatabaseConfiguration
	}
	defer file.Close()
	if err := j.checkPrivateMaterialFile(file, name, maxDockerDatabaseConfigBytes); err != nil {
		return "", ErrDockerDatabaseConfiguration
	}
	bound, err := j.planContext(file)
	if err != nil {
		return "", ErrDockerDatabaseConfiguration
	}
	bound.Purpose = dockerDatabasePurpose
	plaintext, err := json.Marshal(dockerDatabasePayload{Context: bound, Configuration: configuration})
	if err != nil || len(plaintext) > maxDockerDatabaseConfigBytes {
		return "", ErrDockerDatabaseConfiguration
	}
	defer clear(plaintext)
	entropy, _ := json.Marshal(bound)
	sealed, err := protectHostPlan(plaintext, entropy)
	if err != nil {
		return "", ErrDockerDatabaseConfiguration
	}
	defer clear(sealed)
	payload, _ := json.Marshal(hostRecoveryEnvelope{Version: 1, Protection: hostRecoveryProtection, Data: sealed})
	defer clear(payload)
	if len(payload) > maxDockerDatabaseConfigBytes {
		return "", ErrDockerDatabaseConfiguration
	}
	n, err := file.Write(payload)
	if err != nil || n != len(payload) {
		j.poisoned = true
		return "", ErrHostJournalIO
	}
	if err := errors.Join(file.Sync(), syncHostDirectory(j.dir)); err != nil {
		j.poisoned = true
		return "", ErrHostJournalIO
	}
	digest := hostRecoveryDigest(payload)
	if _, _, err := j.readDockerDatabaseConfiguration(digest); err != nil {
		j.poisoned = true
		return "", ErrHostJournalIO
	}
	return digest, nil
}

func (j *HostJournal) readDockerDatabaseConfiguration(expected string) (dockerDatabaseConfiguration, string, error) {
	var empty dockerDatabaseConfiguration
	name := j.databaseConfigName()
	info, err := j.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxDockerDatabaseConfigBytes {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	file, err := j.root.Open(name)
	if err != nil {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	defer file.Close()
	if j.checkPrivateMaterialFile(file, name, maxDockerDatabaseConfigBytes) != nil {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	bound, err := j.planContext(file)
	if err != nil {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	bound.Purpose = dockerDatabasePurpose
	payload, err := io.ReadAll(io.LimitReader(file, maxDockerDatabaseConfigBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxDockerDatabaseConfigBytes {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	defer clear(payload)
	digest := hostRecoveryDigest(payload)
	if expected != "" && digest != expected {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	var envelope hostRecoveryEnvelope
	if json.Unmarshal(payload, &envelope) != nil || envelope.Version != 1 || envelope.Protection != hostRecoveryProtection {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	defer clear(envelope.Data)
	canonical, _ := json.Marshal(envelope)
	defer clear(canonical)
	if !bytes.Equal(payload, canonical) {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	entropy, _ := json.Marshal(bound)
	plaintext, err := unprotectHostPlan(envelope.Data, entropy)
	if err != nil {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	defer clear(plaintext)
	var secret dockerDatabasePayload
	if json.Unmarshal(plaintext, &secret) != nil || secret.Context != bound {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	canonicalSecret, _ := json.Marshal(secret)
	defer clear(canonicalSecret)
	if !bytes.Equal(plaintext, canonicalSecret) || secret.Configuration.validate() != nil || j.checkPrivateMaterialFile(file, name, maxDockerDatabaseConfigBytes) != nil {
		return empty, "", ErrDockerDatabaseConfiguration
	}
	return secret.Configuration, digest, nil
}
