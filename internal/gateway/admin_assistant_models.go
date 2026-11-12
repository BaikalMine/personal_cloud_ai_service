package gateway

import (
	"errors"
	"net/http"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type adminAssistantModel struct {
	ID, Label          string
	Granted, Available bool
}

func BuildAdminAssistantModels(catalog []domain.PromptAssistantModel, grants []string) []adminAssistantModel {
	selected := make(map[string]bool, len(grants))
	for _, id := range grants {
		selected[id] = true
	}
	rows := make([]adminAssistantModel, 0, len(catalog))
	seen := make(map[string]bool, len(catalog))
	for _, model := range catalog {
		rows = append(rows, adminAssistantModel{ID: model.ID, Label: model.Label, Granted: selected[model.ID], Available: true})
		seen[model.ID] = true
	}
	for _, id := range grants {
		if !seen[id] {
			rows = append(rows, adminAssistantModel{ID: id, Label: id, Granted: true})
		}
	}
	return rows
}

func (a *App) handleAdminAssistantModelGrant(w http.ResponseWriter, r *http.Request, actorID, userID int64) {
	values := r.PostForm
	if len(values["action"]) != 1 || len(values["assistant_model"]) != 1 || len(values["granted"]) > 1 ||
		(len(values["granted"]) == 1 && values.Get("granted") != "1") {
		http.Error(w, "Проверьте модель и разрешение.", http.StatusBadRequest)
		return
	}
	id, grant := values.Get("assistant_model"), values.Get("granted") == "1"
	_, known := a.assistantCatalogModel(id)
	if !domain.ValidPromptAssistantModelID(id) || (grant && !known) {
		http.Error(w, "Модель отсутствует в каталоге. Обновите страницу.", http.StatusBadRequest)
		return
	}
	if err := a.store.SetPromptAssistantModelGrant(r.Context(), actorID, userID, id, grant); err != nil {
		if errors.Is(err, store.ErrPromptAssistantModelAccess) {
			http.Error(w, "Доступ запрещён.", http.StatusForbidden)
		} else {
			http.Error(w, "Не удалось сохранить разрешение. Обновите страницу и проверьте его состояние.", http.StatusInternalServerError)
		}
		return
	}
	redirectAdminProfile(w, r, userID, "access", "assistant_model", "changed")
}
