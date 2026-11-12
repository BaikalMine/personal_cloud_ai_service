package database

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/lib/pq"
)

const (
	RuntimeFenceProtocol    = 1
	runtimeControllerLockID = int64(746218391)
	runtimeConnectionLockID = int64(746218392)
	runtimeValidationLockID = int64(746218393)
	runtimeFenceMigrationID = 93
	runtimeFenceReadSQL     = `SELECT left(row_to_json(c)::text,4097) FROM public.release_runtime_control c WHERE singleton=1`
)

var (
	ErrRuntimeFenced       = errors.New("runtime database access is fenced for release")
	ErrRuntimeFenceInvalid = errors.New("runtime release fence is missing or has an unsupported state")
	runtimeOperationID     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	runtimeDigest          = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type RuntimeFenceState struct {
	ProtocolVersion       int        `json:"protocol_version"`
	Epoch                 int64      `json:"epoch"`
	Active                bool       `json:"active"`
	OperationID           string     `json:"operation_id"`
	PreviousManifestHash  string     `json:"previous_manifest_hash"`
	CandidateManifestHash string     `json:"candidate_manifest_hash"`
	RequestedAt           *time.Time `json:"requested_at"`
	FinishedAt            *time.Time `json:"finished_at"`
}

type runtimeFenceRecord struct {
	RuntimeFenceState
	Singleton int    `json:"singleton"`
	TokenHash string `json:"token_hash"`
}

func parseRuntimeFence(data []byte) (runtimeFenceRecord, error) {
	var r runtimeFenceRecord
	var fields map[string]json.RawMessage
	if len(data) > 4096 || json.Unmarshal(data, &fields) != nil || len(fields) != 10 {
		return r, ErrRuntimeFenceInvalid
	}
	for _, name := range []string{"singleton", "protocol_version", "epoch", "active", "operation_id", "token_hash", "previous_manifest_hash", "candidate_manifest_hash", "requested_at", "finished_at"} {
		v, ok := fields[name]
		if !ok || (name != "requested_at" && name != "finished_at" && bytes.Equal(v, []byte("null"))) {
			return r, ErrRuntimeFenceInvalid
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || r.Singleton != 1 || r.ProtocolVersion != RuntimeFenceProtocol || r.Epoch < 0 {
		return r, ErrRuntimeFenceInvalid
	}
	if r.Epoch == 0 {
		if r.Active || r.OperationID != "" || r.TokenHash != "" || r.PreviousManifestHash != "" || r.CandidateManifestHash != "" || r.RequestedAt != nil || r.FinishedAt != nil {
			return r, ErrRuntimeFenceInvalid
		}
		return r, nil
	}
	if !runtimeOperationID.MatchString(r.OperationID) || !runtimeDigest.MatchString(r.TokenHash) || !runtimeDigest.MatchString(r.PreviousManifestHash) || !runtimeDigest.MatchString(r.CandidateManifestHash) || r.RequestedAt == nil || r.RequestedAt.IsZero() || (r.Active && r.FinishedAt != nil) || (!r.Active && (r.FinishedAt == nil || r.FinishedAt.Before(*r.RequestedAt))) {
		return r, ErrRuntimeFenceInvalid
	}
	return r, nil
}

// OpenRuntime requires session-affine PostgreSQL connections, not transaction
// pooling. Each physical connection holds a shared lock until it is closed.
func OpenRuntime(ctx context.Context, dsn string) (*sql.DB, error) {
	base, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, errors.New("runtime database configuration is invalid")
	}
	return waitForDatabase(ctx, sql.OpenDB(runtimeConnector{base: base}))
}

type runtimeConnector struct {
	base       driver.Connector
	validation *RuntimeValidationRequest
}

func (c runtimeConnector) Driver() driver.Driver { return c.base.Driver() }
func (c runtimeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	if err = checkRuntimeConnection(ctx, conn, c.validation); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// Preserve every pq interface; its ResetSession does not discard advisory locks.
	return conn, nil
}

func checkRuntimeConnection(ctx context.Context, conn driver.Conn, validation *RuntimeValidationRequest) error {
	v, err := runtimeDriverValue(ctx, conn, `SELECT pg_try_advisory_lock_shared($1)`, runtimeConnectionLockID)
	if err != nil {
		return err
	}
	if acquired, ok := v.(bool); !ok {
		return ErrRuntimeFenceInvalid
	} else if !acquired {
		return ErrRuntimeFenced
	}
	v, err = runtimeDriverValue(ctx, conn, `SELECT to_regclass('public.release_runtime_control') IS NOT NULL`)
	if err != nil {
		return err
	}
	exists, ok := v.(bool)
	if !ok {
		return ErrRuntimeFenceInvalid
	}
	if !exists {
		if validation != nil {
			return ErrRuntimeFenceInvalid
		}
		// Bootstrap older/pristine databases, but never repair a missing fence
		// table after its migration has been recorded.
		v, err = runtimeDriverValue(ctx, conn, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`)
		if err != nil {
			return err
		}
		migrated, ok := v.(bool)
		if !ok {
			return ErrRuntimeFenceInvalid
		}
		if migrated {
			v, err = runtimeDriverValue(ctx, conn, `SELECT EXISTS(SELECT 1 FROM public.schema_migrations WHERE version >= $1)`, int64(runtimeFenceMigrationID))
			if err != nil {
				return err
			}
			claimed, ok := v.(bool)
			if !ok || claimed {
				return ErrRuntimeFenceInvalid
			}
		}
		return nil
	}
	v, err = runtimeDriverValue(ctx, conn, runtimeFenceReadSQL)
	if err != nil {
		return err
	}
	var data []byte
	switch v := v.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return ErrRuntimeFenceInvalid
	}
	r, err := parseRuntimeFence(data)
	if err != nil {
		return err
	}
	if validation != nil {
		if !validation.matches(r) {
			return ErrRuntimeFenced
		}
		v, err = runtimeDriverValue(ctx, conn, runtimeValidationGrantSQL, runtimeControllerLockID, runtimeValidationLockID)
		if err != nil {
			return err
		}
		if granted, ok := v.(bool); !ok || !granted {
			return ErrRuntimeFenced
		}
		return nil
	}
	if r.Active {
		return ErrRuntimeFenced
	}
	return nil
}

func runtimeDriverValue(ctx context.Context, conn driver.Conn, query string, arguments ...any) (result driver.Value, resultErr error) {
	q, ok := conn.(driver.QueryerContext)
	if !ok {
		return nil, ErrRuntimeFenceInvalid
	}
	args := make([]driver.NamedValue, len(arguments))
	for i, v := range arguments {
		args[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	rows, err := q.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	// Unlike sql.Rows, driver.Rows need not tolerate a second Close. lib/pq's
	// cancel watcher can panic on duplicate completion with a live context.
	defer func() {
		if err := rows.Close(); resultErr == nil && err != nil {
			result, resultErr = nil, err
		}
	}()
	if len(rows.Columns()) != 1 {
		return nil, ErrRuntimeFenceInvalid
	}
	values := make([]driver.Value, 1)
	if err = rows.Next(values); err != nil {
		if errors.Is(err, io.EOF) {
			err = ErrRuntimeFenceInvalid
		}
		return nil, err
	}
	v := values[0]
	if data, ok := v.([]byte); ok {
		v = bytes.Clone(data)
	}
	if err = rows.Next(values); !errors.Is(err, io.EOF) {
		if err == nil {
			err = ErrRuntimeFenceInvalid
		}
		return nil, err
	}
	return v, nil
}
