package database

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/lib/pq"
)

func TestRuntimeBindingSameDatabaseAndLifetime(t *testing.T) {
	db, _ := runtimeTestDatabase(t, false)
	ctx := context.Background()
	probe, err := BeginRuntimeBinding(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	request := probe.Request()
	proof, err := ObserveRuntimeBinding(ctx, db, request)
	if err != nil || len(proof) != 64 || probe.Verify(ctx, proof) != nil {
		t.Fatalf("same database proof: %q %v", proof, err)
	}
	body, _ := json.Marshal(request)
	var fields map[string]any
	if json.Unmarshal(body, &fields) != nil || len(fields) != 2 || strings.Contains(string(body), proof) {
		t.Fatalf("request exposed its proof: %s", body)
	}
	if !errors.Is(probe.Verify(ctx, strings.Repeat("0", 64)), ErrRuntimeBindingUnavailable) {
		t.Fatal("accepted fabricated proof")
	}
	for _, wrong := range []RuntimeBindingRequest{{request.BackendPID + 1, request.ClassID}, {request.BackendPID, request.ClassID + 1}} {
		if _, err := ObserveRuntimeBinding(ctx, db, wrong); !errors.Is(err, ErrRuntimeBindingUnavailable) {
			t.Fatalf("accepted wrong request: %+v", wrong)
		}
	}
	probe.Close()
	probe.Close()
	if probe.Verify(ctx, proof) == nil {
		t.Fatal("accepted a closed probe")
	}
	if _, err := ObserveRuntimeBinding(ctx, db, request); err == nil {
		t.Fatal("closed probe still visible")
	}
	var locks int
	if err := db.QueryRow(`SELECT count(*) FROM pg_locks WHERE pid=$1 AND locktype='advisory'`, request.BackendPID).Scan(&locks); err != nil || locks != 0 {
		t.Fatalf("probe returned locked connection: %d %v", locks, err)
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("binding changed application schema: %d %v", tables, err)
	}
}

func TestRuntimeBindingDatabaseIdentityOnOriginalSession(t *testing.T) {
	db, _ := runtimeTestDatabase(t, false)
	ctx := context.Background()
	probe, err := BeginRuntimeBinding(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	identity, err := probe.DatabaseIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var oid int64
	var name, system string
	if err := db.QueryRow(`SELECT d.oid::bigint, d.datname, c.system_identifier::text FROM pg_database d CROSS JOIN pg_control_system() c WHERE d.datname=current_database()`).Scan(&oid, &name, &system); err != nil {
		t.Fatal(err)
	}
	if identity.DatabaseOID != oid || identity.DatabaseName != name || identity.SystemIdentifier != system || identity.PostmasterStartedAt == "" {
		t.Fatal("identity differs from selected DB")
	}
	if _, err := db.Exec(`SELECT pg_terminate_backend($1)`, probe.Request().BackendPID); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.DatabaseIdentity(ctx); !errors.Is(err, ErrRuntimeBindingUnavailable) {
		t.Fatal("identity reconnected after backend death", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("healthy pool fixture unavailable", err)
	}
	probe.Close()
	if _, err := probe.DatabaseIdentity(ctx); !errors.Is(err, ErrRuntimeBindingUnavailable) {
		t.Fatal("closed probe returned identity", err)
	}
}

func TestRuntimeBindingDatabaseIdentityRequiresPermission(t *testing.T) {
	db, _ := runtimeTestDatabase(t, false)
	ctx := context.Background()
	probe, err := BeginRuntimeBinding(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	role := "binding_identity_" + strconv.FormatInt(probe.Request().BackendPID, 10)
	if _, err := db.Exec(`CREATE ROLE ` + pq.QuoteIdentifier(role)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		probe.Close()
		if _, err := db.Exec(`DROP ROLE ` + pq.QuoteIdentifier(role)); err != nil {
			t.Error(err)
		}
	})
	// pg_control_system is executable by PUBLIC in this PostgreSQL version.
	// Restrict it only in this freshly created test database to exercise denial.
	if _, err := db.Exec(`REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.conn.ExecContext(ctx, `SET ROLE `+pq.QuoteIdentifier(role)); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.DatabaseIdentity(ctx); !errors.Is(err, ErrRuntimeBindingUnavailable) {
		t.Fatal("unprivileged identity read accepted", err)
	}
	var privileged bool
	if err := db.QueryRow(`SELECT rolsuper FROM pg_roles WHERE rolname=$1`, role).Scan(&privileged); err != nil || privileged {
		t.Fatal("identity probe altered role privileges", err)
	}
	if err := db.QueryRow(`SELECT has_function_privilege($1, 'pg_catalog.pg_control_system()', 'EXECUTE')`, role).Scan(&privileged); err != nil || privileged {
		t.Fatal("identity probe granted itself access", err)
	}
}

func TestRuntimeBindingRejectsOtherDatabaseOnSameCluster(t *testing.T) {
	db, _ := runtimeTestDatabase(t, false)
	other, _ := runtimeTestDatabase(t, false)
	ctx := context.Background()
	probe, err := BeginRuntimeBinding(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	request := probe.Request()
	var globallyVisible int
	if err := other.QueryRow(`SELECT count(*) FROM pg_locks WHERE pid=$1 AND classid::bigint=$2 AND locktype='advisory'`, request.BackendPID, request.ClassID).Scan(&globallyVisible); err != nil || globallyVisible != 2 {
		t.Fatalf("cross-database fixture is not observable: %d %v", globallyVisible, err)
	}
	if _, err := ObserveRuntimeBinding(ctx, other, request); !errors.Is(err, ErrRuntimeBindingUnavailable) {
		t.Fatal("accepted another database's globally visible locks")
	}
}

func TestRuntimeBindingRejectsLostAndMalformedLocks(t *testing.T) {
	for _, mode := range []string{"one", "three", "replaced", "shared", "two_int", "backend_lost"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := runtimeTestDatabase(t, false)
			ctx := context.Background()
			probe, err := BeginRuntimeBinding(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Close()
			request := probe.Request()
			original, err := ObserveRuntimeBinding(ctx, db, request)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "backend_lost" {
				if _, err := db.Exec(`SELECT pg_terminate_backend($1)`, request.BackendPID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := probe.conn.ExecContext(ctx, `SELECT pg_advisory_unlock_all()`); err != nil {
					t.Fatal(err)
				}
				n := 2
				if mode == "one" {
					n = 1
				}
				if mode == "three" {
					n = 3
				}
				for index := 1; index <= n; index++ {
					key := int64(uint64(request.ClassID)<<32 | uint64(index))
					query := `SELECT pg_advisory_lock($1::bigint)`
					if mode == "shared" {
						query = `SELECT pg_advisory_lock_shared($1::bigint)`
					}
					if mode == "two_int" {
						if _, err := probe.conn.ExecContext(ctx, `SELECT pg_advisory_lock($1::int,$2::int)`, int32(request.ClassID), index); err != nil {
							t.Fatal(err)
						}
					} else if _, err := probe.conn.ExecContext(ctx, query, key); err != nil {
						t.Fatal(err)
					}
				}
			}
			observed, err := ObserveRuntimeBinding(ctx, db, request)
			if mode == "replaced" {
				if err != nil || observed == original || probe.Verify(ctx, observed) == nil {
					t.Fatal("replacement locks matched the original probe")
				}
			} else if !errors.Is(err, ErrRuntimeBindingUnavailable) {
				t.Fatalf("accepted %s locks", mode)
			}
			if !errors.Is(probe.Verify(ctx, original), ErrRuntimeBindingUnavailable) {
				t.Fatalf("accepted old proof after %s", mode)
			}
		})
	}
}

func TestRuntimeBindingFreshProbeRejectsReplay(t *testing.T) {
	db, _ := runtimeTestDatabase(t, false)
	ctx := context.Background()
	first, err := BeginRuntimeBinding(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := BeginRuntimeBinding(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	proof, err := ObserveRuntimeBinding(ctx, db, first.Request())
	if err != nil || second.Verify(ctx, proof) == nil || first.Request() == second.Request() {
		t.Fatal("independent probe accepted a replay")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if first.Verify(ctx, proof) == nil {
		t.Fatal("cancelled verification succeeded")
	}
}

func TestRuntimeBindingRejectsInvalidAndCancelledProbe(t *testing.T) {
	db, _ := runtimeTestDatabase(t, false)
	for _, request := range []RuntimeBindingRequest{{}, {1, 0}, {0, 1}, {-1, 1}, {1, -1}, {1 << 31, 1}, {1, 1 << 32}} {
		if _, err := ObserveRuntimeBinding(context.Background(), db, request); !errors.Is(err, ErrRuntimeBindingUnavailable) {
			t.Fatalf("accepted malformed request %+v", request)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if probe, err := BeginRuntimeBinding(ctx, db); probe != nil || !errors.Is(err, ErrRuntimeBindingUnavailable) {
		t.Fatalf("cancelled probe: %v %v", probe, err)
	}
	if probe, err := BeginRuntimeBinding(context.Background(), nil); probe != nil || !errors.Is(err, ErrRuntimeBindingUnavailable) {
		t.Fatalf("nil database: %v %v", probe, err)
	}
}
