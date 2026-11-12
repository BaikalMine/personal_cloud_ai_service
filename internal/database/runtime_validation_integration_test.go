package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func runtimeValidationRequest() RuntimeValidationRequest {
	r := runtimeRequest()
	return RuntimeValidationRequest{OperationID: r.OperationID, Epoch: 1, CandidateManifestHash: r.CandidateManifestHash, Token: r.Token}
}

func requireRuntimeValidationBlocked(t *testing.T, dsn string, request RuntimeValidationRequest) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db, err := OpenRuntimeValidation(ctx, dsn, request)
	if db != nil {
		db.Close()
	}
	if !errors.Is(err, ErrRuntimeFenced) {
		t.Fatalf("validation startup: %v, want runtime fence", err)
	}
}

func TestRuntimeFenceValidationWindow(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, true)
	ctx := context.Background()
	request := runtimeValidationRequest()
	runtime, err := OpenRuntime(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	c, err := BeginRuntimeRelease(ctx, db, runtimeRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	requireRuntimeValidationBlocked(t, dsn, request)
	if err := c.AllowValidationConnections(ctx); !errors.Is(err, ErrRuntimeNotQuiescent) {
		t.Fatalf("validation allowed before quiescence: %v", err)
	}
	runtime.Close()
	if yes, err := c.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("quiesce: %v %v", yes, err)
	}
	requireRuntimeValidationBlocked(t, dsn, request)
	if err := c.WithQuiescentConnection(ctx, func(conn *sql.Conn) error { return MigrateOnConnection(ctx, conn) }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := c.AllowValidationConnections(ctx); err != nil {
			t.Fatal(err)
		}
	}
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
	for _, mutate := range []func(*RuntimeValidationRequest){
		func(r *RuntimeValidationRequest) { r.Token = strings.Repeat("e", 64) },
		func(r *RuntimeValidationRequest) { r.OperationID = strings.Repeat("e", 32) },
		func(r *RuntimeValidationRequest) { r.Epoch++ },
		func(r *RuntimeValidationRequest) { r.CandidateManifestHash = strings.Repeat("e", 64) },
	} {
		wrong := request
		mutate(&wrong)
		requireRuntimeValidationBlocked(t, dsn, wrong)
	}
	validation, err := OpenRuntimeValidation(ctx, dsn, request)
	if err != nil {
		t.Fatal(err)
	}
	defer validation.Close()
	if err := CheckRuntimeValidation(ctx, validation, request); err != nil {
		t.Fatal(err)
	}
	if err := RequireCurrentSchema(ctx, validation); err != nil {
		t.Fatal(err)
	}
	if err := c.Unfence(ctx); !errors.Is(err, ErrRuntimeNotQuiescent) {
		t.Fatalf("validation window committed: %v", err)
	}
	if err := c.WithQuiescentConnection(ctx, func(*sql.Conn) error { t.Fatal("unguarded callback invoked"); return nil }); !errors.Is(err, ErrRuntimeNotQuiescent) {
		t.Fatalf("controller used validation window for DB work: %v", err)
	}
	if yes, err := c.TryQuiesce(ctx); yes || err != nil {
		t.Fatalf("validation pool ignored: %v %v", yes, err)
	}
	// TryQuiesce revokes the positive grant even while the old pool still drains.
	requireRuntimeValidationBlocked(t, dsn, request)
	if err := CheckRuntimeValidation(ctx, validation, request); !errors.Is(err, ErrRuntimeFenced) {
		t.Fatalf("existing connection missed revoked grant: %v", err)
	}
	validation.Close()
	if yes, err := c.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("closed validation pool retained lock: %v %v", yes, err)
	}
	if err := c.Unfence(ctx); err != nil {
		t.Fatal(err)
	}
	c.Close()
	requireRuntimeValidationBlocked(t, dsn, request)
	runtime, err = OpenRuntime(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Close()
}

func TestRuntimeFenceValidationControllerLoss(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, true)
	ctx := context.Background()
	c, err := BeginRuntimeRelease(ctx, db, runtimeRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if yes, err := c.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("quiesce: %v %v", yes, err)
	}
	var pid int
	if err := c.WithQuiescentConnection(ctx, func(conn *sql.Conn) error { return conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid) }); err != nil {
		t.Fatal(err)
	}
	if err := c.AllowValidationConnections(ctx); err != nil {
		t.Fatal(err)
	}
	validation, err := OpenRuntimeValidation(ctx, dsn, runtimeValidationRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer validation.Close()
	runtimeExec(t, db, `SELECT pg_terminate_backend($1)`, pid)
	requireRuntimeValidationBlocked(t, dsn, runtimeValidationRequest())
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
	if err := CheckRuntimeValidation(ctx, validation, runtimeValidationRequest()); !errors.Is(err, ErrRuntimeFenced) {
		t.Fatalf("dead controller retained validation permission: %v", err)
	}
	c.Close()
	resumed, err := BeginRuntimeRelease(ctx, db, runtimeRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if err := resumed.AllowValidationConnections(ctx); !errors.Is(err, ErrRuntimeNotQuiescent) {
		t.Fatalf("resumed controller implicitly reopened validation: %v", err)
	}
	if yes, err := resumed.TryQuiesce(ctx); yes || err != nil {
		t.Fatalf("orphan validation pool ignored: %v %v", yes, err)
	}
	validation.Close()
	if yes, err := resumed.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("quiesce after candidate close: %v %v", yes, err)
	}
	if err := resumed.AllowValidationConnections(ctx); err != nil {
		t.Fatal(err)
	}
	validation, err = OpenRuntimeValidation(ctx, dsn, runtimeValidationRequest())
	if err != nil {
		t.Fatal(err)
	}
	validation.Close()
}

func TestRuntimeFenceValidationGrantIsDatabaseScoped(t *testing.T) {
	first, firstDSN := runtimeTestDatabase(t, true)
	second, secondDSN := runtimeTestDatabase(t, true)
	ctx := context.Background()
	for index, db := range []*sql.DB{first, second} {
		c, err := BeginRuntimeRelease(ctx, db, runtimeRequest())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if index == 0 {
			if yes, err := c.TryQuiesce(ctx); !yes || err != nil {
				t.Fatalf("quiesce: %v %v", yes, err)
			}
			if err := c.AllowValidationConnections(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	validation, err := OpenRuntimeValidation(ctx, firstDSN, runtimeValidationRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer validation.Close()
	requireRuntimeValidationBlocked(t, secondDSN, runtimeValidationRequest())
	if err := CheckRuntimeValidation(ctx, second, runtimeValidationRequest()); !errors.Is(err, ErrRuntimeFenced) {
		t.Fatalf("borrowed grant from another database: %v", err)
	}
}

func TestRuntimeFenceValidationSchemaReadiness(t *testing.T) {
	db, _ := runtimeTestDatabase(t, true)
	ctx := context.Background()
	db.SetMaxOpenConns(1)
	runtimeExec(t, db, `SET default_transaction_read_only=on`)
	if err := RequireCurrentSchema(ctx, db); err != nil {
		t.Fatal("schema check is not read-only", err)
	}
	runtimeExec(t, db, `SET default_transaction_read_only=off`)
	runtimeExec(t, db, `UPDATE schema_migrations SET checksum=$1 WHERE version=93`, strings.Repeat("0", 64))
	if err := RequireCurrentSchema(ctx, db); !errors.Is(err, ErrRuntimeSchemaMismatch) {
		t.Fatalf("changed migration accepted: %v", err)
	}
	var checksum string
	if err := db.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=93`).Scan(&checksum); err != nil || checksum != strings.Repeat("0", 64) {
		t.Fatalf("readiness repaired catalog: %s %v", checksum, err)
	}
	runtimeExec(t, db, `DELETE FROM schema_migrations WHERE version=93`)
	if err := RequireCurrentSchema(ctx, db); !errors.Is(err, ErrRuntimeSchemaMismatch) {
		t.Fatalf("older catalog accepted: %v", err)
	}
}
