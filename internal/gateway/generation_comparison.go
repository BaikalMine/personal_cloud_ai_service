package gateway

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"ai-access-gateway/internal/domain"
)

// Comparison reads only the primary input of the owned, completed edit. It does
// not restore the editor, submit work, or expose the saved prompt and references.
func (a *App) handleGenerationComparison(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	if user == nil || user.ID <= 0 || user.Disabled || user.AccountExpiresAt.Valid && !user.AccountExpiresAt.Time.After(time.Now()) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
		return
	}
	if !user.CanUseQuickGenerationType("image-to-image") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "generation_access_denied"})
		return
	}
	id := r.URL.Query().Get("job")
	if !validGenerationRequestID(id) || len(r.URL.Query()) != 1 || len(r.URL.Query()["job"]) != 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_job_id"})
		return
	}
	job, err := a.store.GenerationJobByPublicID(r.Context(), user.ID, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comparison_unavailable"})
		return
	}
	unavailable := func() { writeJSON(w, http.StatusOK, map[string]bool{"available": false}) }
	if job.TemplateID != "image-to-image" || job.State != domain.GenerationJobCompleted {
		unavailable()
		return
	}
	values := map[string]string{}
	if len(job.PayloadCipher) > 0 {
		payload, decodeErr := a.decodeGenerationSavedPayload(job.PayloadCipher)
		if decodeErr == nil {
			values["input_image"] = payload.Values["input_image"]
		}
	}
	if values["input_image"] == "" {
		record, recordErr := a.store.GenerationExecutionForJob(r.Context(), user.ID, id)
		if recordErr != nil && !errors.Is(recordErr, sql.ErrNoRows) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comparison_unavailable"})
			return
		}
		if recordErr == nil {
			snapshot, decodeErr := a.decodeGenerationExecution(job, record)
			if decodeErr == nil {
				for _, asset := range snapshot.Assets {
					if asset.Kind == "image" && asset.Number == 1 {
						values["input_image"] = asset.InputName
						break
					}
				}
			}
		}
	}
	assets, _, err := a.generationJobSourceAssets(r.Context(), user.ID, values)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "comparison_unavailable"})
		return
	}
	if len(assets) != 1 || !assets[0].Available {
		unavailable()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "original": map[string]string{
		"url": assets[0].URL, "label": "Фото 1",
	}})
}
