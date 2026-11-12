package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func runtimeRestoreTestIdentity(t *testing.T, db *sql.DB) RuntimeDatabaseIdentity {
	t.Helper()
	probe, err := BeginRuntimeBinding(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	identity, err := probe.DatabaseIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestRuntimeRestoreBootstrapPersistentFence(t *testing.T) {
	source, _ := runtimeTestDatabase(t, true)
	target, dsn := runtimeTestDatabase(t, false)
	ctx := context.Background()
	sourceIdentity := runtimeRestoreTestIdentity(t, source)
	targetIdentity := runtimeRestoreTestIdentity(t, target)
	request := runtimeRequest()
	request.ExpectedEpoch = 17
	controller, oid, err := BootstrapRuntimeRestore(ctx, target, request, targetIdentity, sourceIdentity)
	if err != nil || oid <= 0 {
		t.Fatalf("bootstrap: oid=%d error=%v", oid, err)
	}
	defer controller.Close()
	if err := controller.WithQuiescentConnection(ctx, func(conn *sql.Conn) error {
		var actual int64
		var migrations sql.NullString
		if err := conn.QueryRowContext(ctx, `SELECT 'public.release_runtime_control'::regclass::oid::bigint,
		 to_regclass('public.schema_migrations')::text`).Scan(&actual, &migrations); err != nil {
			return err
		}
		if actual != oid || migrations.Valid {
			t.Fatal("bootstrap created an application catalog or returned wrong OID")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
	state, err := ReadRuntimeFence(ctx, target)
	if err != nil || !state.Active || state.Epoch != 18 || state.OperationID != request.OperationID {
		t.Fatalf("incorrect active fence: %+v %v", state, err)
	}
	controller.Close()
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
	resumed, err := ResumeRuntimeReleaseForDatabase(ctx, target, request, targetIdentity)
	if err != nil {
		t.Fatal("bootstrap fence is not recoverable", err)
	}
	defer resumed.Close()
	if quiet, err := resumed.TryQuiesce(ctx); err != nil || !quiet {
		t.Fatal("bootstrap fence cannot quiesce after reconnect", err)
	}
	if err := resumed.WithQuiescentConnection(ctx, func(conn *sql.Conn) error {
		var pid int64
		if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		_, err := target.ExecContext(ctx, `SELECT pg_terminate_backend($1)`, pid)
		return err
	}); err == nil {
		t.Fatal("lost bootstrap controller was silently replaced")
	}
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
	before, err := ReadRuntimeFence(ctx, source)
	if err != nil || before.Epoch != 0 || before.Active {
		t.Fatal("source was changed by target bootstrap", err)
	}
}

func TestRuntimeRestoreBootstrapRejectsTargets(t *testing.T) {
	source, _ := runtimeTestDatabase(t, true)
	sourceIdentity := runtimeRestoreTestIdentity(t, source)
	ctx := context.Background()
	for _, mode := range []string{"source-alias", "wrong-identity", "table", "schema", "function", "domain", "large-object", "default-privileges", "migrated", "runtime-lease", "controller-lock"} {
		t.Run(mode, func(t *testing.T) {
			target, dsn := runtimeTestDatabase(t, mode == "migrated")
			identity := runtimeRestoreTestIdentity(t, target)
			expected, original := identity, sourceIdentity
			switch mode {
			case "source-alias":
				original = identity
				original.DatabaseName, original.ServerAddress = "alias", "different-address"
			case "wrong-identity":
				expected.DatabaseOID++
			case "table":
				runtimeExec(t, target, `CREATE TABLE foreign_data(id integer)`)
			case "schema":
				runtimeExec(t, target, `CREATE SCHEMA unexpected`)
			case "function":
				runtimeExec(t, target, `CREATE FUNCTION public.unexpected() RETURNS integer LANGUAGE sql AS 'SELECT 1'`)
			case "domain":
				runtimeExec(t, target, `CREATE DOMAIN public.unexpected AS integer`)
			case "large-object":
				runtimeExec(t, target, `SELECT lo_create(0)`)
			case "default-privileges":
				runtimeExec(t, target, `ALTER DEFAULT PRIVILEGES GRANT SELECT ON TABLES TO PUBLIC`)
			case "runtime-lease":
				runtime, err := OpenRuntime(ctx, dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer runtime.Close()
			case "controller-lock":
				conn, err := target.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer discardRuntimeController(conn)
				if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, runtimeControllerLockID); err != nil {
					t.Fatal(err)
				}
			}
			controller, oid, err := BootstrapRuntimeRestore(ctx, target, runtimeRequest(), expected, original)
			if controller != nil {
				controller.Close()
			}
			want := ErrRuntimeReleaseConflict
			if mode == "runtime-lease" {
				want = ErrRuntimeNotQuiescent
			} else if mode == "controller-lock" {
				want = ErrRuntimeControllerBusy
			}
			if !errors.Is(err, want) || oid != 0 || controller != nil {
				t.Fatalf("unsafe bootstrap accepted: %d %v", oid, err)
			}
			if mode != "migrated" {
				var exists bool
				if err := target.QueryRowContext(ctx, `SELECT to_regclass('public.release_runtime_control') IS NOT NULL`).Scan(&exists); err != nil || exists {
					t.Fatal("rejected bootstrap wrote a fence", err)
				}
			}
		})
	}
}
