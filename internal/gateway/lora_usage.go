package gateway

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

type loraUseModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Architecture string `json:"architecture,omitempty"`
	Available    bool   `json:"available"`
	Reason       string `json:"reason,omitempty"`
	UseURL       string `json:"use_url,omitempty"`
}

var errLoraUseScopeDenied = errors.New("LoRA use scope denied")

type loraUseView struct {
	JobID     string           `json:"job_id"`
	State     string           `json:"state"`
	Message   string           `json:"message"`
	CheckedAt int64            `json:"checked_at,omitempty"`
	ExpiresAt int64            `json:"expires_at,omitempty"`
	Models    []loraUseModel   `json:"models"`
	Reference string           `json:"-"`
	Preset    generationPreset `json:"-"`
}

func loraUseBlocked(id, state, message string) loraUseView {
	return loraUseView{JobID: id, State: state, Message: message, Models: []loraUseModel{}}
}

func loraUseAccess(user *User, job domain.LoraTrainingJob) (string, string) {
	if user == nil || !user.CanUseService("quick_generation") {
		return "generation_denied", "Нет доступа к генерации изображений."
	}
	if user.Role != "admin" && !user.CanUseAdvancedGenerationSettings {
		return "lora_access_denied", "Для использования LoRA нужен доступ к расширенным настройкам генерации."
	}
	template := "text-to-image"
	if job.Family == "flux2-klein" {
		template = "image-to-image"
	}
	if !user.CanUseQuickGenerationType(template) {
		return "generation_denied", "Этот тип генерации недоступен для вашей учётной записи."
	}
	if job.State != domain.LoraTrainingCompleted {
		return "not_completed", "LoRA пока не готова."
	}
	if job.Provenance == nil || job.Provenance.Artifact == nil || job.Provenance.Validate() != nil || job.AgentJobID == "" {
		return "provenance_missing", "Нет подтверждённых данных об установленном файле. LoRA можно скачать, но её совместимость не проверена."
	}
	if loratraining.TrainingArchitecture(job.Family, job.Provenance.Profile.ModelVersion) == "" {
		return "unsupported_profile", "Версия базовой модели обучения не поддерживается."
	}
	return "", ""
}

func loraCatalogReference(catalog generationModelCatalog, job domain.LoraTrainingJob) (string, bool) {
	expected, err := loratraining.ArtifactRelativeName(job.UsernameSnapshot, job.Family, job.ArtifactName)
	if err != nil {
		return "", false
	}
	groups := catalog.LoraGroups
	if job.Family == "flux2-klein" {
		groups = catalog.FluxLoraGroups
	}
	reference := ""
	count := 0
	for _, group := range groups {
		for _, lora := range group.Loras {
			name, ok := loratraining.ModelRelativeName(lora.Name)
			if ok && name == expected {
				reference = lora.Name
				count++
			}
		}
	}
	return reference, count == 1
}

func loraInspectionMatches(job domain.LoraTrainingJob, inspection loratraining.ArtifactInspection) bool {
	p := job.Provenance
	if p == nil || p.Artifact == nil || inspection.Artifact == nil {
		return false
	}
	reference, err := loratraining.ArtifactRelativeName(job.UsernameSnapshot, job.Family, job.ArtifactName)
	return err == nil && p.GatewayJobID == job.PublicID && inspection.Version == 1 && inspection.State == "verified" && inspection.AgentJobID == job.AgentJobID && inspection.GatewayJobID == job.PublicID && inspection.Owner == job.UsernameSnapshot &&
		inspection.Family == job.Family && p.Profile.Family == job.Family && inspection.ProfileSHA256 == p.ProfileSHA256 && inspection.RelativeName == reference &&
		*inspection.Artifact == *p.Artifact && inspection.Artifact.Name == job.ArtifactName && inspection.Artifact.Bytes == job.ArtifactBytes
}

func allowedLoraUseReference(catalog generationModelCatalog, policy domain.GenerationAccessPolicy, family, reference string) bool {
	if family == "krea2" {
		return loraBelongsToAllowedGroup(catalog.LoraGroups, policy.KreaLoraGroups, reference)
	}
	return family == "flux2-klein" && loraBelongsToAllowedGroup(catalog.FluxLoraGroups, policy.FluxLoraGroups, reference)
}

func (a *App) loraUseOptions(ctx context.Context, user *User, job domain.LoraTrainingJob) (loraUseView, generationModelCatalog, error) {
	catalog := generationModelCatalog{}
	if state, message := loraUseAccess(user, job); state != "" {
		return loraUseBlocked(job.PublicID, state, message), catalog, nil
	}
	snapshot, err := a.comfyObjectInfo(ctx, true)
	if err != nil || snapshot.Source == comfyObjectInfoLastKnownGood {
		return loraUseBlocked(job.PublicID, "comfy_offline", "ComfyUI недоступен. Нельзя подтвердить список моделей и LoRA."), catalog, nil
	}
	catalog = buildGenerationModelCatalog(snapshot.Info)
	catalog.ObjectInfo = snapshot
	reference, ok := loraCatalogReference(catalog, job)
	if !ok {
		return loraUseBlocked(job.PublicID, "not_installed", "Точный файл этой LoRA отсутствует в каталоге ComfyUI или найден неоднозначно."), catalog, nil
	}
	policy, err := a.generationAccessPolicy(ctx, user)
	if err != nil {
		return loraUseView{}, catalog, err
	}
	if !allowedLoraUseReference(catalog, policy, job.Family, reference) {
		return loraUseBlocked(job.PublicID, "lora_access_denied", "Группа этой LoRA недоступна для вашей учётной записи."), catalog, nil
	}
	family, presetID := modelFamilyKrea2, krea2TextWorkflowManifest().ID
	if job.Family == "flux2-klein" {
		family, presetID = modelFamilyFlux2, flux2EditWorkflowManifest().ID
	}
	var preset generationPreset
	for _, candidate := range buildGenerationPresets(catalog) {
		if candidate.ID == presetID {
			preset = candidate
			break
		}
	}
	if !preset.Available || !policyAllows(policy.PresetIDs, presetID) {
		return loraUseBlocked(job.PublicID, "workflow_unavailable", "Совместимый режим генерации недоступен."), catalog, nil
	}
	models := []generationModel{}
	names := []string{}
	for _, group := range catalog.Groups {
		for _, model := range group.Models {
			if model.Family == family && model.Available && policyAllows(policy.ModelIDs, model.ID) {
				if _, ok := loratraining.ModelRelativeName(model.Name); ok {
					models = append(models, model)
					names = append(names, model.Name)
				}
			}
		}
	}
	if len(names) == 0 {
		return loraUseBlocked(job.PublicID, "no_models", "Нет доступных моделей нужного семейства."), catalog, nil
	}
	if !(loratraining.ArtifactInspectionRequest{Models: names}).Valid() {
		return loraUseBlocked(job.PublicID, "catalog_ambiguous", "Каталог моделей слишком велик или содержит неоднозначные имена. Нужна проверка администратора."), catalog, nil
	}
	inspection, err := a.loraTraining.InspectArtifact(ctx, job.AgentJobID, names)
	if err != nil {
		return loraUseBlocked(job.PublicID, "agent_unavailable", "Не удалось проверить файлы. Проверьте доступность и версию агента обучения, затем повторите запрос."), catalog, nil
	}
	if !loraInspectionMatches(job, inspection) {
		message := "Установленный файл не совпадает с паспортом этой LoRA."
		if inspection.State == "artifact_missing" {
			message = "Установленный файл LoRA больше недоступен."
		}
		if inspection.State == "provenance_missing" {
			message = "Агент не сохранил подтверждённые данные об этой LoRA."
		}
		return loraUseBlocked(job.PublicID, "artifact_unconfirmed", message), catalog, nil
	}
	view := loraUseView{JobID: job.PublicID, State: "no_compatible_models", Message: "Нет моделей с подтверждённой совместимой архитектурой.", Reference: reference, Preset: preset, Models: []loraUseModel{}}
	checks := map[string]loratraining.ModelInspection{}
	for _, check := range inspection.Models {
		checks[check.Name] = check
	}
	for _, model := range models {
		name, _ := loratraining.ModelRelativeName(model.Name)
		check, exists := checks[name]
		item := loraUseModel{ID: model.ID, Name: model.DisplayName, Architecture: check.Architecture, Reason: "Файл модели недоступен для проверки."}
		switch {
		case exists && check.State == "compatible" && check.Architecture == loratraining.TrainingArchitecture(job.Family, job.Provenance.Profile.ModelVersion):
			// The current Flux edit recipe binds the Qwen3 8B encoder (Klein 9B).
			if family == modelFamilyFlux2 && check.Architecture != "flux2-klein-9b" {
				item.Reason = "Текущий режим Flux2 требует модель Klein 9B и энкодер Qwen3 8B."
				break
			}
			item.Available = true
			item.Reason = ""
			item.UseURL = "/generate?" + url.Values{"lora_job": {job.PublicID}, "lora_model": {model.ID}}.Encode()
			view.State = "ready"
			view.Message = "Файл LoRA совпадает с паспортом. Архитектура моделей проверена."
		case exists && check.State == "incompatible":
			item.Reason = "Архитектура не соответствует базовой модели обучения."
		case exists && check.State == "unsupported":
			item.Reason = "Структура файла модели не распознана."
		}
		view.Models = append(view.Models, item)
	}
	view.CheckedAt = time.Now().UnixMilli()
	view.ExpiresAt = time.Now().Add(time.Minute).UnixMilli()
	return view, catalog, nil
}

func loraGenerationSourceValues(job domain.LoraTrainingJob, view loraUseView, model generationModel) map[string]string {
	values := map[string]string{"template_id": view.Preset.TemplateID, "generation_workflow": view.Preset.ID, "model": model.ID, "positive_prompt": job.TriggerWord,
		"negative_prompt": "", "loras_configured": "true", "assistant_enabled": "false", "assistant_job_id": "", "batch_enabled": "false",
		"steps": strconv.Itoa(model.DefaultSteps), "cfg": strconv.FormatFloat(model.DefaultCFG, 'f', -1, 64), "sampler": model.DefaultSampler, "scheduler": model.DefaultScheduler}
	for i := 1; i <= maxGenerationLoraSlots; i++ {
		suffix := strconv.Itoa(i)
		values["lora_"+suffix] = ""
		values["lora_model_strength_"+suffix] = "0"
		values["lora_clip_strength_"+suffix] = "0"
	}
	values["lora_1"] = view.Reference
	values["lora_model_strength_1"] = "1"
	values["lora_clip_strength_1"] = "1"
	return values
}

func (a *App) handleLoraUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Метод не поддерживается."})
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Сеанс завершён. Войдите снова."})
		return
	}
	source := strings.HasPrefix(r.URL.Path, "/api/lora-training/source/")
	prefix := "/api/lora-training/usage/"
	if source {
		prefix = "/api/lora-training/source/"
	}
	id := strings.TrimPrefix(r.URL.Path, prefix)
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !loraDatasetIDPattern.MatchString(id) || (!source && len(query) != 0) || source && (len(query) != 1 || len(query["model"]) != 1 || query.Get("model") == "" || len(query.Get("model")) > 3000) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Некорректный адрес LoRA или модели."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	load := func() (*User, domain.LoraTrainingJob, error) {
		fresh, err := a.store.UserByID(ctx, user.ID)
		if err != nil {
			return nil, domain.LoraTrainingJob{}, err
		}
		if fresh.Disabled || fresh.AccountExpiresAt.Valid && !fresh.AccountExpiresAt.Time.After(time.Now()) || !taskAccessScope(&fresh).Training {
			return nil, domain.LoraTrainingJob{}, errLoraUseScopeDenied
		}
		job, err := a.store.LoraTrainingJobByPublicID(ctx, id, fresh.ID, false)
		return &fresh, job, err
	}
	fresh, job, err := load()
	writeFailure := func(err error) {
		status := http.StatusServiceUnavailable
		message := "Не удалось проверить LoRA. Повторите запрос."
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
			message = "LoRA больше недоступна."
		}
		if errors.Is(err, errLoraUseScopeDenied) {
			status = http.StatusForbidden
			message = "Доступ к обучению закрыт."
		}
		writeJSON(w, status, map[string]string{"error": message})
	}
	if err != nil {
		writeFailure(err)
		return
	}
	view, catalog, err := a.loraUseOptions(ctx, fresh, job)
	if err != nil {
		writeFailure(err)
		return
	}
	fresh, current, err := load()
	if err != nil {
		writeFailure(err)
		return
	}
	if state, message := loraUseAccess(fresh, current); state != "" {
		view = loraUseBlocked(id, state, message)
	}
	if view.Reference != "" {
		policy, err := a.generationAccessPolicy(ctx, fresh)
		if err != nil {
			writeFailure(err)
			return
		}
		if current.AgentJobID != job.AgentJobID || current.ArtifactName != job.ArtifactName || current.ArtifactBytes != job.ArtifactBytes || current.Provenance == nil || current.Provenance.Artifact == nil ||
			current.Provenance.ProfileSHA256 != job.Provenance.ProfileSHA256 || *current.Provenance.Artifact != *job.Provenance.Artifact {
			view = loraUseBlocked(id, "artifact_changed", "Данные LoRA изменились во время проверки. Повторите запрос.")
		} else if !policyAllows(policy.PresetIDs, view.Preset.ID) || !allowedLoraUseReference(catalog, policy, current.Family, view.Reference) {
			view = loraUseBlocked(id, "generation_denied", "Доступ к режиму генерации или группе LoRA изменился.")
		} else {
			filtered := view.Models[:0]
			for _, model := range view.Models {
				if policyAllows(policy.ModelIDs, model.ID) {
					filtered = append(filtered, model)
				}
			}
			view.Models = filtered
			ready := false
			for _, model := range view.Models {
				ready = ready || model.Available
			}
			if !ready {
				view.State = "no_compatible_models"
				view.Message = "Нет доступных совместимых моделей."
			}
		}
	}
	if !source {
		writeJSON(w, http.StatusOK, view)
		return
	}
	if view.State == "ready" {
		for _, candidate := range view.Models {
			if candidate.Available && candidate.ID == query.Get("model") {
				model, ok := catalog.byID[candidate.ID]
				if !ok {
					break
				}
				writeJSON(w, http.StatusOK, map[string]any{"source_kind": "lora_training", "source_id": id, "source_name": current.Name, "values": loraGenerationSourceValues(current, view, model), "requires_inputs": view.Preset.RequiresImage})
				return
			}
		}
	}
	message := view.Message
	if view.State == "ready" {
		message = "Выбранная модель больше недоступна или не совместима с этой LoRA."
	}
	writeJSON(w, http.StatusConflict, map[string]string{"error": message})
}
