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

	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/store"
)

var errTrainingStorageReadOnly = errors.New("training storage is read-only in restore drill mode")

func (a *App) handleAdminTrainingStorageOperations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "access_denied"})
		return
	}
	const root = "/admin/storage/training/operations"
	id, action := "", ""
	if r.URL.Path != root {
		var found bool
		id, action, found = strings.Cut(strings.TrimPrefix(r.URL.Path, root+"/"), "/")
		if !loratraining.ValidStorageOperationID(id) || found && action != "confirm" && action != "cancel" && action != "reconcile" {
			http.NotFound(w, r)
			return
		}
	}
	if r.Method == http.MethodGet && action == "" {
		if id != "" {
			if r.URL.RawQuery != "" {
				writeTrainingStorageError(w, store.ErrTrainingStorageInput)
				return
			}
			op, err := a.store.TrainingStorageOperation(r.Context(), id)
			if err != nil {
				writeTrainingStorageError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, a.trainingStorageOperationEnvelope(op, actor.ID))
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(r.URL.RawQuery) > 2048 || len(query) > 1 {
			writeTrainingStorageError(w, store.ErrTrainingStorageInput)
			return
		}
		offset := 0
		for key, values := range query {
			if key != "offset" || len(values) != 1 {
				writeTrainingStorageError(w, store.ErrTrainingStorageInput)
				return
			}
			offset, err = strconv.Atoi(values[0])
			if err != nil || strconv.Itoa(offset) != values[0] {
				writeTrainingStorageError(w, store.ErrTrainingStorageInput)
				return
			}
		}
		history, err := a.store.TrainingStorageHistory(r.Context(), offset)
		if err != nil {
			writeTrainingStorageError(w, err)
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
		writeTrainingStorageError(w, errTrainingStorageReadOnly)
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" || r.URL.RawQuery != "" {
		writeTrainingStorageError(w, store.ErrTrainingStorageInput)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if id == "" {
		var input struct {
			ID  string   `json:"id"`
			IDs []string `json:"ids"`
		}
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !loratraining.ValidStorageOperationID(input.ID) || loratraining.ValidateStorageIDs(input.IDs) != nil {
			writeTrainingStorageError(w, store.ErrTrainingStorageInput)
			return
		}
		op, err := a.prepareTrainingStorageOperation(r.Context(), actor.ID, input.ID, input.IDs)
		if err != nil {
			writeTrainingStorageError(w, err)
			return
		}
		a.writeTrainingStorageOperation(w, op, actor.ID)
		return
	}
	if action == "confirm" {
		var input struct {
			Hash    string `json:"request_hash"`
			Confirm bool   `json:"confirm"`
		}
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !input.Confirm || !loratraining.ValidStorageVersion(input.Hash) {
			writeTrainingStorageError(w, store.ErrTrainingStorageInput)
			return
		}
		op, err := a.confirmTrainingStorageOperation(r.Context(), actor.ID, id, input.Hash)
		if err != nil {
			writeTrainingStorageError(w, err)
			return
		}
		a.writeTrainingStorageOperation(w, op, actor.ID)
		return
	}
	if decoder.Decode(&struct{}{}) != nil || decoder.Decode(new(any)) != io.EOF {
		writeTrainingStorageError(w, store.ErrTrainingStorageInput)
		return
	}
	var op store.TrainingStorageOperation
	if action == "cancel" {
		op, err = a.store.CancelTrainingStoragePreview(r.Context(), actor.ID, id)
	} else {
		op, err = a.store.TrainingStorageOperation(r.Context(), id)
		if err == nil && op.Active() {
			op, err = a.reconcileTrainingStorageOperation(r.Context(), op)
		}
	}
	if err != nil {
		writeTrainingStorageError(w, err)
		return
	}
	a.writeTrainingStorageOperation(w, op, actor.ID)
}

func (a *App) writeTrainingStorageOperation(w http.ResponseWriter, op store.TrainingStorageOperation, actorID int64) {
	status := http.StatusOK
	if op.Active() {
		status = http.StatusAccepted
	}
	writeJSON(w, status, a.trainingStorageOperationEnvelope(op, actorID))
}

func (a *App) trainingStorageOperationEnvelope(op store.TrainingStorageOperation, actorID int64) map[string]any {
	ownPreview := !a.cfg.RestoreDrillMode && op.ActorID != nil && *op.ActorID == actorID && op.State == "preview" && op.PrunedAt == nil
	return map[string]any{"operation": op,
		"can_confirm": ownPreview && op.ExpiresAt.After(time.Now()), "can_cancel": ownPreview,
		"can_reconcile": !a.cfg.RestoreDrillMode && op.Active(),
	}
}

func writeTrainingStorageError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "storage_operation_failed"
	var remote *loratraining.HTTPError
	switch {
	case errors.Is(err, errTrainingStorageReadOnly):
		status, code = http.StatusForbidden, "storage_read_only"
	case errors.Is(err, store.ErrTrainingStorageInput):
		status, code = http.StatusBadRequest, "invalid_storage_request"
	case errors.Is(err, store.ErrTrainingStorageDenied):
		status, code = http.StatusForbidden, "storage_operation_denied"
	case errors.Is(err, sql.ErrNoRows):
		status, code = http.StatusNotFound, "storage_operation_missing"
	case errors.Is(err, store.ErrTrainingStorageExpired):
		status, code = http.StatusGone, "storage_operation_expired"
	case errors.Is(err, store.ErrTrainingStorageBusy), errors.Is(err, store.ErrGPUWorkConflict):
		status, code = http.StatusConflict, "storage_admission_busy"
	case errors.Is(err, store.ErrTrainingStorageConflict):
		status, code = http.StatusConflict, "storage_operation_changed"
	case errors.Is(err, loratraining.ErrStorageRequest):
		status, code = http.StatusBadGateway, "training_storage_invalid_response"
	case errors.Is(err, loratraining.ErrUnavailable):
		status, code = http.StatusServiceUnavailable, "training_storage_unavailable"
	case errors.As(err, &remote):
		status, code = http.StatusBadGateway, "training_storage_unavailable"
		if remote.StatusCode == 404 || remote.StatusCode == 405 {
			status, code = http.StatusServiceUnavailable, "training_storage_unsupported"
		}
	}
	writeJSON(w, status, map[string]any{"error": code})
}

func (a *App) prepareTrainingStorageOperation(ctx context.Context, actorID int64, id string, ids []string) (store.TrainingStorageOperation, error) {
	if a.cfg.RestoreDrillMode {
		return store.TrainingStorageOperation{}, errTrainingStorageReadOnly
	}
	if !a.loraTraining.Configured() {
		return store.TrainingStorageOperation{}, loratraining.ErrUnavailable
	}
	host := a.loraTraining.StorageHostKey()
	if existing, err := a.store.TrainingStorageOperation(ctx, id); err == nil {
		if existing.ActorID == nil || *existing.ActorID != actorID {
			return existing, store.ErrTrainingStorageDenied
		}
		if existing.PrunedAt != nil {
			return existing, store.ErrTrainingStorageExpired
		}
		if existing.HostKey != host || !existing.MatchesSelection(ids) {
			return existing, store.ErrTrainingStorageConflict
		}
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return existing, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	preview, err := a.loraTraining.PreviewStorageCleanup(ctx, ids)
	if err != nil {
		return store.TrainingStorageOperation{}, err
	}
	return a.store.CreateTrainingStorageOperation(ctx, actorID, id, host, preview)
}

func (a *App) confirmTrainingStorageOperation(ctx context.Context, actorID int64, id, hash string) (store.TrainingStorageOperation, error) {
	if a.cfg.RestoreDrillMode {
		return store.TrainingStorageOperation{}, errTrainingStorageReadOnly
	}
	if !a.loraTraining.Configured() {
		return store.TrainingStorageOperation{}, loratraining.ErrUnavailable
	}
	var op store.TrainingStorageOperation
	var claimed bool
	err := a.store.WithGPUAdmissionGuard(ctx, func() error {
		var err error
		op, claimed, err = a.store.DispatchTrainingStorageOperation(ctx, actorID, id, a.loraTraining.StorageHostKey(), hash)
		return err
	})
	if err != nil || !claimed {
		return op, err
	}
	// Only the caller that committed preview -> dispatching may issue this POST.
	// All duplicate/recovery paths below read or fence, never resend deletion.
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, err := a.loraTraining.CleanupStorage(requestCtx, *op.Request)
	if err != nil {
		return a.deferTrainingStorageOperation(ctx, op, storageRemoteFailureCode(err))
	}
	return a.persistTrainingStorageResult(ctx, op, result)
}

func storageRemoteFailureCode(err error) string {
	if errors.Is(err, loratraining.ErrStorageRequest) {
		return "invalid_response"
	}
	return "host_unavailable"
}

func (a *App) deferTrainingStorageOperation(ctx context.Context, op store.TrainingStorageOperation, code string) (store.TrainingStorageOperation, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.store.MarkTrainingStorageUnconfirmed(cleanup, op.ID, op.HostKey, code); err != nil {
		return op, err
	}
	return a.store.TrainingStorageOperation(cleanup, op.ID)
}

func (a *App) persistTrainingStorageResult(ctx context.Context, op store.TrainingStorageOperation, result loratraining.StorageCleanupResult) (store.TrainingStorageOperation, error) {
	if result.State == "running" {
		return a.deferTrainingStorageOperation(ctx, op, "host_running")
	}
	if result.State == "unconfirmed" {
		return a.deferTrainingStorageOperation(ctx, op, "host_unconfirmed")
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	settled, err := a.store.SettleTrainingStorageOperation(cleanup, op.ID, op.HostKey, result)
	if err == nil {
		return settled, nil
	}
	code := "result_write_failed"
	if errors.Is(err, store.ErrTrainingStorageInput) || errors.Is(err, store.ErrTrainingStorageConflict) {
		code = "invalid_response"
	}
	return a.deferTrainingStorageOperation(ctx, op, code)
}

func (a *App) reconcileTrainingStorageOperation(ctx context.Context, op store.TrainingStorageOperation) (store.TrainingStorageOperation, error) {
	if a.cfg.RestoreDrillMode {
		return op, errTrainingStorageReadOnly
	}
	if !op.Active() {
		return op, nil
	}
	if !a.loraTraining.Configured() || op.HostKey != a.loraTraining.StorageHostKey() {
		return a.deferTrainingStorageOperation(ctx, op, "host_changed")
	}
	if op.Request == nil {
		return op, store.ErrTrainingStorageInput
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := a.loraTraining.StorageCleanupResult(ctx, op.ID)
	if err == nil && result.RequestHash != op.RequestHash {
		return a.deferTrainingStorageOperation(ctx, op, "invalid_response")
	}
	if err == nil && result.State != "unconfirmed" {
		return a.persistTrainingStorageResult(ctx, op, result)
	}
	var remote *loratraining.HTTPError
	if err != nil && !(errors.As(err, &remote) && (remote.StatusCode == http.StatusNotFound || remote.StatusCode == http.StatusGone)) {
		return a.deferTrainingStorageOperation(ctx, op, storageRemoteFailureCode(err))
	}
	// Closing the exact saved ID handles a request that never arrived, a lost
	// journal result, and a delayed original POST racing with this recovery.
	fenced, err := a.loraTraining.FenceStorageCleanup(ctx, *op.Request)
	if err != nil {
		return a.deferTrainingStorageOperation(ctx, op, storageRemoteFailureCode(err))
	}
	return a.persistTrainingStorageResult(ctx, op, fenced.Result)
}

func (a *App) reconcileTrainingStorageOperations(ctx context.Context) (int64, error) {
	if a.cfg.RestoreDrillMode {
		return 0, nil
	}
	ops, err := a.store.PendingTrainingStorageOperations(ctx)
	if err != nil {
		return 0, err
	}
	var checked int64
	for _, op := range ops {
		if op.State == "dispatching" && op.DispatchedAt != nil && time.Since(*op.DispatchedAt) < 3*time.Minute {
			continue
		}
		if _, err := a.reconcileTrainingStorageOperation(ctx, op); err != nil {
			return checked, err
		}
		checked++
	}
	return checked, nil
}
