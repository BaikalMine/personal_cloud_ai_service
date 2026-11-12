package database

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"math"
	"sync"
)

var (
	ErrRuntimeReleaseConflict      = errors.New("runtime release identity or epoch conflicts")
	ErrRuntimeReleaseClosed        = errors.New("runtime release has already been closed")
	ErrRuntimeControllerBusy       = errors.New("another runtime release controller is connected")
	ErrRuntimeNotQuiescent         = errors.New("runtime database connections have not been quiesced")
	ErrRuntimeControllerConnection = errors.New("release control requires an unleased dedicated database connection")
)

type RuntimeReleaseRequest struct {
	OperationID           string
	Token                 string `json:"-"`
	ExpectedEpoch         int64
	PreviousManifestHash  string
	CandidateManifestHash string
}

// RuntimeRelease is a low-level database fence, not an installer. The caller
// must verify packages, stop the declared writers and validate workloads. Close
// releases session locks but intentionally NEVER clears the persistent fence.
type RuntimeRelease struct {
	mu        sync.Mutex
	conn      *sql.Conn
	record    runtimeFenceRecord
	quiescent bool
}

type runtimeFenceQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRuntimeFence(ctx context.Context, q runtimeFenceQuerier, forUpdate bool) (runtimeFenceRecord, error) {
	query := runtimeFenceReadSQL
	if forUpdate {
		query += " FOR UPDATE OF c"
	}
	var data []byte
	if err := q.QueryRowContext(ctx, query).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrRuntimeFenceInvalid
		}
		return runtimeFenceRecord{}, err
	}
	return parseRuntimeFence(data)
}

func ReadRuntimeFence(ctx context.Context, db *sql.DB) (RuntimeFenceState, error) {
	if db == nil {
		return RuntimeFenceState{}, ErrRuntimeFenceInvalid
	}
	r, err := readRuntimeFence(ctx, db, false)
	return r.RuntimeFenceState, err
}

func discardRuntimeController(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

func runtimeOwnsExclusive(ctx context.Context, conn *sql.Conn, lockID int64) (bool, error) {
	var held bool
	err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND classid=0 AND objid::bigint=$1 AND objsubid=1 AND mode='ExclusiveLock' AND granted)`, lockID).Scan(&held)
	return held, err
}

// BeginRuntimeRelease atomically closes NEW runtime connections. Existing
// pooled connections still count as writers until TryQuiesce succeeds.
// Tokens are caller-generated 32-byte random values encoded as lowercase hex.
func BeginRuntimeRelease(ctx context.Context, db *sql.DB, request RuntimeReleaseRequest) (*RuntimeRelease, error) {
	return beginRuntimeRelease(ctx, db, request, nil, false)
}

// BeginRuntimeReleaseForDatabase checks the planned identity on the selected
// physical controller session BEFORE acquiring locks or changing the fence.
// It never resolves a database name or substitutes another pooled connection.
func BeginRuntimeReleaseForDatabase(ctx context.Context, db *sql.DB, request RuntimeReleaseRequest, expected RuntimeDatabaseIdentity) (*RuntimeRelease, error) {
	return beginRuntimeRelease(ctx, db, request, &expected, false)
}

// ResumeRuntimeReleaseForDatabase reacquires only an already active matching
// fence. It must not turn an uncertain, never-applied fence intent into a write.
func ResumeRuntimeReleaseForDatabase(ctx context.Context, db *sql.DB, request RuntimeReleaseRequest, expected RuntimeDatabaseIdentity) (*RuntimeRelease, error) {
	return beginRuntimeRelease(ctx, db, request, &expected, true)
}

func beginRuntimeRelease(ctx context.Context, db *sql.DB, request RuntimeReleaseRequest, expected *RuntimeDatabaseIdentity, resumeOnly bool) (*RuntimeRelease, error) {
	if db == nil || !runtimeOperationID.MatchString(request.OperationID) || !runtimeDigest.MatchString(request.Token) || !runtimeDigest.MatchString(request.PreviousManifestHash) || !runtimeDigest.MatchString(request.CandidateManifestHash) || request.ExpectedEpoch < 0 || request.ExpectedEpoch == math.MaxInt64 {
		return nil, ErrRuntimeReleaseConflict
	}
	digest := sha256.Sum256([]byte(request.Token))
	tokenHash := hex.EncodeToString(digest[:])
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			discardRuntimeController(conn)
		}
	}()
	if expected != nil {
		actual, _, err := readRuntimeDatabaseIdentity(ctx, conn)
		if err != nil || actual != *expected {
			return nil, ErrRuntimeReleaseConflict
		}
	}
	var leased bool
	if err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND classid=0 AND objid::bigint=$1 AND objsubid=1 AND mode='ShareLock' AND granted)`, runtimeConnectionLockID).Scan(&leased); err != nil {
		return nil, err
	}
	if leased {
		return nil, ErrRuntimeControllerConnection
	}
	var locked bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, runtimeControllerLockID).Scan(&locked); err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrRuntimeControllerBusy
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := readRuntimeFence(ctx, tx, true)
	if err != nil {
		return nil, err
	}
	same := r.Epoch == request.ExpectedEpoch+1 && r.OperationID == request.OperationID && r.PreviousManifestHash == request.PreviousManifestHash && r.CandidateManifestHash == request.CandidateManifestHash && subtle.ConstantTimeCompare([]byte(r.TokenHash), []byte(tokenHash)) == 1
	if same && !r.Active {
		return nil, ErrRuntimeReleaseClosed
	}
	if r.Active {
		if !same {
			return nil, ErrRuntimeReleaseConflict
		}
	} else {
		if resumeOnly {
			return nil, ErrRuntimeReleaseConflict
		}
		if r.Epoch != request.ExpectedEpoch || r.OperationID == request.OperationID {
			return nil, ErrRuntimeReleaseConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE public.release_runtime_control SET epoch=epoch+1,active=true,operation_id=$1,token_hash=$2,previous_manifest_hash=$3,candidate_manifest_hash=$4,requested_at=now(),finished_at=NULL WHERE singleton=1`, request.OperationID, tokenHash, request.PreviousManifestHash, request.CandidateManifestHash); err != nil {
			return nil, err
		}
		r, err = readRuntimeFence(ctx, tx, false)
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	keep = true
	return &RuntimeRelease{conn: conn, record: r}, nil
}

// WithActiveFence retains controller ownership while a trusted host action
// stops a declared runtime. Existing runtime connections need not be quiescent
// yet; no SQL connection is lent to the callback. The callback must not invoke
// this controller recursively. An error may follow an already applied action
// and must never be interpreted as permission to retry it.
func (r *RuntimeRelease) WithActiveFence(ctx context.Context, request RuntimeReleaseRequest, expected RuntimeDatabaseIdentity, work func(RuntimeFenceState) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if work == nil || request.ExpectedEpoch < 0 || request.ExpectedEpoch == math.MaxInt64 || !runtimeDigest.MatchString(request.Token) {
		return ErrRuntimeReleaseConflict
	}
	tokenHash := sha256.Sum256([]byte(request.Token))
	expectedTokenHash := hex.EncodeToString(tokenHash[:])
	check := func() (RuntimeFenceState, error) {
		state, err := r.current(ctx)
		if err != nil {
			return RuntimeFenceState{}, err
		}
		if !state.Active || state.Epoch != request.ExpectedEpoch+1 || state.OperationID != request.OperationID ||
			state.PreviousManifestHash != request.PreviousManifestHash || state.CandidateManifestHash != request.CandidateManifestHash ||
			subtle.ConstantTimeCompare([]byte(state.TokenHash), []byte(expectedTokenHash)) != 1 {
			return RuntimeFenceState{}, ErrRuntimeReleaseConflict
		}
		actual, _, err := readRuntimeDatabaseIdentity(ctx, r.conn)
		if err != nil || actual != expected {
			return RuntimeFenceState{}, ErrRuntimeReleaseConflict
		}
		return state.RuntimeFenceState, nil
	}
	state, err := check()
	if err != nil {
		return err
	}
	if err := work(state); err != nil {
		return err
	}
	_, err = check()
	return err
}

func (r *RuntimeRelease) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn != nil {
		discardRuntimeController(r.conn)
		r.conn = nil
		r.quiescent = false
	}
}

func (r *RuntimeRelease) current(ctx context.Context) (runtimeFenceRecord, error) {
	if r.conn == nil {
		return runtimeFenceRecord{}, ErrRuntimeReleaseClosed
	}
	held, err := runtimeOwnsExclusive(ctx, r.conn, runtimeControllerLockID)
	if err != nil {
		return runtimeFenceRecord{}, err
	}
	if !held {
		return runtimeFenceRecord{}, ErrRuntimeControllerConnection
	}
	state, err := readRuntimeFence(ctx, r.conn, false)
	if err != nil {
		return state, err
	}
	if state.Epoch != r.record.Epoch || state.OperationID != r.record.OperationID || state.PreviousManifestHash != r.record.PreviousManifestHash || state.CandidateManifestHash != r.record.CandidateManifestHash || subtle.ConstantTimeCompare([]byte(state.TokenHash), []byte(r.record.TokenHash)) != 1 {
		return state, ErrRuntimeReleaseConflict
	}
	return state, nil
}

func (r *RuntimeRelease) TryQuiesce(ctx context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.current(ctx)
	if err != nil {
		return false, err
	}
	if !state.Active {
		return false, ErrRuntimeReleaseClosed
	}
	if err := r.revokeValidation(ctx); err != nil {
		return false, err
	}
	if r.quiescent {
		err := r.checkExclusive(ctx)
		return err == nil, err
	}
	// A cancelled lock query can have reached PostgreSQL. Do not stack a second
	// session lock when retrying an ambiguous acquisition on the same connection.
	held, err := runtimeOwnsExclusive(ctx, r.conn, runtimeConnectionLockID)
	if err != nil {
		return false, err
	}
	if held {
		r.quiescent = true
		return true, nil
	}
	var acquired bool
	if err = r.conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, runtimeConnectionLockID).Scan(&acquired); err != nil {
		return false, err
	}
	r.quiescent = acquired
	return acquired, nil
}

func (r *RuntimeRelease) checkExclusive(ctx context.Context) error {
	if !r.quiescent {
		return ErrRuntimeNotQuiescent
	}
	held, err := runtimeOwnsExclusive(ctx, r.conn, runtimeConnectionLockID)
	if err != nil {
		return err
	}
	if !held {
		r.quiescent = false
		return ErrRuntimeNotQuiescent
	}
	return nil
}

// WithQuiescentConnection keeps database work on the exact session that owns
// the controller and exclusion locks; connection loss cannot transparently
// replace it with an unguarded pool connection.
func (r *RuntimeRelease) WithQuiescentConnection(ctx context.Context, work func(*sql.Conn) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.current(ctx)
	if err != nil {
		return err
	}
	if !state.Active {
		return ErrRuntimeReleaseClosed
	}
	if err = r.checkExclusive(ctx); err != nil {
		return err
	}
	if work == nil {
		return ErrRuntimeControllerConnection
	}
	if err = work(r.conn); err != nil {
		return err
	}
	state, err = r.current(ctx)
	if err != nil {
		return err
	}
	if !state.Active {
		return ErrRuntimeReleaseClosed
	}
	return r.checkExclusive(ctx)
}

// Unfence is explicit and owner-bound. It is not called on failure or Close.
// The caller must establish its own release/rollback acceptance before using it.
func (r *RuntimeRelease) Unfence(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.current(ctx)
	if err != nil {
		return err
	}
	if err = r.checkExclusive(ctx); err != nil {
		return err
	}
	if !state.Active {
		return nil
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE public.release_runtime_control SET active=false,finished_at=now() WHERE singleton=1 AND epoch=$1 AND active AND operation_id=$2 AND token_hash=$3 AND previous_manifest_hash=$4 AND candidate_manifest_hash=$5`, r.record.Epoch, r.record.OperationID, r.record.TokenHash, r.record.PreviousManifestHash, r.record.CandidateManifestHash)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrRuntimeReleaseConflict
	}
	return tx.Commit()
}
