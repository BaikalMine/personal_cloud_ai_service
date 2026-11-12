package gateway

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"ai-access-gateway/internal/domain"
)

type galleryDestinationPreset struct {
	Key        string `json:"key"`
	ID         string `json:"id"`
	ModelID    string `json:"model_id"`
	Frames     bool   `json:"frames"`
	TemplateID string `json:"template_id"`
	Name       string `json:"name"`
	Maximum    int    `json:"maximum"`
}

func galleryDestinationPresets(presets []generationPreset, user *domain.User) []galleryDestinationPreset {
	result := []galleryDestinationPreset{}
	for _, preset := range presets {
		if !preset.Available || preset.Restricted || !preset.AllowsImages || preset.MaxInputImages <= 0 || preset.AdminOnly && user.Role != "admin" || !user.CanUseQuickGenerationType(preset.TemplateID) {
			continue
		}
		if preset.TemplateID != "image-to-image" && preset.TemplateID != "minimax-h3-video" {
			continue
		}
		result = append(result, galleryDestinationPreset{Key: preset.ID, ID: preset.ID, TemplateID: preset.TemplateID, Name: preset.Name, Maximum: preset.MaxInputImages, Frames: true})
	}
	return result
}

func galleryCompatibleDestinations(catalog generationModelCatalog, policy domain.GenerationAccessPolicy, user *User) []galleryDestinationPreset {
	definitions := filterGenerationPresets(buildGenerationPresets(catalog), policy)
	allowed := galleryDestinationPresets(definitions, user)
	models := filterGenerationModels(quickGenerationModels(catalog), policy)
	result := []galleryDestinationPreset{}
	for _, destination := range allowed {
		var family string
		for _, definition := range definitions {
			if definition.ID == destination.ID {
				family = definition.Family
				break
			}
		}
		for _, model := range models {
			if destination.ID == aiHubWorkflowID && !model.AIHubOnly || destination.ID != aiHubWorkflowID && model.AIHubOnly {
				continue
			}
			if !model.Available || model.Family != family || destination.TemplateID == "image-to-image" && !model.SupportsImage {
				continue
			}
			item := destination
			item.ModelID = model.ID
			item.Key = destination.ID + ":" + model.ID
			item.Name = model.DisplayName
			if item.Name == "" {
				item.Name = generationModelLabel(model.Name)
			}
			item.Frames = !model.VideoReferenceOnly
			result = append(result, item)
		}
	}
	return result
}

func (a *App) handleGalleryDestinations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	if user == nil || a.store == nil {
		http.Error(w, "недоступно", http.StatusServiceUnavailable)
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("media"), 10, 64)
	if err != nil || id <= 0 || len(r.URL.Query()) != 1 || len(r.URL.Query()["media"]) != 1 {
		writeGenerationError(w, http.StatusBadRequest, "Некорректный результат.")
		return
	}
	media, err := a.store.UserGenerationMediaByID(r.Context(), user.ID, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && media.MediaType != "image" {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusServiceUnavailable, "Не удалось проверить результат.")
		return
	}
	policy, err := a.generationAccessPolicy(r.Context(), user)
	if err != nil {
		writeGenerationError(w, http.StatusServiceUnavailable, "Не удалось проверить доступные модели.")
		return
	}
	catalog := a.comfyGenerationModels(r.Context())
	presets := galleryCompatibleDestinations(catalog, policy, user)
	if media.SizeBytes <= 0 || media.SizeBytes > maxComfyUploadBody-(1<<20) {
		presets = []galleryDestinationPreset{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"media_id": strconv.FormatInt(id, 10), "presets": presets,
		"can_dataset":    (user.Role == "admin" || user.CanTrainImageLora) && media.SizeBytes > 0 && media.SizeBytes <= maxLoraTrainingImageBytes,
		"catalog_online": catalog.Online})
}
