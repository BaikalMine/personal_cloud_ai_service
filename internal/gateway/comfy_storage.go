package gateway

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

type comfyStorageSelection struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

type comfyStorageInspectionItem struct {
	Key        string   `json:"key"`
	Version    string   `json:"version"`
	Status     string   `json:"status"`
	Protection []string `json:"protection"`
}

type comfyStorageInspection struct {
	CheckedAt     time.Time                    `json:"checked_at"`
	HostCheckedAt *time.Time                   `json:"host_checked_at,omitempty"`
	Complete      bool                         `json:"complete"`
	HostState     string                       `json:"host_state"`
	Items         []comfyStorageInspectionItem `json:"items"`
}

func parseComfyStorageQuery(raw string) (store.ComfyStorageQuery, error) {
	query := store.ComfyStorageQuery{Limit: 50}
	values, err := url.ParseQuery(raw)
	if err != nil || len(raw) > 2048 {
		return query, store.ErrComfyStorageQuery
	}
	for key, entries := range values {
		if len(entries) != 1 {
			return query, store.ErrComfyStorageQuery
		}
		switch key {
		case "kind":
			query.Kind = entries[0]
		case "state":
			query.State = entries[0]
		case "q":
			query.Search = entries[0]
		case "limit", "offset":
			n, err := strconv.Atoi(entries[0])
			if err != nil || strconv.Itoa(n) != entries[0] {
				return query, store.ErrComfyStorageQuery
			}
			if key == "limit" {
				query.Limit = n
			} else {
				query.Offset = n
			}
		default:
			return query, store.ErrComfyStorageQuery
		}
	}
	return query, query.Validate()
}

func (a *App) handleAdminComfyStorage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "access_denied"})
		return
	}
	inspect := r.URL.Path == "/admin/storage/comfy/inspect"
	if !inspect && r.URL.Path != "/admin/storage/comfy" {
		http.NotFound(w, r)
		return
	}
	method := http.MethodGet
	if inspect {
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	bad := func() { writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_storage_request"}) }
	query := store.ComfyStorageQuery{Limit: 50}
	var selection []comfyStorageSelection
	if inspect {
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
		var input struct {
			Items []comfyStorageSelection `json:"items"`
		}
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || len(input.Items) == 0 || len(input.Items) > 50 {
			bad()
			return
		}
		for _, item := range input.Items {
			digest, err := hex.DecodeString(item.Version)
			if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != item.Version {
				bad()
				return
			}
			query.Keys = append(query.Keys, item.Key)
		}
		if query.Validate() != nil {
			bad()
			return
		}
		selection = input.Items
	} else {
		var err error
		query, err = parseComfyStorageQuery(r.URL.RawQuery)
		if err != nil {
			bad()
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	inventory, err := a.store.ComfyStorageInventory(ctx, query)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "comfy_storage_unavailable"})
		return
	}
	response := map[string]any{"manual_cleanup_available": false, "manual_cleanup_reason": "executor_not_fenced", "scope": "tracked_records"}
	if a.cfg.RestoreDrillMode {
		response["manual_cleanup_reason"] = "restore_drill"
	} else if a.cfg.ComfyStorageCleanupEnabled && a.cfg.ComfyGPUGuardRequired {
		status, statusErr := a.updates.ComfyStorageStatus(ctx)
		response["manual_cleanup_reason"] = "host_unavailable"
		if statusErr == nil {
			response["manual_cleanup_available"] = true
			response["manual_cleanup_reason"] = "confirmation_required"
			if status.GuardHeld || status.PendingID != "" {
				response["manual_cleanup_available"] = false
				response["manual_cleanup_reason"] = "storage_admission_busy"
			}
		}
	}
	if !inspect {
		response["inventory"] = inventory
		response["host_configured"] = a.updates.Configured()
		writeJSON(w, http.StatusOK, response)
		return
	}
	byKey := make(map[string]domain.ComfyStorageItem, len(inventory.Items))
	for _, item := range inventory.Items {
		byKey[item.Key] = item
	}
	ordered := make([]domain.ComfyStorageItem, 0, len(selection))
	for _, item := range selection {
		current, ok := byKey[item.Key]
		if !ok || current.Version != item.Version {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "storage_selection_changed"})
			return
		}
		ordered = append(ordered, current)
	}
	response["inspection"] = a.inspectComfyStorage(ctx, ordered)
	writeJSON(w, http.StatusOK, response)
}

func (a *App) inspectComfyStorage(ctx context.Context, items []domain.ComfyStorageItem) comfyStorageInspection {
	inspection := comfyStorageInspection{Complete: true, HostState: "not_requested", Items: []comfyStorageInspectionItem{}}
	files := []updates.ComfyAssetFile{}
	positions := []int{}
	for i, item := range items {
		status := "fingerprint_unavailable"
		if item.State == "reserved" {
			status = "upload_unconfirmed"
		}
		if item.CanInspect {
			status = "host_unavailable"
			files = append(files, updates.ComfyAssetFile{Filename: item.Filename, Subfolder: item.Subfolder, StorageType: item.StorageType, SizeBytes: item.SizeBytes, SHA256: item.ContentHash})
			positions = append(positions, i)
		} else {
			inspection.Complete = false
		}
		inspection.Items = append(inspection.Items, comfyStorageInspectionItem{Key: item.Key, Version: item.Version, Status: status, Protection: item.Protection})
	}
	if len(files) > 0 {
		result, err := a.updates.InspectComfyAssets(ctx, files)
		inspection.HostState = "available"
		if err != nil {
			inspection.Complete = false
			inspection.HostState = "invalid_response"
			if errors.Is(err, updates.ErrUnavailable) {
				inspection.HostState = "unavailable"
			}
			if !a.updates.Configured() {
				inspection.HostState = "not_configured"
			}
		}
		// The client only returns Items after validating every echoed identity.
		// Partial read-only evidence is useful, but it never permits deletion.
		if len(result.Items) == len(files) && !result.CheckedAt.IsZero() {
			inspection.HostCheckedAt = &result.CheckedAt
			for i, outcome := range result.Items {
				inspection.Items[positions[i]].Status = outcome.Status
			}
		}
	}
	inspection.CheckedAt = time.Now().UTC()
	return inspection
}
