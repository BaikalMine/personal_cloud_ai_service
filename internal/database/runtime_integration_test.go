package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
)

func runtimeTestDatabase(t *testing.T, migrate bool) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	var id [12]byte
	if _, err = rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	name := "runtime_fence_" + hex.EncodeToString(id[:])
	if _, err = admin.ExecContext(ctx, `CREATE DATABASE `+pq.QuoteIdentifier(name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE `+pq.QuoteIdentifier(name)+` WITH (FORCE)`); err != nil {
			t.Error(err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/" + name
		q := u.Query()
		q.Del("dbname")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " dbname=" + name
	}
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if migrate {
		if err = Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	return db, dsn
}

func runtimeRequest() RuntimeReleaseRequest {
	return RuntimeReleaseRequest{OperationID: strings.Repeat("a", 32), Token: strings.Repeat("b", 64), PreviousManifestHash: strings.Repeat("c", 64), CandidateManifestHash: strings.Repeat("d", 64)}
}

func runtimeExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func requireRuntimeBlocked(t *testing.T, dsn string, want error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db, err := OpenRuntime(ctx, dsn)
	if db != nil {
		db.Close()
	}
	if !errors.Is(err, want) {
		t.Fatalf("runtime startup: %v, want %v", err, want)
	}
}

func TestRuntimeFencePoolsAndCrashRecovery(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, true)
	ctx := context.Background()
	request := runtimeRequest()
	runtime, err := OpenRuntime(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	first, err := runtime.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := runtime.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var firstPID int
	if err = first.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&firstPID); err != nil {
		t.Fatal(err)
	}
	controller, err := BeginRuntimeRelease(ctx, db, request)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	state, err := ReadRuntimeFence(ctx, db)
	if err != nil || !state.Active || state.Epoch != 1 {
		t.Fatalf("persistent fence: %+v %v", state, err)
	}
	data, _ := json.Marshal(state)
	if strings.Contains(string(data), request.Token) || strings.Contains(string(data), "token_hash") {
		t.Fatal("public state leaks ownership token")
	}
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
	if err = second.PingContext(ctx); err != nil {
		t.Fatal("draining invalidated an existing connection", err)
	}
	if yes, err := controller.TryQuiesce(ctx); yes || err != nil {
		t.Fatalf("pool ignored: %v %v", yes, err)
	}
	if err = controller.Unfence(ctx); !errors.Is(err, ErrRuntimeNotQuiescent) {
		t.Fatalf("premature unfence: %v", err)
	}
	if _, err = BeginRuntimeRelease(ctx, db, request); !errors.Is(err, ErrRuntimeControllerBusy) {
		t.Fatalf("two controllers: %v", err)
	}
	runtimeExec(t, db, `SELECT pg_terminate_backend($1)`, firstPID)
	_ = first.PingContext(ctx)
	_ = first.Close()
	if yes, err := controller.TryQuiesce(ctx); yes || err != nil {
		t.Fatalf("one dead session cleared whole pool: %v %v", yes, err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	if yes, err := controller.TryQuiesce(ctx); yes || err != nil {
		t.Fatalf("idle pooled session was ignored: %v %v", yes, err)
	}
	if err = runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if yes, err := controller.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("closed pool not recognized: %v %v", yes, err)
	}
	if err = controller.WithQuiescentConnection(ctx, func(c *sql.Conn) error { var one int; return c.QueryRowContext(ctx, `SELECT 1`).Scan(&one) }); err != nil {
		t.Fatal(err)
	}
	controller.Close()
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
	wrong := request
	wrong.Token = strings.Repeat("e", 64)
	if _, err = BeginRuntimeRelease(ctx, db, wrong); !errors.Is(err, ErrRuntimeReleaseConflict) {
		t.Fatalf("wrong owner resumed: %v", err)
	}
	resumed, err := BeginRuntimeRelease(ctx, db, request)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if yes, err := resumed.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("resume: %v %v", yes, err)
	}
	if err = resumed.Unfence(ctx); err != nil {
		t.Fatal(err)
	}
	if err = resumed.Unfence(ctx); err != nil {
		t.Fatal("unfence retry", err)
	}
	// The live exclusive lock still blocks starts until the controller closes.
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
	resumed.Close()
	if _, err = BeginRuntimeRelease(ctx, db, request); !errors.Is(err, ErrRuntimeReleaseClosed) {
		t.Fatalf("late start reopened completed operation: %v", err)
	}
	runtime, err = OpenRuntime(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Close()
	request.ExpectedEpoch = 1
	request.OperationID = strings.Repeat("f", 32)
	next, err := BeginRuntimeRelease(ctx, db, request)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
	state, err = ReadRuntimeFence(ctx, db)
	if err != nil || state.Epoch != 2 || !state.Active {
		t.Fatalf("epoch did not advance: %+v %v", state, err)
	}
	if _, err = BeginRuntimeRelease(ctx, db, runtimeRequest()); !errors.Is(err, ErrRuntimeReleaseConflict) {
		t.Fatalf("old epoch resumed: %v", err)
	}
}

func TestRuntimeFenceBootstrapAndCorruption(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, false)
	ctx := context.Background()
	runtime, err := OpenRuntime(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	runtime.Close()
	state, err := ReadRuntimeFence(ctx, db)
	if err != nil || state.Active || state.Epoch != 0 {
		t.Fatalf("bootstrap state: %+v %v", state, err)
	}
	runtimeExec(t, db, `ALTER TABLE release_runtime_control RENAME TO hidden_runtime_control`)
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenceInvalid)
	runtimeExec(t, db, `ALTER TABLE hidden_runtime_control RENAME TO release_runtime_control`)
	runtimeExec(t, db, `DELETE FROM release_runtime_control`)
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenceInvalid)
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM release_runtime_control`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration repaired a deleted gate: %d %v", count, err)
	}
}

func TestRuntimeFenceControllerSessionLossAndMisuse(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, true)
	ctx := context.Background()
	runtime, err := OpenRuntime(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BeginRuntimeRelease(ctx, runtime, runtimeRequest()); !errors.Is(err, ErrRuntimeControllerConnection) {
		t.Fatalf("leased controller accepted: %v", err)
	}
	runtime.Close()
	c, err := BeginRuntimeRelease(ctx, db, runtimeRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if yes, err := c.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("quiesce: %v %v", yes, err)
	}
	var pid int
	if err = c.WithQuiescentConnection(ctx, func(conn *sql.Conn) error { return conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid) }); err != nil {
		t.Fatal(err)
	}
	runtimeExec(t, db, `SELECT pg_terminate_backend($1)`, pid)
	if yes, err := c.TryQuiesce(ctx); yes || err == nil {
		t.Fatalf("dead controller claimed ownership: %v %v", yes, err)
	}
	if err = c.Unfence(ctx); err == nil {
		t.Fatal("dead controller cleared durable state")
	}
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
	c.Close()
	c, err = BeginRuntimeRelease(ctx, db, runtimeRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if yes, err := c.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("quiesce after crash: %v %v", yes, err)
	}
	err = c.WithQuiescentConnection(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock_all()`)
		return err
	})
	if !errors.Is(err, ErrRuntimeControllerConnection) {
		t.Fatalf("lost locks accepted: %v", err)
	}
	if err = c.Unfence(ctx); err == nil {
		t.Fatal("unlocked controller cleared durable state")
	}
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
}

func TestRuntimeFenceOldSchemaBootstrap(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, false)
	ctx := context.Background()
	runtimeExec(t, db, `CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	for _, migration := range migrationCatalog {
		if migration.version >= runtimeFenceMigrationID {
			break
		}
		for _, statement := range migration.statements {
			runtimeExec(t, db, statement)
		}
		runtimeExec(t, db, `INSERT INTO schema_migrations(version,name,checksum) VALUES ($1,$2,$3)`, migration.version, migration.name, migrationChecksum(migration))
	}
	runtime, err := OpenRuntime(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := Migrate(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	state, err := ReadRuntimeFence(ctx, db)
	if err != nil || state.Active || state.Epoch != 0 {
		t.Fatalf("old schema bootstrap: %+v %v", state, err)
	}
}

func TestRuntimeFenceCancelledConnectorDoesNotLeak(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, true)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`LOCK TABLE release_runtime_control IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	// The connector acquires its advisory lock, then waits on this table lock.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	runtime, err := OpenRuntime(ctx, dsn)
	if runtime != nil {
		runtime.Close()
	}
	if err == nil || ctx.Err() == nil {
		t.Fatalf("blocked connector did not cancel: %v (%v)", err, ctx.Err())
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	c, err := BeginRuntimeRelease(context.Background(), db, runtimeRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if yes, err := c.TryQuiesce(context.Background()); !yes || err != nil {
		t.Fatalf("cancelled connector leaked runtime lock: %v %v", yes, err)
	}
}

func TestRuntimeFenceTransactionAndSessionReuse(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, true)
	ctx := context.Background()
	runtime, err := OpenRuntime(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	runtime.SetMaxOpenConns(1)
	var originalPID int
	if err := runtime.QueryRow(`SELECT pg_backend_pid()`).Scan(&originalPID); err != nil {
		t.Fatal(err)
	}
	runtimeExec(t, runtime, `CREATE TABLE runtime_transaction_test (value INTEGER NOT NULL)`)
	stmt, err := runtime.PrepareContext(ctx, `INSERT INTO runtime_transaction_test(value) VALUES ($1)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i := 0; i < 2; i++ {
		tx, err := runtime.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.StmtContext(ctx, stmt).ExecContext(ctx, i); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if i == 0 {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	var count, pid int
	if err := runtime.QueryRow(`SELECT count(*), pg_backend_pid() FROM runtime_transaction_test`).Scan(&count, &pid); err != nil || count != 1 || pid != originalPID {
		t.Fatalf("transactions/prepared statements/session reuse: count=%d pid=%d original=%d err=%v", count, pid, originalPID, err)
	}
	c, err := BeginRuntimeRelease(ctx, db, runtimeRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if yes, err := c.TryQuiesce(ctx); yes || err != nil {
		t.Fatalf("session reset lost advisory lock: %v %v", yes, err)
	}
	stmt.Close()
	runtime.Close()
	if yes, err := c.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("closed reused session retained lock: %v %v", yes, err)
	}
}
