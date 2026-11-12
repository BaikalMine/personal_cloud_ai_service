package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRuntimeFenceBoundDatabaseBeforeMutation(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, true)
	other, _ := runtimeTestDatabase(t, true)
	ctx := context.Background()
	probe, err := BeginRuntimeBinding(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := probe.DatabaseIdentity(ctx)
	probe.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, err := ReadRuntimeFence(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if controller, err := BeginRuntimeReleaseForDatabase(ctx, other, runtimeRequest(), identity); !errors.Is(err, ErrRuntimeReleaseConflict) {
		if controller != nil {
			controller.Close()
		}
		t.Fatal("wrong database accepted", err)
	}
	after, err := ReadRuntimeFence(ctx, other)
	if err != nil || before != after {
		t.Fatal("wrong database was fenced before identity check", err)
	}
	if resumed, err := ResumeRuntimeReleaseForDatabase(ctx, db, runtimeRequest(), identity); err == nil {
		resumed.Close()
		t.Fatal("resume created a previously absent fence")
	}
	untouched, err := ReadRuntimeFence(ctx, db)
	if err != nil || untouched.Active || untouched.Epoch != 0 {
		t.Fatal("resume changed an inactive database", err)
	}
	runtime, err := OpenRuntime(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	conn, err := runtime.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	controller, err := BeginRuntimeReleaseForDatabase(ctx, db, runtimeRequest(), identity)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if quiet, err := controller.TryQuiesce(ctx); err != nil || quiet {
		t.Fatal("live runtime fixture is not holding a connection", err)
	}
	called := false
	if err := controller.WithActiveFence(ctx, runtimeRequest(), identity, func(state RuntimeFenceState) error {
		called = true
		if !state.Active || state.Epoch != 1 {
			t.Fatal("wrong active fence")
		}
		return nil
	}); err != nil || !called {
		t.Fatal("active nonquiescent controller refused host action", err)
	}
	for _, mode := range []string{"token", "operation", "epoch", "database"} {
		request, expected := runtimeRequest(), identity
		switch mode {
		case "token":
			request.Token = strings.Repeat("e", 64)
		case "operation":
			request.OperationID = strings.Repeat("e", 32)
		case "epoch":
			request.ExpectedEpoch++
		case "database":
			expected.DatabaseOID++
		}
		if err := controller.WithActiveFence(ctx, request, expected, func(RuntimeFenceState) error { t.Fatal("mismatched callback", mode); return nil }); err == nil {
			t.Fatal("mismatch accepted", mode)
		}
	}
	if err := controller.WithActiveFence(ctx, runtimeRequest(), identity, func(RuntimeFenceState) error {
		var pid int64
		if err := controller.conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
			t.Fatal(err)
		}
		return nil
	}); err == nil {
		t.Fatal("controller loss after host action was hidden")
	}
	state, err := ReadRuntimeFence(ctx, db)
	if err != nil || !state.Active {
		t.Fatal("controller loss cleared durable fence", err)
	}
	if err := controller.WithActiveFence(ctx, runtimeRequest(), identity, func(RuntimeFenceState) error { t.Fatal("lost controller callback"); return nil }); err == nil {
		t.Fatal("lost session reconnected")
	}
	resumed, err := ResumeRuntimeReleaseForDatabase(ctx, db, runtimeRequest(), identity)
	if err != nil {
		t.Fatal("failed to reclaim active fence", err)
	}
	defer resumed.Close()
	if err := resumed.WithActiveFence(ctx, runtimeRequest(), identity, func(RuntimeFenceState) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
