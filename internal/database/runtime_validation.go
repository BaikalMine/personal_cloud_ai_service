package database

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/lib/pq"
)

const runtimeValidationGrantSQL = `SELECT EXISTS(
 SELECT 1 FROM pg_locks controller JOIN pg_locks validation
 ON validation.pid=controller.pid AND validation.database=controller.database
 WHERE controller.locktype='advisory' AND controller.classid=0
 AND controller.objid::bigint=$1 AND controller.objsubid=1
 AND controller.mode='ExclusiveLock' AND controller.granted
 AND validation.locktype='advisory' AND validation.classid=0
 AND validation.objid::bigint=$2 AND validation.objsubid=1
 AND validation.mode='ExclusiveLock' AND validation.granted
 AND controller.database=(SELECT oid FROM pg_database WHERE datname=current_database()))`

// RuntimeValidationRequest is a narrowly scoped connection credential. It does
// not authorize migration, ordinary traffic, background work or release commit.
type RuntimeValidationRequest struct {
	OperationID           string `json:"operation_id"`
	Epoch                 int64  `json:"epoch"`
	CandidateManifestHash string `json:"candidate_manifest_hash"`
	Token                 string `json:"-"`
}

func (r RuntimeValidationRequest) Validate() error {
	if !runtimeOperationID.MatchString(r.OperationID) || r.Epoch < 1 || !runtimeDigest.MatchString(r.CandidateManifestHash) || !runtimeDigest.MatchString(r.Token) {
		return ErrRuntimeReleaseConflict
	}
	return nil
}

func (r RuntimeValidationRequest) matches(state runtimeFenceRecord) bool {
	digest := sha256.Sum256([]byte(r.Token))
	return r.Validate() == nil && state.Active && state.OperationID == r.OperationID && state.Epoch == r.Epoch && state.CandidateManifestHash == r.CandidateManifestHash &&
		subtle.ConstantTimeCompare([]byte(state.TokenHash), []byte(hex.EncodeToString(digest[:]))) == 1
}

// OpenRuntimeValidation remains subject to the physical-connection runtime lock.
// The controller must first quiesce all writers, then explicitly open a validation
// window. Loss of that controller revokes new validation connections immediately.
func OpenRuntimeValidation(ctx context.Context, dsn string, request RuntimeValidationRequest) (*sql.DB, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	base, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, errors.New("runtime database configuration is invalid")
	}
	return waitForDatabase(ctx, sql.OpenDB(runtimeConnector{base: base, validation: &request}))
}

// CheckRuntimeValidation rechecks a live request boundary even when the SQL pool
// reuses a connection admitted before the controller revoked its grant.
func CheckRuntimeValidation(ctx context.Context, db *sql.DB, request RuntimeValidationRequest) error {
	if db == nil || request.Validate() != nil {
		return ErrRuntimeFenced
	}
	state, err := readRuntimeFence(ctx, db, false)
	if err != nil {
		return err
	}
	if !request.matches(state) {
		return ErrRuntimeFenced
	}
	var granted bool
	if err := db.QueryRowContext(ctx, runtimeValidationGrantSQL, runtimeControllerLockID, runtimeValidationLockID).Scan(&granted); err != nil {
		return err
	}
	if !granted {
		return ErrRuntimeFenced
	}
	return nil
}

// AllowValidationConnections yields exclusive database ownership to explicitly
// credentialed validation processes, while ordinary runtimes remain fenced.
// Stop those processes and call TryQuiesce again before any mutation or unfence.
func (r *RuntimeRelease) AllowValidationConnections(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.current(ctx)
	if err != nil {
		return err
	}
	if !state.Active {
		return ErrRuntimeReleaseClosed
	}
	granted, err := runtimeOwnsExclusive(ctx, r.conn, runtimeValidationLockID)
	if err != nil {
		return err
	}
	if granted && !r.quiescent {
		held, err := runtimeOwnsExclusive(ctx, r.conn, runtimeConnectionLockID)
		if err != nil {
			return err
		}
		if !held {
			return nil
		}
		r.quiescent = true
	}
	if err := r.checkExclusive(ctx); err != nil {
		return err
	}
	if !granted {
		if err := r.conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, runtimeValidationLockID).Scan(&granted); err != nil {
			return err
		}
		if !granted {
			return ErrRuntimeControllerBusy
		}
	}
	var unlocked bool
	err = r.conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, runtimeConnectionLockID).Scan(&unlocked)
	r.quiescent = false
	if err != nil {
		return err
	}
	if !unlocked {
		return ErrRuntimeNotQuiescent
	}
	return nil
}

func (r *RuntimeRelease) revokeValidation(ctx context.Context) error {
	_, err := r.conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1) WHERE EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND classid=0 AND objid::bigint=$1 AND objsubid=1 AND mode='ExclusiveLock' AND granted)`, runtimeValidationLockID)
	return err
}
