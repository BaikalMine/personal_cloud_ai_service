package gateway

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type taskGPUView struct {
	Kind string                `json:"kind"`
	ID   string                `json:"id"`
	GPU  *domain.TaskGPUStatus `json:"gpu"`
}

func parseTaskGPUKeys(query url.Values) ([]domain.TaskGPUKey, error) {
	if len(query) != 1 || len(query["task"]) == 0 || len(query["task"]) > domain.TaskGPUSnapshotLimit {
		return nil, store.ErrTaskQuery
	}
	keys := make([]domain.TaskGPUKey, 0, len(query["task"]))
	seen := make(map[domain.TaskGPUKey]bool)
	for _, value := range query["task"] {
		kind, id, found := strings.Cut(value, ":")
		if !found || len(id) == 0 || len(id) > 200 ||
			(kind != "generation" && kind != "lora_training" && kind != "lora_caption") {
			return nil, store.ErrTaskQuery
		}
		for _, ch := range id {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return nil, store.ErrTaskQuery
			}
		}
		key := domain.TaskGPUKey{Kind: kind, ID: id}
		if seen[key] {
			return nil, store.ErrTaskQuery
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys, nil
}

func (a *App) handleTaskGPUSnapshots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	scope := taskAccessScope(a.currentUser(r))
	if scope.UserID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
		return
	}
	if !scope.Generation && !scope.Training {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "task_access_denied"})
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_task_query"})
		return
	}
	keys, err := parseTaskGPUKeys(query)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_task_query"})
		return
	}
	snapshot, err := a.store.TaskGPUSnapshots(r.Context(), scope, keys)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "task_gpu_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// GPU changes and time-based expiry need not change the owner's task revision.
// This read avoids repeatedly decrypting and rebuilding the selected detail.
func (a *App) handleTaskGPU(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeGenerationError(w, http.StatusMethodNotAllowed, "Метод не поддерживается.")
		return
	}
	scope := taskAccessScope(a.currentUser(r))
	if scope.UserID <= 0 {
		writeGenerationError(w, http.StatusUnauthorized, "Требуется вход.")
		return
	}
	kind, id, valid := taskDetailIdentity(r)
	if !valid {
		writeGenerationError(w, http.StatusBadRequest, "Некорректный адрес задачи.")
		return
	}
	status, err := a.store.TaskGPUStatus(r.Context(), scope, kind, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeGenerationError(w, http.StatusNotFound, "Задача больше не доступна.")
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "Не удалось проверить состояние GPU. Попробуйте ещё раз.")
		return
	}
	writeJSON(w, http.StatusOK, taskGPUView{Kind: kind, ID: id, GPU: status})
}
