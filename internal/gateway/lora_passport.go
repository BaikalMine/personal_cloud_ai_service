package gateway

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

type loraPassportParameters struct {
	domain.LoraExecutionParameters
	// Historical seeds can exceed JavaScript's exact integer range.
	Seed string `json:"seed"`
}

type loraPassportView struct {
	RepeatSource      *loraRepeatSourceView          `json:"repeat_source,omitempty"`
	Version           int                            `json:"version"`
	JobID             string                         `json:"job_id"`
	Name              string                         `json:"name"`
	Family            string                         `json:"family"`
	Trigger           string                         `json:"trigger"`
	State             domain.LoraTrainingState       `json:"state"`
	EvidenceState     string                         `json:"evidence_state"`
	Requested         loraPassportParameters         `json:"requested_parameters"`
	SnapshotID        string                         `json:"dataset_snapshot_id,omitempty"`
	SnapshotHash      string                         `json:"dataset_snapshot_hash,omitempty"`
	SnapshotAvailable bool                           `json:"dataset_snapshot_available"`
	Execution         *domain.LoraTrainingProvenance `json:"execution,omitempty"`
}

type loraRepeatSourceView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

func loraPassport(job domain.LoraTrainingJob) loraPassportView {
	view := loraPassportView{Version: 1, JobID: job.PublicID, Name: job.Name, Family: job.Family,
		Trigger: job.TriggerWord, State: job.State, EvidenceState: "not_recorded",
		SnapshotID: job.DatasetSnapshotID, SnapshotHash: job.DatasetSnapshotHash,
		Requested: loraPassportParameters{Seed: strconv.FormatInt(job.Seed, 10), LoraExecutionParameters: domain.LoraExecutionParameters{Resolution: job.Resolution, MaxSteps: job.MaxTrainSteps,
			NetworkDim: job.NetworkDim, NetworkAlpha: job.NetworkAlpha, LearningRate: job.LearningRate,
			SampleCount: job.SampleCount}}, Execution: job.Provenance.Clone()}
	if view.Execution != nil {
		view.EvidenceState = "inputs_recorded"
		if view.Execution.Artifact != nil {
			view.EvidenceState = "artifact_recorded"
		}
	}
	return view
}

func (a *App) handleLoraTrainingPassport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	user := a.currentUser(r)
	if user == nil || user.ID <= 0 || user.Disabled || user.AccountExpiresAt.Valid && !user.AccountExpiresAt.Time.After(time.Now()) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
		return
	}
	scope := taskAccessScope(user)
	if !scope.Training {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "training_access_denied"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/lora-training/passports/")
	if id == "" || len(id) > 96 || strings.IndexFunc(id, func(ch rune) bool {
		return !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_')
	}) >= 0 || r.URL.RawQuery != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_passport_id"})
		return
	}
	// Like task details, the public passport is owner-scoped even for an admin.
	job, err := a.store.LoraTrainingJobByPublicID(r.Context(), id, scope.UserID, false)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "passport_not_found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "passport_unavailable"})
		return
	}
	view := loraPassport(job)
	if receipt, err := a.store.LoraTrainingRepeatForJob(r.Context(), job.ID); err == nil {
		view.RepeatSource = &loraRepeatSourceView{ID: receipt.SourcePublicID, Name: receipt.SourceName, Available: receipt.SourceJobID != nil}
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "passport_unavailable"})
		return
	}
	if job.DatasetSnapshotID != "" {
		snapshot, err := a.store.LoraDatasetSnapshot(r.Context(), scope.UserID, job.DatasetSnapshotID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "passport_unavailable"})
			return
		}
		view.SnapshotAvailable = err == nil && snapshot.Hash == job.DatasetSnapshotHash
	}
	writeJSON(w, http.StatusOK, view)
}
