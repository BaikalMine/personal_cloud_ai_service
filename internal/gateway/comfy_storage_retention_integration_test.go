package gateway

import (
	"context"
	"os"
	"strings"
	"testing"

	"ai-access-gateway/internal/store"
)

func TestComfyStorageRetentionHTTPRecoveryIntegration(t *testing.T) {
	for _, mode := range []string{"success", "lost_response", "never_received", "guard_close_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newComfyStorageHTTPFixture(t, mode)
			ctx := context.Background()
			// System retention does not impersonate or depend on an administrator.
			if _, err := f.db.Exec(`DELETE FROM users WHERE id=$1`, f.actor.ID); err != nil {
				t.Fatal(err)
			}
			f.closeFails.Store(mode == "guard_close_failure")
			completed, err := f.app.cleanupComfyStorageRetention(ctx)
			if mode == "success" {
				if err != nil || completed != 1 {
					t.Fatal(completed, err)
				}
			} else if err == nil || completed != 0 {
				t.Fatal("unconfirmed execution reported success", completed, err)
			}
			history, err := f.app.store.ComfyStorageHistory(ctx, 0)
			if err != nil || len(history.Items) != 1 {
				t.Fatal(history, err)
			}
			op := history.Items[0]
			if op.Origin != "retention" || op.ActorID != nil || f.executions.Load() != 1 {
				t.Fatal(op, f.executions.Load())
			}
			if op.Active() {
				if n, _ := f.app.cleanupComfyStorageRetention(ctx); n != 0 || f.executions.Load() != 1 {
					t.Fatal("new deletion while prior claim unresolved", n, f.executions.Load())
				}
				f.restart(t)
				f.closeFails.Store(false)
				f.app.cfg.ComfyStorageCleanupEnabled = false
				if _, err := f.app.reconcileComfyStorageOperations(ctx); err != nil {
					t.Fatal(err)
				}
				op, err = f.app.store.ComfyStorageOperation(ctx, op.ID)
				if err != nil || op.Active() || f.executions.Load() != 1 {
					t.Fatal(op, err, f.executions.Load())
				}
			}
			if mode == "never_received" {
				if _, err := os.Stat(f.path); err != nil || op.FreedBytes != 0 {
					t.Fatal("unreceived request erased file or counted bytes", op, err)
				}
			} else if _, err := os.Stat(f.path); !os.IsNotExist(err) || op.FreedBytes != int64(len(f.contents)) {
				t.Fatal(op, err)
			}
			f.app.cfg.ComfyStorageCleanupEnabled = true
			if n, err := f.app.cleanupComfyStorageRetention(ctx); err != nil || n != 0 || f.executions.Load() != 1 {
				t.Fatal("repeated completed/deferred selection", n, err, f.executions.Load())
			}
		})
	}
}

func TestComfyStorageRetentionDisabledAndRestoreDrill(t *testing.T) {
	for _, restore := range []bool{false, true} {
		a := &App{}
		a.cfg.ComfyStorageCleanupEnabled = restore
		a.cfg.RestoreDrillMode = restore
		if n, err := a.cleanupComfyStorageRetention(context.Background()); err != nil || n != 0 {
			t.Fatal(n, err)
		}
	}
}

func TestComfyStorageRetentionDoesNotAdoptManualPreview(t *testing.T) {
	f := newComfyStorageHTTPFixture(t, "success")
	op := f.create(t, strings.Repeat("m", 32))
	status, err := f.app.updates.ComfyStorageStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := f.app.store.ClaimComfyStorageRetention(context.Background(), op.ID, f.app.updates.ComfyStorageHostKey(), status); err != store.ErrComfyStorageConflict || claimed {
		t.Fatal("adopted manual ID", claimed, err)
	}
	if f.executions.Load() != 0 {
		t.Fatal("executed preview")
	}
}

func TestComfyStorageRetentionMissingAndChangedFiles(t *testing.T) {
	for _, mode := range []string{"missing", "changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newComfyStorageHTTPFixture(t, "success")
			want := int64(0)
			if mode == "missing" {
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
				want = 1
			} else if err := os.WriteFile(f.path, []byte("replacement file"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if n, err := f.app.cleanupComfyStorageRetention(ctx); err != nil || n != want {
				t.Fatal(n, err)
			}
			history, err := f.app.store.ComfyStorageHistory(ctx, 0)
			if err != nil || len(history.Items) != 1 || history.FreedBytes != 0 || history.Items[0].Active() {
				t.Fatal(history, err)
			}
			if mode == "changed" {
				body, err := os.ReadFile(f.path)
				if err != nil || string(body) != "replacement file" {
					t.Fatal("changed file deleted", string(body), err)
				}
			}
			if n, err := f.app.cleanupComfyStorageRetention(ctx); err != nil || n != 0 || f.executions.Load() != 1 {
				t.Fatal("missing/changed item retried immediately", n, err, f.executions.Load())
			}
		})
	}
}
