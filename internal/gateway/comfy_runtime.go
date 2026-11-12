package gateway

import (
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"
)

func (a *App) handleAdminComfyRuntime(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		writeJSON(w, 403, map[string]string{"error": "access_denied"})
		return
	}
	if r.Method == http.MethodGet {
		values, queryErr := url.ParseQuery(r.URL.RawQuery)
		id := values.Get("id")
		if queryErr != nil || len(values) > 1 || len(values["id"]) > 1 || id != "" && !updates.ComfyStorageHex(id, 16) || len(values) == 1 && id == "" {
			writeJSON(w, 400, map[string]string{"error": "invalid_request"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		result, err := a.updates.ComfyRuntime(ctx, nil, id)
		result.CommandUntil = time.Now().UTC().Add(25 * time.Second)
		status := 200
		if err != nil {
			status = 503
			if errors.Is(err, updates.ErrComfyRuntimeNotFound) {
				status = 404
			}
		}
		writeJSON(w, status, result)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
		return
	}
	if a.csrfSigner == nil || !a.validCSRFValue(r, r.Header.Get("X-CSRF-Token")) {
		writeJSON(w, 403, map[string]string{"error": "csrf_failed"})
		return
	}
	if a.cfg.RestoreDrillMode || a.store == nil || a.gpuCoordinator == nil {
		writeJSON(w, 409, map[string]string{"error": "control_unavailable"})
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" || r.URL.RawQuery != "" {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	var input struct {
		ID        string    `json:"id"`
		Action    string    `json:"action"`
		Version   string    `json:"version"`
		Force     bool      `json:"force"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	now := time.Now().UTC()
	request := updates.ComfyRuntimeRequest{ID: input.ID, Action: input.Action, Version: input.Version, Force: input.Force, ExpiresAt: input.ExpiresAt}
	if !request.Valid() || !input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(25*time.Second)) {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	if request.ExpiresAt.After(now.Add(20 * time.Second)) {
		request.ExpiresAt = now.Add(20 * time.Second)
	}
	// Browser disconnection does not abort accepted process control. Admission
	// remains closed through the bounded agent command's mutation deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	var result updates.ComfyRuntimeStatus
	sent := false
	err = a.store.WithGPUAdmissionGuard(ctx, func() error {
		return a.store.WithComfyRuntimeGuard(ctx, input.Action != "start", func() error {
			busy, err := a.store.ComfyRuntimeBlocked(ctx)
			if err != nil {
				return err
			}
			if busy {
				return store.ErrGPUWorkConflict
			}
			sent = true
			result, err = a.updates.ComfyRuntime(ctx, &request, "")
			if err != nil && result.Receipt == nil && result.Error == "" {
				// No replay: wait out the mutation deadline while keeping both guards.
				if delay := time.Until(request.ExpiresAt.Add(time.Second)); delay > 0 {
					timer := time.NewTimer(delay)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-ctx.Done():
					}
				}
				read, readErr := a.updates.ComfyRuntime(ctx, nil, request.ID)
				if readErr == nil {
					result = read
					return nil
				}
			}
			return err
		})
	})
	_ = a.store.RecordAudit(ctx, domain.AuditEvent{ActorUserID: &actor.ID, Action: "comfyui.runtime." + input.Action, TargetType: "service", Metadata: map[string]any{"command_id": input.ID, "force": input.Force, "sent": sent, "confirmed": err == nil, "receipt": result.Receipt}})
	result.CommandUntil = time.Now().UTC().Add(25 * time.Second)
	if err != nil {
		if !sent {
			if errors.Is(err, store.ErrModelInstallConflict) || errors.Is(err, store.ErrGPUWorkConflict) {
				writeJSON(w, 409, map[string]string{"error": "active_jobs"})
			} else {
				writeJSON(w, 503, map[string]string{"error": "control_unavailable"})
			}
			return
		}
		if result.Error == "" {
			result.Error = "command_unconfirmed"
		}
		writeJSON(w, 503, result)
		return
	}
	writeJSON(w, 200, result)
}
