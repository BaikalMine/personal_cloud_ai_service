package release

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
)

const maxDatabaseTargetMetadataBytes = 128 << 10

func (j *HostJournal) createDatabaseTargetMetadata(ctx context.Context, payload databaseTargetPayload) (string, error) {
	name := databaseTargetMetadataName(payload.Target)
	file, err := j.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", ErrDatabaseTarget
	}
	defer file.Close()
	if j.checkPrivateMaterialFile(file, name, maxDatabaseTargetMetadataBytes) != nil {
		return "", ErrDatabaseTarget
	}
	payload.Context, err = j.planContext(file)
	if err != nil {
		return "", ErrDatabaseTarget
	}
	payload.Context.Purpose = databaseTargetScope
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return "", ErrDatabaseTarget
	}
	defer clear(plaintext)
	entropy, _ := json.Marshal(payload.Context)
	sealed, err := protectHostPlan(plaintext, entropy)
	if err != nil {
		return "", ErrDatabaseTarget
	}
	defer clear(sealed)
	body, _ := json.Marshal(hostRecoveryEnvelope{Version: 1, Protection: hostRecoveryProtection, Data: sealed})
	defer clear(body)
	if len(body) > maxDatabaseTargetMetadataBytes {
		return "", ErrDatabaseTarget
	}
	n, err := file.Write(body)
	if err != nil || n != len(body) || file.Sync() != nil || syncHostDirectory(j.dir) != nil {
		return "", ErrDatabaseTarget
	}
	digest := hostRecoveryDigest(body)
	if _, _, err := j.readDatabaseTargetMetadata(ctx, payload.Target, digest); err != nil {
		return "", ErrDatabaseTarget
	}
	return digest, nil
}

func (j *HostJournal) readDatabaseTargetMetadata(ctx context.Context, target, expected string) (databaseTargetPayload, string, error) {
	var empty databaseTargetPayload
	if !validHostHash(target) {
		return empty, "", ErrDatabaseTarget
	}
	name := databaseTargetMetadataName(target)
	file, err := j.root.Open(name)
	if err != nil {
		return empty, "", ErrDatabaseTarget
	}
	defer file.Close()
	if j.checkPrivateMaterialFile(file, name, maxDatabaseTargetMetadataBytes) != nil {
		return empty, "", ErrDatabaseTarget
	}
	bound, err := j.planContext(file)
	if err != nil {
		return empty, "", ErrDatabaseTarget
	}
	bound.Purpose = databaseTargetScope
	body, err := io.ReadAll(io.LimitReader(file, maxDatabaseTargetMetadataBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxDatabaseTargetMetadataBytes {
		return empty, "", ErrDatabaseTarget
	}
	defer clear(body)
	digest := hostRecoveryDigest(body)
	if expected != "" && digest != expected {
		return empty, "", ErrDatabaseTarget
	}
	var envelope hostRecoveryEnvelope
	if json.Unmarshal(body, &envelope) != nil || envelope.Version != 1 || envelope.Protection != hostRecoveryProtection {
		return empty, "", ErrDatabaseTarget
	}
	defer clear(envelope.Data)
	canonical, _ := json.Marshal(envelope)
	defer clear(canonical)
	if !bytes.Equal(body, canonical) {
		return empty, "", ErrDatabaseTarget
	}
	entropy, _ := json.Marshal(bound)
	plaintext, err := unprotectHostPlan(envelope.Data, entropy)
	if err != nil {
		return empty, "", ErrDatabaseTarget
	}
	defer clear(plaintext)
	var payload databaseTargetPayload
	if json.Unmarshal(plaintext, &payload) != nil || payload.Context != bound || payload.Target != target ||
		!validHostHash(payload.ArchiveTarget) || !validHostHash(payload.ArchiveReceipt) || !validHostHash(payload.ConfigurationReceipt) ||
		payload.Epoch < 0 || payload.BootstrapOID <= 0 || payload.CompletedAt.IsZero() {
		return empty, "", ErrDatabaseTarget
	}
	canonicalPayload, _ := json.Marshal(payload)
	defer clear(canonicalPayload)
	if !bytes.Equal(plaintext, canonicalPayload) {
		return empty, "", ErrDatabaseTarget
	}
	plan, _, source, configReceipt, err := j.archiveMaterial(payload.Epoch)
	if err != nil || configReceipt != payload.ConfigurationReceipt ||
		databaseTargetHash(plan, payload.Epoch, payload.ArchiveTarget, payload.ArchiveReceipt) != target {
		return empty, "", ErrDatabaseTarget
	}
	archiveReceipt, err := j.databaseArchiveReceipt(payload.ArchiveTarget)
	if err != nil || archiveReceipt != payload.ArchiveReceipt {
		return empty, "", ErrDatabaseTarget
	}
	archive, _, err := j.readDatabaseArchiveMetadata(ctx, payload.ArchiveTarget, archiveReceipt)
	if err != nil || archive.DatabaseProperties == nil || archive.Epoch != payload.Epoch {
		return empty, "", ErrDatabaseTarget
	}
	configuration, err := targetDatabaseConfiguration(source, target)
	a, _ := json.Marshal(configuration)
	b, _ := json.Marshal(payload.Configuration)
	defer clear(a)
	defer clear(b)
	if err != nil || !bytes.Equal(a, b) {
		return empty, "", ErrDatabaseTarget
	}
	identity := archive.Database
	identity.DatabaseOID, identity.DatabaseName = payload.Database.DatabaseOID, databaseTargetName(target)
	if payload.Database != identity || payload.Database.DatabaseOID <= 0 || payload.Database.DatabaseOID == archive.Database.DatabaseOID {
		return empty, "", ErrDatabaseTarget
	}
	if j.checkPrivateMaterialFile(file, name, maxDatabaseTargetMetadataBytes) != nil {
		return empty, "", ErrDatabaseTarget
	}
	return payload, digest, nil
}

func (j *HostJournal) databaseTargetReceipt(target string) (string, error) {
	intents := make(map[uint64]bool)
	receipt := ""
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == HostCreateRestoreDatabase && event.TargetSHA256 == target {
			intents[event.Sequence] = true
		}
		if event.Kind == "observe" && intents[event.IntentSequence] && event.Observation == HostApplied {
			if receipt != "" {
				return "", ErrDatabaseTarget
			}
			receipt = event.EvidenceSHA256
		}
	}
	return receipt, nil
}

// ReadDockerRestoreDatabase checks historical creation evidence, not live
// target state or permission to restore, switch configuration or activate.
func (j *HostJournal) ReadDockerRestoreDatabase(ctx context.Context, epoch int64, options DatabaseArchiveOptions) (DatabaseTargetReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	plan, _, _, archive, _, receipt, err := j.targetMaterial(ctx, epoch, options)
	if err != nil {
		return DatabaseTargetReceipt{}, err
	}
	target := databaseTargetHash(plan, epoch, archive.Target, receipt)
	digest, err := j.databaseTargetReceipt(target)
	if err != nil || digest == "" {
		return DatabaseTargetReceipt{}, ErrDatabaseTarget
	}
	payload, _, err := j.readDatabaseTargetMetadata(ctx, target, digest)
	if err != nil {
		return DatabaseTargetReceipt{}, err
	}
	return targetReceipt(payload), nil
}
