package release

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
)

func TestDatabasePreflightOnExclusiveOwnerConnection(t *testing.T) {
	db := releaseTestDatabase(t)
	_, manifest := fixtureManifest(t)
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	controller, err := database.BeginRuntimeRelease(ctx, db, database.RuntimeReleaseRequest{
		OperationID: strings.Repeat("a", 32), Token: strings.Repeat("b", 64),
		PreviousManifestHash: strings.Repeat("c", 64), CandidateManifestHash: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if yes, err := controller.TryQuiesce(ctx); err != nil || !yes {
		t.Fatalf("quiesce: %v %v", yes, err)
	}
	err = controller.WithQuiescentConnection(ctx, func(conn *sql.Conn) error {
		var before, after int
		if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&before); err != nil {
			return err
		}
		// The controller owns the pool's only connection. The old pool-based API
		// must time out here rather than silently borrowing a second connection.
		blocked, blockedCancel := context.WithTimeout(ctx, 30*time.Millisecond)
		wrong, wrongErr := InspectDatabase(blocked, db, manifest, manifest)
		blockedCancel()
		if wrongErr == nil || wrong.FailureCode != "database_unavailable" {
			t.Fatalf("pool unexpectedly available: %+v %v", wrong, wrongErr)
		}
		report, err := InspectDatabaseOnConnection(ctx, conn, manifest, manifest)
		if err != nil || !report.DatabaseChecksPassed || !report.WorkloadIdle || report.DeploymentAuthorized || len(report.Workload) != len(releaseWorkloadQueries) {
			t.Fatalf("owner observation: %+v %v", report, err)
		}
		if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&after); err != nil {
			return err
		}
		if before != after || db.Stats().OpenConnections != 1 {
			t.Fatalf("physical owner changed: %d -> %d; %+v", before, after, db.Stats())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Observation must not clear the durable fence or release either owner lock.
	if yes, err := controller.TryQuiesce(ctx); err != nil || !yes {
		t.Fatalf("observation lost exclusion: %v %v", yes, err)
	}
	controller.Close()
	state, err := database.ReadRuntimeFence(ctx, db)
	if err != nil || !state.Active {
		t.Fatalf("observation cleared fence: %+v %v", state, err)
	}
}

func TestDatabasePreflightConnectionCannotReconnect(t *testing.T) {
	db := releaseTestDatabase(t)
	_, manifest := fixtureManifest(t)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := InspectDatabaseOnConnection(ctx, conn, manifest, manifest)
	if err == nil || report.FailureCode != "database_unavailable" || report.DatabaseChecksPassed || report.WorkloadChecked {
		t.Fatalf("closed connection reconnected: %+v %v", report, err)
	}
	if report, err = InspectDatabase(ctx, db, manifest, manifest); err != nil || !report.DatabaseChecksPassed {
		t.Fatalf("healthy pool fixture unavailable: %+v %v", report, err)
	}
}
