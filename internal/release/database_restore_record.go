package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const maxDatabaseRestoreRecordBytes = 64 << 10

func validDatabaseRestorePhase(phase string) bool {
	return phase == "prepared" || phase == "writer" || phase == "completed" || phase == "resolved"
}

// Each phase owns a separate immutable file and binds the previous file's
// exact protected bytes. No partial record is overwritten or adopted.
func (j *HostJournal) createDatabaseRestoreRecord(ctx context.Context, record databaseRestoreRecord) (string, error) {
	if !validHostHash(record.Binding.Target) || !validDatabaseRestorePhase(record.Phase) || ctx.Err() != nil {
		return "", ErrDatabaseRestore
	}
	name := databaseRestoreRecordName(record.Binding.Target, record.Phase)
	file, err := j.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", ErrDatabaseRestore
	}
	defer file.Close()
	if j.checkPrivateMaterialFile(file, name, maxDatabaseRestoreRecordBytes) != nil {
		return "", ErrDatabaseRestore
	}
	record.Context, err = j.planContext(file)
	if err != nil {
		return "", ErrDatabaseRestore
	}
	record.Context.Purpose = databaseRestoreScope + "/" + record.Phase
	plaintext, err := json.Marshal(record)
	if err != nil {
		return "", ErrDatabaseRestore
	}
	defer clear(plaintext)
	entropy, _ := json.Marshal(record.Context)
	sealed, err := protectHostPlan(plaintext, entropy)
	if err != nil {
		return "", ErrDatabaseRestore
	}
	defer clear(sealed)
	body, _ := json.Marshal(hostRecoveryEnvelope{Version: 1, Protection: hostRecoveryProtection, Data: sealed})
	defer clear(body)
	if len(body) > maxDatabaseRestoreRecordBytes {
		return "", ErrDatabaseRestore
	}
	n, err := file.Write(body)
	if err != nil || n != len(body) || file.Sync() != nil || syncHostDirectory(j.dir) != nil {
		return "", ErrDatabaseRestore
	}
	digest := hostRecoveryDigest(body)
	if _, _, err := j.readDatabaseRestoreChain(ctx, record.Binding.Target, record.Phase, digest); err != nil {
		return "", ErrDatabaseRestore
	}
	return digest, nil
}

func (j *HostJournal) readDatabaseRestoreRecord(ctx context.Context, target, phase, expected string) (databaseRestoreRecord, string, error) {
	var empty databaseRestoreRecord
	if !validHostHash(target) || !validDatabaseRestorePhase(phase) || ctx.Err() != nil || j.validate() != nil {
		return empty, "", ErrDatabaseRestore
	}
	name := databaseRestoreRecordName(target, phase)
	file, err := j.root.Open(name)
	if err != nil {
		return empty, "", ErrDatabaseRestore
	}
	defer file.Close()
	if j.checkPrivateMaterialFile(file, name, maxDatabaseRestoreRecordBytes) != nil {
		return empty, "", ErrDatabaseRestore
	}
	bound, err := j.planContext(file)
	if err != nil {
		return empty, "", ErrDatabaseRestore
	}
	bound.Purpose = databaseRestoreScope + "/" + phase
	body, err := io.ReadAll(io.LimitReader(file, maxDatabaseRestoreRecordBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxDatabaseRestoreRecordBytes {
		return empty, "", ErrDatabaseRestore
	}
	defer clear(body)
	digest := hostRecoveryDigest(body)
	if expected != "" && digest != expected {
		return empty, "", ErrDatabaseRestore
	}
	var envelope hostRecoveryEnvelope
	if json.Unmarshal(body, &envelope) != nil || envelope.Version != 1 || envelope.Protection != hostRecoveryProtection {
		return empty, "", ErrDatabaseRestore
	}
	defer clear(envelope.Data)
	canonical, _ := json.Marshal(envelope)
	defer clear(canonical)
	if !bytes.Equal(body, canonical) {
		return empty, "", ErrDatabaseRestore
	}
	entropy, _ := json.Marshal(bound)
	plaintext, err := unprotectHostPlan(envelope.Data, entropy)
	if err != nil {
		return empty, "", ErrDatabaseRestore
	}
	defer clear(plaintext)
	var record databaseRestoreRecord
	if json.Unmarshal(plaintext, &record) != nil || record.Context != bound || record.Binding.Target != target || record.Phase != phase || record.At.IsZero() {
		return empty, "", ErrDatabaseRestore
	}
	canonicalRecord, _ := json.Marshal(record)
	defer clear(canonicalRecord)
	if !bytes.Equal(plaintext, canonicalRecord) || j.checkPrivateMaterialFile(file, name, maxDatabaseRestoreRecordBytes) != nil {
		return empty, "", ErrDatabaseRestore
	}
	return record, digest, nil
}

func (j *HostJournal) readDatabaseRestoreChain(ctx context.Context, target, phase, expected string) (databaseRestoreRecord, string, error) {
	head, digest, err := j.readDatabaseRestoreLocalChain(ctx, target, phase, expected)
	if err != nil {
		return databaseRestoreRecord{}, "", err
	}
	// Walk predecessors iteratively. Every edge must point to the immediately
	// preceding journal attempt and its exact not_applied resolution receipt.
	current := head
	for current.Binding.PreviousAttempt != "" {
		phase, err := j.databaseRestoreRecoveryPhase(ctx, current.Binding.PreviousAttempt)
		if err != nil || phase != "resolved" {
			return databaseRestoreRecord{}, "", ErrDatabaseRestore
		}
		previous, _, err := j.readDatabaseRestoreLocalChain(ctx, current.Binding.PreviousAttempt, phase, current.Binding.PreviousReceipt)
		if err != nil || previous.Resolution != HostNotApplied || !sameRestoreDestination(head.Binding, previous.Binding) ||
			previous.Binding.Intent.Sequence >= current.Binding.Intent.Sequence || previous.At.After(current.At) {
			return databaseRestoreRecord{}, "", ErrDatabaseRestore
		}
		current = previous
	}
	return head, digest, nil
}

func (j *HostJournal) readDatabaseRestoreLocalChain(ctx context.Context, target, phase, expected string) (databaseRestoreRecord, string, error) {
	var empty databaseRestoreRecord
	head, digest, err := j.readDatabaseRestoreRecord(ctx, target, phase, expected)
	if err != nil {
		return empty, "", err
	}
	m, err := j.restoreMaterial(ctx, head.Binding.Epoch, head.Binding.ArchiveOptions, head.Binding.Options)
	if err != nil || !validHostHash(head.Binding.ToolIdentity) {
		return empty, "", ErrDatabaseRestore
	}
	m.binding, err = j.expectedRestoreBinding(m.binding, target, head.Binding.ToolIdentity)
	if err != nil || m.binding != head.Binding {
		return empty, "", ErrDatabaseRestore
	}
	if (phase == "completed" || phase == "resolved") && j.restoreCredentialsAbsent(target) != nil {
		return empty, "", ErrDatabaseRestore
	}
	record := head
	for {
		if record.Binding != head.Binding {
			return empty, "", ErrDatabaseRestore
		}
		if record.Phase != "resolved" && record.Resolution != "" {
			return empty, "", ErrDatabaseRestore
		}
		if record.Credential != nil && !restoreCredentialMatches(m, record.Credential) {
			return empty, "", ErrDatabaseRestore
		}
		next := ""
		switch record.Phase {
		case "prepared":
			if record.Previous != "" || record.Process != (databaseProcessIdentity{}) || record.Credential != nil || record.ResultOID != 0 || record.ObservationSHA256 != "" {
				return empty, "", ErrDatabaseRestore
			}
			return head, digest, nil
		case "writer":
			if !record.Process.valid() || record.ResultOID != 0 || record.ObservationSHA256 != "" {
				return empty, "", ErrDatabaseRestore
			}
			next = "prepared"
		case "completed":
			if !record.Process.valid() || record.ResultOID <= 0 || record.ResultOID == m.destination.BootstrapOID || !validHostHash(record.ObservationSHA256) {
				return empty, "", ErrDatabaseRestore
			}
			next = "writer"
		case "resolved":
			if !record.Process.valid() || record.Credential == nil || !validHostHash(record.ObservationSHA256) ||
				(record.Resolution != HostApplied && record.Resolution != HostNotApplied) || record.ResultOID <= 0 ||
				(record.ResultOID == m.destination.BootstrapOID) != (record.Resolution == HostNotApplied) {
				return empty, "", ErrDatabaseRestore
			}
			next = "writer"
		default:
			return empty, "", ErrDatabaseRestore
		}
		if !validHostHash(record.Previous) {
			return empty, "", ErrDatabaseRestore
		}
		previous, _, err := j.readDatabaseRestoreRecord(ctx, target, next, record.Previous)
		if err != nil || previous.At.After(record.At) || (next == "writer" && previous.Process != record.Process) {
			return empty, "", ErrDatabaseRestore
		}
		if next == "writer" && !sameRestoreCredential(previous.Credential, record.Credential) {
			return empty, "", ErrDatabaseRestore
		}
		record = previous
	}
}

func (j *HostJournal) restoreCredentialsAbsent(target string) error {
	password, absent := databaseRestorePasswordNames(target)
	for _, name := range []string{password, absent} {
		if _, err := j.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return ErrDatabaseRestore
		}
	}
	return nil
}

// A corrupt or conflicting terminal file must not fall back to an earlier
// phase. Only genuine absence permits live SQL resolution of the writer chain.
func (j *HostJournal) readDatabaseRestoreRecoveryHead(ctx context.Context, target, expected string) (databaseRestoreRecord, string, error) {
	phase, err := j.databaseRestoreRecoveryPhase(ctx, target)
	if err != nil {
		return databaseRestoreRecord{}, "", err
	}
	return j.readDatabaseRestoreChain(ctx, target, phase, expected)
}

func (j *HostJournal) databaseRestoreRecoveryPhase(ctx context.Context, target string) (string, error) {
	if !validHostHash(target) || ctx.Err() != nil || j.validate() != nil {
		return "", ErrDatabaseRestore
	}
	phase := "writer"
	for _, candidate := range []string{"completed", "resolved"} {
		_, err := j.root.Lstat(databaseRestoreRecordName(target, candidate))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || phase != "writer" {
			return "", ErrDatabaseRestore
		}
		phase = candidate
	}
	return phase, nil
}
