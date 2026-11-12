package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"ai-access-gateway/internal/config"
	"ai-access-gateway/internal/store"
)

func TestStorageOperationCapabilities(t *testing.T) {
	owner := int64(9007199254740993)
	other := owner - 1
	now := time.Now()
	cases := []struct {
		name, state                                       string
		actor                                             int64
		noOwner, expired, pruned, system, drill, disabled bool
		confirm, cancel, reconcile                        bool
	}{
		{name: "own preview", state: "preview", actor: owner, confirm: true, cancel: true},
		{name: "different exact actor", state: "preview", actor: other},
		{name: "deleted actor", state: "preview", actor: owner, noOwner: true},
		{name: "expired preview", state: "preview", actor: owner, expired: true, cancel: true},
		{name: "pruned preview", state: "preview", actor: owner, pruned: true},
		{name: "system preview", state: "preview", actor: owner, system: true},
		{name: "read only preview", state: "preview", actor: owner, drill: true},
		{name: "disabled new cleanup", state: "preview", actor: owner, disabled: true, cancel: true},
		{name: "active other admin", state: "unconfirmed", actor: other, reconcile: true},
		{name: "active system", state: "dispatching", actor: other, system: true, reconcile: true},
		{name: "disabled active recovery", state: "unconfirmed", actor: owner, disabled: true, reconcile: true},
		{name: "read only active", state: "dispatching", actor: owner, drill: true},
		{name: "completed", state: "completed", actor: owner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{cfg: config.Config{RestoreDrillMode: tc.drill, ComfyStorageCleanupEnabled: !tc.disabled, ComfyGPUGuardRequired: true}}
			op := store.ComfyStorageOperation{ID: "capability-test", Origin: "manual", ActorID: &owner, State: tc.state, ExpiresAt: now.Add(time.Hour)}
			if tc.noOwner {
				op.ActorID = nil
			}
			if tc.expired {
				op.ExpiresAt = now.Add(-time.Hour)
			}
			if tc.pruned {
				op.PrunedAt = &now
			}
			if tc.system {
				op.Origin = "retention"
			}
			response := httptest.NewRecorder()
			app.writeComfyStorageOperation(response, op, tc.actor)
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]bool{"can_confirm": tc.confirm, "can_cancel": tc.cancel, "can_reconcile": tc.reconcile} {
				var got bool
				if err := json.Unmarshal(envelope[key], &got); err != nil || got != want {
					t.Errorf("%s=%v want %v (%v)", key, got, want, err)
				}
			}
			expectedStatus := 200
			if op.Active() {
				expectedStatus = 202
			}
			if response.Code != expectedStatus {
				t.Errorf("status=%d want%d", response.Code, expectedStatus)
			}
			if tc.system {
				return
			}
			training := store.TrainingStorageOperation{ID: op.ID, ActorID: op.ActorID, State: op.State, ExpiresAt: op.ExpiresAt, PrunedAt: op.PrunedAt}
			data := app.trainingStorageOperationEnvelope(training, tc.actor)
			wantConfirm := tc.confirm || tc.disabled && tc.state == "preview"
			for key, want := range map[string]bool{"can_confirm": wantConfirm, "can_cancel": tc.cancel, "can_reconcile": tc.reconcile} {
				if data[key] != want {
					t.Errorf("training %s=%v want%v", key, data[key], want)
				}
			}
		})
	}
}
