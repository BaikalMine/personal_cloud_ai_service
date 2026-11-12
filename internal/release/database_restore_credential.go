package release

import (
	"context"
	"errors"
	"os"
)

func restoreCredentialMatches(m databaseRestoreMaterial, proof *archiveFileProof) bool {
	if proof == nil || proof.Identity == "" || !validHostHash(proof.SHA256) || proof.Bytes <= 0 || proof.Bytes > maxDockerDatabaseURLBytes*2 {
		return false
	}
	password, err := databaseArchivePassword(m.destination.Configuration)
	if err != nil {
		return false
	}
	defer clear(password)
	return proof.Bytes == int64(len(password)) && proof.SHA256 == hostRecoveryDigest(password)
}

func sameRestoreCredential(a, b *archiveFileProof) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// CleanupDockerDatabaseRestorePassword removes only the original temporary
// password of a pending restore whose recorded writer has demonstrably exited.
// It does not connect to SQL, signal a process, acknowledge the restore, remove
// a fence or authorize retry. Old records without a credential proof stay put.
func (j *HostJournal) CleanupDockerDatabaseRestorePassword(ctx context.Context, epoch int64,
	archiveOptions DatabaseArchiveOptions, options DatabaseRestoreOptions) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	m, err := j.latestRestoreMaterial(ctx, epoch, archiveOptions, options)
	if err != nil {
		return ErrDatabaseRestore
	}
	pending := j.state.Pending
	if pending.Sequence == 0 || pending.Effect != HostRestoreDatabase || pending.TargetSHA256 != m.binding.Target {
		return ErrHostJournalState
	}
	writer, digest, err := j.readDatabaseRestoreChain(ctx, m.binding.Target, "writer", "")
	if err != nil || writer.Binding.Intent != pending || !restoreCredentialMatches(m, writer.Credential) {
		return ErrDatabaseRestore
	}
	if exited, err := databaseProcessExited(writer.Process); err != nil || !exited {
		return ErrDatabaseRestore
	}
	passwordName, absentName := databaseRestorePasswordNames(m.binding.Target)
	if _, err := j.root.Lstat(absentName); !errors.Is(err, os.ErrNotExist) {
		return ErrDatabaseRestore
	}
	// Re-read the original protected chain immediately before the effect. The
	// pending restore intent remains unresolved, including after a partial cleanup.
	if _, _, err := j.readDatabaseRestoreChain(ctx, m.binding.Target, "writer", digest); err != nil || ctx.Err() != nil {
		return ErrDatabaseRestore
	}
	if j.removeArchivePassword(passwordName, *writer.Credential) != nil || syncHostDirectory(j.dir) != nil {
		return ErrDatabaseRestore
	}
	if _, err := j.root.Lstat(passwordName); !errors.Is(err, os.ErrNotExist) {
		return ErrDatabaseRestore
	}
	return nil
}
