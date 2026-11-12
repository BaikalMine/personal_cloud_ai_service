package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"sync"
)

var ErrRuntimeBindingUnavailable = errors.New("runtime database binding is unavailable")

// RuntimeBindingRequest identifies a short-lived probe but does not contain its
// two random lock values. Knowing this request is insufficient to make a proof.
type RuntimeBindingRequest struct {
	BackendPID int64 `json:"backend_pid"`
	ClassID    int64 `json:"class_id"`
}

func (r RuntimeBindingRequest) Validate() error {
	if r.BackendPID < 1 || r.BackendPID > math.MaxInt32 || r.ClassID < 1 || r.ClassID > math.MaxUint32 {
		return ErrRuntimeBindingUnavailable
	}
	return nil
}

// RuntimeBindingProbe owns a dedicated physical session until Close. It is only
// a database identity probe: it does not fence application work or authorize an
// installation. No application table or schema is modified.
type RuntimeBindingProbe struct {
	mu      sync.Mutex
	conn    *sql.Conn
	request RuntimeBindingRequest
	proof   string
}

// RuntimeDatabaseIdentity is read on the challenge's original physical session.
// SystemIdentifier requires permission to execute pg_control_system(). A clone
// may preserve it; this is database binding evidence, not a restore authorization.
type RuntimeDatabaseIdentity struct {
	SystemIdentifier    string `json:"system_identifier"`
	DatabaseOID         int64  `json:"database_oid"`
	DatabaseName        string `json:"database_name"`
	ServerAddress       string `json:"server_address"`
	ServerPort          int    `json:"server_port"`
	PostmasterStartedAt string `json:"postmaster_started_at"`
}

func (p *RuntimeBindingProbe) DatabaseIdentity(ctx context.Context) (RuntimeDatabaseIdentity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	identity, pid, err := readRuntimeDatabaseIdentity(ctx, p.conn)
	if err != nil || pid != p.request.BackendPID {
		return RuntimeDatabaseIdentity{}, ErrRuntimeBindingUnavailable
	}
	return identity, nil
}

func readRuntimeDatabaseIdentity(ctx context.Context, conn *sql.Conn) (RuntimeDatabaseIdentity, int64, error) {
	var identity RuntimeDatabaseIdentity
	var pid int64
	if conn == nil {
		return identity, 0, ErrRuntimeBindingUnavailable
	}
	err := conn.QueryRowContext(ctx, `SELECT pg_catalog.pg_backend_pid(), c.system_identifier::text,
	 d.oid::bigint, d.datname, COALESCE(pg_catalog.inet_server_addr()::text, ''),
	 COALESCE(pg_catalog.inet_server_port(), 0), pg_catalog.pg_postmaster_start_time() AT TIME ZONE 'UTC'
	 FROM pg_catalog.pg_control_system() c CROSS JOIN pg_catalog.pg_database d
	 WHERE d.datname=pg_catalog.current_database()`).Scan(&pid, &identity.SystemIdentifier,
		&identity.DatabaseOID, &identity.DatabaseName, &identity.ServerAddress,
		&identity.ServerPort, &identity.PostmasterStartedAt)
	if err != nil || pid <= 0 || identity.SystemIdentifier == "" || identity.DatabaseOID <= 0 || identity.DatabaseName == "" || identity.PostmasterStartedAt == "" {
		return RuntimeDatabaseIdentity{}, 0, ErrRuntimeBindingUnavailable
	}
	return identity, pid, nil
}

func BeginRuntimeBinding(ctx context.Context, db *sql.DB) (*RuntimeBindingProbe, error) {
	if db == nil {
		return nil, ErrRuntimeBindingUnavailable
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, ErrRuntimeBindingUnavailable
	}
	probe := &RuntimeBindingProbe{conn: conn}
	keep := false
	defer func() {
		if !keep {
			probe.Close()
		}
	}()
	if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&probe.request.BackendPID); err != nil {
		return nil, ErrRuntimeBindingUnavailable
	}
	var entropy [12]byte
	for {
		if _, err := rand.Read(entropy[:]); err != nil {
			return nil, ErrRuntimeBindingUnavailable
		}
		probe.request.ClassID = int64(binary.BigEndian.Uint32(entropy[:4]))
		if probe.request.ClassID != 0 && binary.BigEndian.Uint32(entropy[4:8]) != binary.BigEndian.Uint32(entropy[8:]) {
			break
		}
	}
	secrets := []int64{int64(binary.BigEndian.Uint32(entropy[4:8])), int64(binary.BigEndian.Uint32(entropy[8:]))}
	for _, secret := range secrets {
		key := int64(uint64(probe.request.ClassID)<<32 | uint64(secret))
		var acquired bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1::bigint)`, key).Scan(&acquired); err != nil || !acquired {
			return nil, ErrRuntimeBindingUnavailable
		}
	}
	probe.proof = runtimeBindingProof(probe.request, secrets)
	actual, err := observeRuntimeBinding(ctx, conn, probe.request)
	if err != nil || subtle.ConstantTimeCompare([]byte(actual), []byte(probe.proof)) != 1 {
		return nil, ErrRuntimeBindingUnavailable
	}
	keep = true
	return probe, nil
}

func (p *RuntimeBindingProbe) Request() RuntimeBindingRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.request
}

// Verify checks both the response and the original session's still-live probe.
// A response arriving after that backend died cannot pass by reconnecting.
func (p *RuntimeBindingProbe) Verify(ctx context.Context, proof string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil || len(proof) != 64 || subtle.ConstantTimeCompare([]byte(proof), []byte(p.proof)) != 1 {
		return ErrRuntimeBindingUnavailable
	}
	var currentPID int64
	if err := p.conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&currentPID); err != nil || currentPID != p.request.BackendPID {
		return ErrRuntimeBindingUnavailable
	}
	actual, err := observeRuntimeBinding(ctx, p.conn, p.request)
	if err != nil || subtle.ConstantTimeCompare([]byte(actual), []byte(proof)) != 1 {
		return ErrRuntimeBindingUnavailable
	}
	return nil
}

func (p *RuntimeBindingProbe) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		// Never return a session carrying either challenge lock to a pool.
		discardRuntimeController(p.conn)
		p.conn = nil
		p.proof = ""
	}
}

type runtimeBindingQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ObserveRuntimeBinding deliberately filters by the observing connection's
// current database. pg_locks otherwise also exposes probes in other databases.
// The caller must protect this observation with its ordinary admin authorization.
func ObserveRuntimeBinding(ctx context.Context, db *sql.DB, request RuntimeBindingRequest) (string, error) {
	if db == nil {
		return "", ErrRuntimeBindingUnavailable
	}
	return observeRuntimeBinding(ctx, db, request)
}

func observeRuntimeBinding(ctx context.Context, q runtimeBindingQuerier, request RuntimeBindingRequest) (string, error) {
	if request.Validate() != nil {
		return "", ErrRuntimeBindingUnavailable
	}
	rows, err := q.QueryContext(ctx, `SELECT objid::bigint FROM pg_locks WHERE
	 locktype='advisory' AND pid=$1 AND classid::bigint=$2 AND objsubid=1
	 AND mode='ExclusiveLock' AND granted
	 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())
	 ORDER BY objid LIMIT 3`, request.BackendPID, request.ClassID)
	if err != nil {
		return "", ErrRuntimeBindingUnavailable
	}
	defer rows.Close()
	var secrets []int64
	for rows.Next() {
		var secret int64
		if err := rows.Scan(&secret); err != nil {
			return "", ErrRuntimeBindingUnavailable
		}
		secrets = append(secrets, secret)
	}
	if rows.Err() != nil || len(secrets) != 2 {
		return "", ErrRuntimeBindingUnavailable
	}
	return runtimeBindingProof(request, secrets), nil
}

func runtimeBindingProof(request RuntimeBindingRequest, secrets []int64) string {
	ordered := slices.Clone(secrets)
	slices.Sort(ordered)
	var data [16]byte
	binary.BigEndian.PutUint32(data[:4], uint32(request.BackendPID))
	binary.BigEndian.PutUint32(data[4:8], uint32(request.ClassID))
	binary.BigEndian.PutUint32(data[8:12], uint32(ordered[0]))
	binary.BigEndian.PutUint32(data[12:], uint32(ordered[1]))
	hash := sha256.New()
	_, _ = hash.Write([]byte("gateway-runtime-database-binding-v1\x00"))
	_, _ = hash.Write(data[:])
	return hex.EncodeToString(hash.Sum(nil))
}
