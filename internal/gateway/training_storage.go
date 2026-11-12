package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-access-gateway/internal/loratraining"
)

// This boundary exposes observations only. Mutations use the separately
// persisted operation endpoints and never reuse this preview route.
func (a *App) handleAdminTrainingStorage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "access_denied"})
		return
	}
	preview := r.URL.Path == "/admin/storage/training/preview"
	method := http.MethodGet
	if preview {
		method = http.MethodPost
	} else if r.URL.Path != "/admin/storage/training" {
		http.NotFound(w, r)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	bad := func() { writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_storage_request"}) }
	var ids []string
	cursor, limit := "", 50
	if preview {
		if a.csrfSigner == nil || !a.validCSRFValue(r, r.Header.Get("X-CSRF-Token")) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "csrf_failed"})
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" || r.URL.RawQuery != "" {
			bad()
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var input loratraining.StoragePreviewRequest
		if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF || loratraining.ValidateStorageIDs(input.IDs) != nil {
			bad()
			return
		}
		ids = input.IDs
	} else {
		if len(r.URL.RawQuery) > 2048 {
			bad()
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			bad()
			return
		}
		for key, values := range query {
			if len(values) != 1 || key != "after" && key != "limit" {
				bad()
				return
			}
		}
		cursor = query.Get("after")
		if raw, exists := query["limit"]; exists {
			limit, err = strconv.Atoi(raw[0])
			if err != nil || strconv.Itoa(limit) != raw[0] {
				bad()
				return
			}
		}
		if limit < 1 || limit > 100 || cursor != "" && !loratraining.ValidStorageTargetID(cursor) {
			bad()
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var inventory loratraining.StorageInventory
	var err error
	if preview {
		inventory, err = a.loraTraining.PreviewStorageCleanup(ctx, ids)
	} else {
		inventory, err = a.loraTraining.StorageInventory(ctx, cursor, limit)
	}
	if err != nil {
		status, code := http.StatusBadGateway, "training_storage_unavailable"
		var remote *loratraining.HTTPError
		if !a.loraTraining.Configured() {
			status, code = http.StatusServiceUnavailable, "training_storage_not_configured"
		} else if errors.As(err, &remote) && (remote.StatusCode == http.StatusNotFound || remote.StatusCode == http.StatusMethodNotAllowed) {
			status, code = http.StatusServiceUnavailable, "training_storage_unsupported"
		} else if errors.Is(err, loratraining.ErrStorageRequest) {
			code = "training_storage_invalid_response"
		}
		// Host errors can contain local paths; return a stable public code only.
		writeJSON(w, status, map[string]any{"error": code})
		return
	}
	response := map[string]any{"inventory": inventory, "manual_cleanup_available": !a.cfg.RestoreDrillMode}
	if a.cfg.RestoreDrillMode {
		response["manual_cleanup_reason"] = "restore_drill"
	}
	writeJSON(w, http.StatusOK, response)
}
