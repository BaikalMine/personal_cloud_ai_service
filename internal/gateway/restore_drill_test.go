package gateway

import (
	"context"
	"testing"
)

func TestRestoreDrillDoesNotStartBackgroundWork(t *testing.T) {
	// No database or clients: touching a worker dependency would fail this test.
	app := &App{cfg: Config{RestoreDrillMode: true}, maintenanceDone: make(chan struct{})}
	app.startBackgroundWork(context.Background())
	select {
	case <-app.maintenanceDone:
	default:
		t.Fatal("restore shutdown would wait for a worker that must not start")
	}
}
