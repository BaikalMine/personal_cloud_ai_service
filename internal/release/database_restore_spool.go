package release

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// The caller holds j.mu and has verified the archive's committed receipt.
// Hash the entire copy before dispatch: streaming a mutable pathname directly
// into pg_restore could execute SQL before detecting changed trailing bytes.
func (j *HostJournal) databaseRestoreSpool(ctx context.Context, payload databaseArchivePayload) (*os.File, error) {
	if !validHostHash(payload.Target) || !validHostHash(payload.Archive.SHA256) || !validHostHash(payload.Archive.Identity) ||
		payload.Archive.Bytes < 5 || payload.Archive.Bytes > payload.Options.MaxBytes || !payload.Options.valid() {
		return nil, ErrDatabaseRestore
	}
	name, _, _, _ := databaseArchiveNames(payload.Target)
	original, err := j.root.Open(name)
	if err != nil {
		return nil, ErrDatabaseRestore
	}
	defer original.Close()
	if j.checkPrivateMaterialFile(original, name, payload.Options.MaxBytes) != nil {
		return nil, ErrDatabaseRestore
	}
	id, err := hostFileIdentity(original)
	if err != nil || id != payload.Archive.Identity {
		return nil, ErrDatabaseRestore
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, ErrDatabaseRestore
	}
	spool, err := createDatabaseRestoreSpool(j, ".restore-spool-"+hex.EncodeToString(nonce[:]))
	if err != nil {
		return nil, ErrDatabaseRestore
	}
	keep := false
	defer func() {
		if !keep {
			spool.Close()
		}
	}()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(spool, hash), io.LimitReader(archiveContextReader{ctx, original}, payload.Archive.Bytes+1))
	if err != nil || size != payload.Archive.Bytes || hex.EncodeToString(hash.Sum(nil)) != payload.Archive.SHA256 ||
		j.checkPrivateMaterialFile(original, name, payload.Options.MaxBytes) != nil || ctx.Err() != nil {
		return nil, ErrDatabaseRestore
	}
	var magic [5]byte
	if _, err := spool.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "PGDMP" {
		return nil, ErrDatabaseRestore
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, ErrDatabaseRestore
	}
	keep = true
	return spool, nil
}
