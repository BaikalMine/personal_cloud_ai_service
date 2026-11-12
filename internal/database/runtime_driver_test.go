package database

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/lib/pq"
)

type runtimeValueTestConnection struct {
	driver.Conn
	rows driver.Rows
	err  error
}

func (c runtimeValueTestConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return c.rows, c.err
}

type runtimeValueTestRows struct {
	columns    []string
	values     [][]driver.Value
	nextError  error
	closeError error
	closed     int
	wipe       []byte
}

func (r *runtimeValueTestRows) Columns() []string { return r.columns }
func (r *runtimeValueTestRows) Close() error {
	r.closed++
	clear(r.wipe)
	return r.closeError
}
func (r *runtimeValueTestRows) Next(values []driver.Value) error {
	if r.nextError != nil {
		return r.nextError
	}
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(values, r.values[0])
	r.values = r.values[1:]
	return nil
}

func TestRuntimeDriverValueClose(t *testing.T) {
	nextError, closeError := errors.New("next failed"), errors.New("close failed")
	for _, mode := range []string{"value", "no-rows", "columns", "extra-row", "next-error", "close-error", "query-error"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte("value")
			rows := &runtimeValueTestRows{columns: []string{"value"}, values: [][]driver.Value{{body}}, wipe: body}
			conn := runtimeValueTestConnection{rows: rows}
			var expected error
			switch mode {
			case "no-rows":
				rows.values, expected = nil, ErrRuntimeFenceInvalid
			case "columns":
				rows.columns, expected = []string{"one", "two"}, ErrRuntimeFenceInvalid
			case "extra-row":
				rows.values, expected = [][]driver.Value{{body}, {body}}, ErrRuntimeFenceInvalid
			case "next-error":
				rows.nextError, expected = nextError, nextError
			case "close-error":
				rows.closeError, expected = closeError, closeError
			case "query-error":
				conn.err, expected = nextError, nextError
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			value, err := runtimeDriverValue(ctx, conn, "fixture")
			if !errors.Is(err, expected) {
				t.Fatalf("got %v, want %v", err, expected)
			}
			wantedCloses := 1
			if mode == "query-error" {
				wantedCloses = 0
			}
			if rows.closed != wantedCloses {
				t.Fatalf("driver Rows.Close called %d times, want %d", rows.closed, wantedCloses)
			}
			if expected == nil {
				copied, ok := value.([]byte)
				if !ok || !bytes.Equal(copied, []byte("value")) {
					t.Fatal("driver buffer escaped rows lifetime")
				}
			} else if value != nil {
				t.Fatal("errored result returned a value")
			}
		})
	}
}

func TestRuntimeDriverValuePostgreSQL(t *testing.T) {
	_, dsn := runtimeTestDatabase(t, false)
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := connector.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Direct driver.Rows is not the idempotent database/sql.Rows wrapper. A
	// duplicate close races lib/pq's cancel watcher even with a live context.
	for i := 0; i < 100; i++ {
		value, err := runtimeDriverValue(ctx, conn, `SELECT 1::bigint`)
		if err != nil || value != int64(1) {
			t.Fatal("repeated driver query", value, err)
		}
	}
}
