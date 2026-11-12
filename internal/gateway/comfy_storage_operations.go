package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

var errComfyStorageReadOnly = errors.New("ComfyUI storage is read-only in restore drill mode")
var errComfyStorageDisabled = errors.New("selected ComfyUI storage cleanup is disabled")

func (a *App) handleAdminComfyStorageOperations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "access_denied"})
		return
	}
	const root = "/admin/storage/comfy/operations"
	id, action := "", ""
	if r.URL.Path != root {
		if !strings.HasPrefix(r.URL.Path, root+"/") {
			http.NotFound(w, r)
			return
		}
		var found bool
		id, action, found = strings.Cut(strings.TrimPrefix(r.URL.Path, root+"/"), "/")
		if !updates.ValidComfyStorageID(id) || found && action != "confirm" && action != "cancel" && action != "reconcile" {
			http.NotFound(w, r)
			return
		}
	}
	if r.Method == http.MethodGet && action == "" {
		if id != "" {
			if r.URL.RawQuery != "" {
				writeComfyStorageError(w, store.ErrComfyStorageInput)
				return
			}
			op, err := a.store.ComfyStorageOperation(r.Context(), id)
			if err != nil {
				writeComfyStorageError(w, err)
				return
			}
			a.writeComfyStorageOperation(w, op, actor.ID)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(r.URL.RawQuery) > 2048 || len(query) > 1 {
			writeComfyStorageError(w, store.ErrComfyStorageInput)
			return
		}
		offset := 0
		for key, values := range query {
			if key != "offset" || len(values) != 1 {
				writeComfyStorageError(w, store.ErrComfyStorageInput)
				return
			}
			offset, err = strconv.Atoi(values[0])
			if err != nil || strconv.Itoa(offset) != values[0] {
				writeComfyStorageError(w, store.ErrComfyStorageInput)
				return
			}
		}
		history, err := a.store.ComfyStorageHistory(r.Context(), offset)
		if err != nil {
			writeComfyStorageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, history)
		return
	}
	if r.Method != http.MethodPost || id != "" && action == "" {
		allow := "POST"
		if action == "" {
			allow = "GET"
			if id == "" {
				allow = "GET, POST"
			}
		}
		w.Header().Set("Allow", allow)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	if a.csrfSigner == nil || !a.validCSRFValue(r, r.Header.Get("X-CSRF-Token")) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "csrf_failed"})
		return
	}
	if a.cfg.RestoreDrillMode {
		writeComfyStorageError(w, errComfyStorageReadOnly)
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" || r.URL.RawQuery != "" {
		writeComfyStorageError(w, store.ErrComfyStorageInput)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if id == "" {
		var input struct {
			ID    string                                `json:"id"`
			Items []store.ComfyStorageRegistrySelection `json:"items"`
		}
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !updates.ValidComfyStorageID(input.ID) || len(input.Items) == 0 || len(input.Items) > updates.MaxComfyStorageSelection {
			writeComfyStorageError(w, store.ErrComfyStorageInput)
			return
		}
		op, err := a.prepareComfyStorageOperation(r.Context(), actor.ID, input.ID, input.Items)
		if err != nil {
			writeComfyStorageError(w, err)
			return
		}
		a.writeComfyStorageOperation(w, op, actor.ID)
		return
	}
	if action == "confirm" {
		var input struct {
			Hash    string `json:"request_hash"`
			Confirm bool   `json:"confirm"`
		}
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !input.Confirm || !updates.ComfyStorageHex(input.Hash, 32) {
			writeComfyStorageError(w, store.ErrComfyStorageInput)
			return
		}
		op, err := a.confirmComfyStorageOperation(r.Context(), actor.ID, id, input.Hash)
		if err != nil {
			writeComfyStorageError(w, err)
			return
		}
		a.writeComfyStorageOperation(w, op, actor.ID)
		return
	}
	if decoder.Decode(&struct{}{}) != nil || decoder.Decode(new(any)) != io.EOF {
		writeComfyStorageError(w, store.ErrComfyStorageInput)
		return
	}
	var op store.ComfyStorageOperation
	if action == "cancel" {
		op, err = a.store.CancelComfyStoragePreview(r.Context(), actor.ID, id)
	} else {
		op, err = a.store.ComfyStorageOperation(r.Context(), id)
		if err == nil && op.Active() {
			op, err = a.reconcileComfyStorageOperation(r.Context(), op)
		}
	}
	if err != nil {
		writeComfyStorageError(w, err)
		return
	}
	a.writeComfyStorageOperation(w, op, actor.ID)
}

func (a *App) writeComfyStorageOperation(w http.ResponseWriter, op store.ComfyStorageOperation, actorID int64) {
	status := http.StatusOK
	if op.Active() {
		status = http.StatusAccepted
	}
	var files []updates.ComfyStorageSelection
	if op.Request != nil {
		files = op.Request.Items
	}
	ownPreview := !a.cfg.RestoreDrillMode && op.Origin == "manual" && op.ActorID != nil && *op.ActorID == actorID && op.State == "preview" && op.PrunedAt == nil
	writeJSON(w, status, map[string]any{"operation": op, "files": files,
		"can_confirm": ownPreview && op.ExpiresAt.After(time.Now()) && a.cfg.ComfyStorageCleanupEnabled && a.cfg.ComfyGPUGuardRequired,
		"can_cancel":  ownPreview, "can_reconcile": !a.cfg.RestoreDrillMode && op.Active(),
	})
}

func writeComfyStorageError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "storage_operation_failed"
	switch {
	case errors.Is(err, errComfyStorageReadOnly):
		status, code = http.StatusForbidden, "storage_read_only"
	case errors.Is(err, errComfyStorageDisabled):
		status, code = http.StatusForbidden, "storage_cleanup_disabled"
	case errors.Is(err, store.ErrComfyStorageInput), errors.Is(err, store.ErrComfyStorageQuery):
		status, code = http.StatusBadRequest, "invalid_storage_request"
	case errors.Is(err, store.ErrComfyStorageDenied):
		status, code = http.StatusForbidden, "storage_operation_denied"
	case errors.Is(err, sql.ErrNoRows):
		status, code = http.StatusNotFound, "storage_operation_missing"
	case errors.Is(err, store.ErrComfyStorageExpired):
		status, code = http.StatusGone, "storage_operation_expired"
	case errors.Is(err, store.ErrComfyStorageBusy), errors.Is(err, store.ErrGPUWorkConflict), errors.Is(err, updates.ErrComfyStorageBusy):
		status, code = http.StatusConflict, "storage_admission_busy"
	case errors.Is(err, store.ErrComfyStorageConflict):
		status, code = http.StatusConflict, "storage_operation_changed"
	case errors.Is(err, updates.ErrComfyStorageRequest):
		status, code = http.StatusBadGateway, "comfy_storage_invalid_response"
	case errors.Is(err, updates.ErrUnavailable):
		status, code = http.StatusServiceUnavailable, "comfy_storage_unavailable"
	}
	writeJSON(w, status, map[string]any{"error": code})
}

func (a *App) comfyStorageMutationAllowed() error {
	if a.cfg.RestoreDrillMode {
		return errComfyStorageReadOnly
	}
	if !a.cfg.ComfyStorageCleanupEnabled || !a.cfg.ComfyGPUGuardRequired {
		return errComfyStorageDisabled
	}
	if !a.updates.Configured() {
		return updates.ErrUnavailable
	}
	return nil
}

func (a *App) prepareComfyStorageOperation(ctx context.Context, actorID int64, id string, selection []store.ComfyStorageRegistrySelection) (store.ComfyStorageOperation, error) {
	if err := a.comfyStorageMutationAllowed(); err != nil {
		return store.ComfyStorageOperation{}, err
	}
	query := store.ComfyStorageQuery{Limit: 50}
	for _, item := range selection {
		if !updates.ComfyStorageHex(item.Version, 32) || strings.HasPrefix(item.Key, "output:") {
			return store.ComfyStorageOperation{}, store.ErrComfyStorageInput
		}
		query.Keys = append(query.Keys, item.Key)
	}
	if len(selection) == 0 || query.Validate() != nil {
		return store.ComfyStorageOperation{}, store.ErrComfyStorageInput
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status, err := a.updates.ComfyStorageStatus(ctx)
	if err != nil {
		return store.ComfyStorageOperation{}, err
	}
	return a.store.CreateComfyStorageOperation(ctx, actorID, id, a.updates.ComfyStorageHostKey(), status, selection)
}

func (a *App) confirmComfyStorageOperation(ctx context.Context, actorID int64, id, hash string) (store.ComfyStorageOperation, error) {
	if err := a.comfyStorageMutationAllowed(); err != nil {
		return store.ComfyStorageOperation{}, err
	}
	op, err := a.store.ComfyStorageOperation(ctx, id)
	if err != nil {
		return op, err
	}
	if op.PrunedAt != nil {
		return op, store.ErrComfyStorageExpired
	}
	if op.Request == nil {
		return op, store.ErrComfyStorageInput
	}
	// A duplicate confirm reads the stored decision and never needs a healthy
	// host. Only a still-unclaimed preview obtains a fresh host identity.
	status := updates.ComfyStorageStatus{Protocol: updates.ComfyStorageProtocol, ExecutorID: op.Request.ExecutorID, GuardJournalID: op.Request.GuardJournalID, GuardEnforced: true}
	if op.State == "preview" {
		check, cancel := context.WithTimeout(ctx, 30*time.Second)
		status, err = a.updates.ComfyStorageStatus(check)
		cancel()
		if err != nil {
			return op, err
		}
	}
	var claimed bool
	err = a.store.WithGPUAdmissionGuard(ctx, func() error {
		var err error
		op, claimed, err = a.store.DispatchComfyStorageOperation(ctx, actorID, id, a.updates.ComfyStorageHostKey(), hash, status)
		return err
	})
	if err != nil || !claimed {
		return op, err
	}
	return a.executeClaimedComfyStorageOperation(ctx, op)
}

func (a *App) executeClaimedComfyStorageOperation(ctx context.Context, op store.ComfyStorageOperation) (store.ComfyStorageOperation, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, err := a.updates.ExecuteComfyStorage(requestCtx, *op.Request)
	if result.Protocol != 0 {
		return a.persistComfyStorageResult(ctx, op, result)
	}
	if err != nil {
		return a.deferComfyStorageOperation(ctx, op, comfyStorageRemoteFailureCode(err))
	}
	return a.deferComfyStorageOperation(ctx, op, "invalid_response")
}

func comfyStorageRemoteFailureCode(err error) string {
	if errors.Is(err, updates.ErrComfyStorageRequest) {
		return "invalid_response"
	}
	if errors.Is(err, updates.ErrComfyStorageConflict) {
		return "host_changed"
	}
	return "host_unavailable"
}

func (a *App) deferComfyStorageOperation(ctx context.Context, op store.ComfyStorageOperation, code string) (store.ComfyStorageOperation, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.store.MarkComfyStorageUnconfirmed(cleanup, op.ID, op.HostKey, code); err != nil {
		return op, err
	}
	return a.store.ComfyStorageOperation(cleanup, op.ID)
}

func (a *App) persistComfyStorageResult(ctx context.Context, op store.ComfyStorageOperation, result updates.ComfyStorageResult) (store.ComfyStorageOperation, error) {
	if op.Request == nil || updates.MatchComfyStorageResult(result, *op.Request) != nil {
		return a.deferComfyStorageOperation(ctx, op, "invalid_response")
	}
	if !result.ExecutorFenced {
		code := "host_unconfirmed"
		if result.State == "running" {
			code = "host_running"
		}
		return a.deferComfyStorageOperation(ctx, op, code)
	}
	if !result.GuardClosed {
		return a.deferComfyStorageOperation(ctx, op, "guard_not_closed")
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	settled, err := a.store.SettleComfyStorageOperation(cleanup, op.ID, op.HostKey, result)
	if err == nil {
		return settled, nil
	}
	code := "result_write_failed"
	if errors.Is(err, store.ErrComfyStorageInput) || errors.Is(err, store.ErrComfyStorageConflict) {
		code = "invalid_response"
	}
	return a.deferComfyStorageOperation(ctx, op, code)
}

func (a *App) reconcileComfyStorageOperation(ctx context.Context, op store.ComfyStorageOperation) (store.ComfyStorageOperation, error) {
	if a.cfg.RestoreDrillMode {
		return op, errComfyStorageReadOnly
	}
	if !op.Active() {
		return op, nil
	}
	// Disabling new cleanup does not abandon a previously durable claim.
	if !a.updates.Configured() || op.HostKey != a.updates.ComfyStorageHostKey() {
		return a.deferComfyStorageOperation(ctx, op, "host_changed")
	}
	if op.Request == nil {
		return op, store.ErrComfyStorageInput
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status, err := a.updates.ComfyStorageStatus(ctx)
	if err != nil {
		return a.deferComfyStorageOperation(ctx, op, comfyStorageRemoteFailureCode(err))
	}
	if status.ExecutorID != op.Request.ExecutorID || status.GuardJournalID != op.Request.GuardJournalID {
		return a.deferComfyStorageOperation(ctx, op, "host_changed")
	}
	result, err := a.updates.ReadComfyStorage(ctx, *op.Request)
	if result.Protocol != 0 {
		if updates.MatchComfyStorageResult(result, *op.Request) != nil {
			return a.deferComfyStorageOperation(ctx, op, "invalid_response")
		}
		if result.GuardClosed || result.State == "running" {
			return a.persistComfyStorageResult(ctx, op, result)
		}
	} else if !errors.Is(err, updates.ErrComfyStorageNotFound) {
		return a.deferComfyStorageOperation(ctx, op, comfyStorageRemoteFailureCode(err))
	}
	// Missing/unconfirmed journals and incomplete guard closure use only Fence.
	// A delayed Execute races this same ID and is fenced at the host, not retried.
	fenced, err := a.updates.FenceComfyStorage(ctx, *op.Request)
	if fenced.Protocol != 0 {
		return a.persistComfyStorageResult(ctx, op, fenced)
	}
	if err != nil {
		return a.deferComfyStorageOperation(ctx, op, comfyStorageRemoteFailureCode(err))
	}
	return a.deferComfyStorageOperation(ctx, op, "invalid_response")
}

func (a *App) reconcileComfyStorageOperations(ctx context.Context) (int64, error) {
	if a.cfg.RestoreDrillMode {
		return 0, nil
	}
	ops, err := a.store.PendingComfyStorageOperations(ctx)
	if err != nil {
		return 0, err
	}
	var checked int64
	for _, op := range ops {
		if op.State == "dispatching" && op.DispatchedAt != nil && time.Since(*op.DispatchedAt) < 3*time.Minute {
			continue
		}
		if _, err := a.reconcileComfyStorageOperation(ctx, op); err != nil {
			return checked, err
		}
		checked++
	}
	return checked, nil
}
