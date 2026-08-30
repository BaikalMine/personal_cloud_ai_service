package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const maxGenerationRecipePayload = 32 << 10

type generationSavedPayload struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

type generationRecipeView struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	TemplateID string            `json:"template_id"`
	WorkflowID string            `json:"workflow_id"`
	Values     map[string]string `json:"values"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type generationVariantView struct {
	ID              int64                 `json:"id"`
	PromptID        string                `json:"prompt_id"`
	TemplateID      string                `json:"template_id"`
	WorkflowID      string                `json:"workflow_id"`
	ModelName       string                `json:"model_name"`
	Seed            int64                 `json:"seed"`
	State           string                `json:"state"`
	Values          map[string]string     `json:"values"`
	CreatedAt       time.Time             `json:"created_at"`
	FinishedAt      *time.Time            `json:"finished_at,omitempty"`
	DurationSeconds int64                 `json:"duration_seconds"`
	ErrorMessage    string                `json:"error_message,omitempty"`
	Media           []generationMediaView `json:"media"`
}

func (a *App) registerGenerationCompanionRoutes(mux *http.ServeMux, quick func(http.Handler) http.Handler) {
	mux.Handle("/generate/preflight", quick(http.HandlerFunc(a.handleGenerationPreflight)))
	mux.Handle("/generate/recipes", quick(http.HandlerFunc(a.handleGenerationRecipes)))
	mux.Handle("/generate/recipes/delete", quick(http.HandlerFunc(a.handleDeleteGenerationRecipe)))
	mux.Handle("/generate/variants", quick(http.HandlerFunc(a.handleGenerationVariants)))
}

func (a *App) handleGenerationPreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRequest)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	input, err := parseGenerationForm(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "checks": []map[string]string{{"level": "error", "label": "Параметры", "message": err.Error()}}, "recovery_profile": "balanced", "recovery_label": "Вернуть безопасный профиль"})
		return
	}
	preparation, err := a.prepareGeneration(r.Context(), a.currentUser(r), input, false)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "checks": []map[string]string{{"level": "error", "label": "Готовность workflow", "message": err.Error()}}, "recovery_profile": "balanced", "recovery_label": "Вернуть безопасный профиль"})
		return
	}
	checks := []map[string]string{
		{"level": "ok", "label": "Модель", "message": preparation.Model.DisplayName + " доступна"},
		{"level": "ok", "label": "Workflow и LoRA", "message": "узлы, зависимости и выбранные адаптеры проверены"},
		{"level": "ok", "label": "Параметры", "message": "Gateway собрал и проверил граф выбранной схемы"},
	}
	if input.Seed < 0 {
		checks = append(checks, map[string]string{"level": "info", "label": "Seed", "message": "будет выбран новый случайный seed при запуске"})
	} else {
		checks = append(checks, map[string]string{"level": "info", "label": "Seed", "message": "будет использован seed " + strconv.FormatInt(input.Seed, 10)})
	}
	if report, reportErr := a.generationVRAMReport(r.Context(), preparation.Model.Family); reportErr == nil {
		checks = append(checks, report)
	} else {
		checks = append(checks, map[string]string{"level": "info", "label": "VRAM", "message": "ComfyUI не отдал текущую статистику VRAM; окончательную загрузку проверит сам ComfyUI"})
	}
	queue, _ := a.generationQueueOverview(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "checks": checks, "queue": queue})
}

func (a *App) generationVRAMReport(ctx context.Context, family string) (map[string]string, error) {
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/system_stats")
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		req.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 4 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("system_stats returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 512<<10))
	if err != nil {
		return nil, err
	}
	var stats struct {
		Devices []struct {
			VRAMTotal float64 `json:"vram_total"`
			VRAMFree  float64 `json:"vram_free"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(body, &stats); err != nil || len(stats.Devices) == 0 || stats.Devices[0].VRAMTotal <= 0 {
		return nil, errors.New("unknown vram stats")
	}
	freeGB := stats.Devices[0].VRAMFree / (1024 * 1024 * 1024)
	totalGB := stats.Devices[0].VRAMTotal / (1024 * 1024 * 1024)
	recommendedGB := map[string]float64{modelFamilyCheckpoint: 3, modelFamilyKrea2: 10, modelFamilyFlux2: 16, modelFamilyMiniMaxH3: 20}[family]
	if recommendedGB == 0 {
		recommendedGB = 6
	}
	message := fmt.Sprintf("свободно %.1f из %.1f ГБ; для этой схемы рекомендуется около %.0f ГБ", freeGB, totalGB, recommendedGB)
	if freeGB < recommendedGB {
		return map[string]string{"level": "warning", "label": "VRAM", "message": message + ". Перед запуском закройте тяжёлые задачи или дождитесь освобождения памяти."}, nil
	}
	return map[string]string{"level": "ok", "label": "VRAM", "message": message}, nil
}

func (a *App) handleGenerationRecipes(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	switch r.Method {
	case http.MethodGet:
		items, err := a.generationRecipeViews(r.Context(), user.ID)
		if err != nil {
			writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить сохранённые наборы")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"recipes": items})
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRecipePayload)
		if !a.validCSRF(r) {
			writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeGenerationError(w, http.StatusBadRequest, "не удалось прочитать набор параметров")
			return
		}
		name := strings.TrimSpace(r.Form.Get("recipe_name"))
		if len([]rune(name)) < 2 || len([]rune(name)) > 80 {
			writeGenerationError(w, http.StatusBadRequest, "название набора должно содержать от 2 до 80 символов")
			return
		}
		payload, err := encodeGenerationSavedPayload(generationSavedPayload{Version: 1, Values: generationRecipeValues(r.Form, -1)})
		if err != nil {
			writeGenerationError(w, http.StatusBadRequest, err.Error())
			return
		}
		cipher, err := a.contentCipher.Encrypt(string(payload))
		if err != nil {
			writeGenerationError(w, http.StatusInternalServerError, "не удалось защитить сохранённый набор")
			return
		}
		row, err := a.store.SaveGenerationRecipe(r.Context(), user.ID, name, r.Form.Get("template_id"), r.Form.Get("generation_workflow"), cipher)
		if err != nil {
			writeGenerationError(w, http.StatusBadRequest, err.Error())
			return
		}
		view, err := a.generationRecipeView(row)
		if err != nil {
			writeGenerationError(w, http.StatusInternalServerError, "не удалось прочитать сохранённый набор")
			return
		}
		a.audit(r.Context(), &user.ID, "quick_generation_recipe_saved", "quick_generation_recipe", &row.ID, a.clientIP(r), r.UserAgent(), map[string]any{"name": name})
		writeJSON(w, http.StatusCreated, map[string]any{"recipe": view})
	default:
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleDeleteGenerationRecipe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeGenerationError(w, http.StatusBadRequest, "не удалось прочитать запрос")
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.Form.Get("recipe_id")), 10, 64)
	if err != nil || id <= 0 {
		writeGenerationError(w, http.StatusBadRequest, "некорректный набор")
		return
	}
	user := a.currentUser(r)
	deleted, err := a.store.DeleteGenerationRecipe(r.Context(), user.ID, id)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось удалить набор")
		return
	}
	if !deleted {
		http.NotFound(w, r)
		return
	}
	a.audit(r.Context(), &user.ID, "quick_generation_recipe_deleted", "quick_generation_recipe", &id, a.clientIP(r), r.UserAgent(), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (a *App) handleGenerationVariants(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	items, err := a.generationVariantViews(r.Context(), user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить историю вариантов")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"variants": items})
}

func (a *App) rememberGenerationVariant(ctx context.Context, userID int64, promptID string, input generationForm, values url.Values) {
	if a.contentCipher == nil || a.store == nil {
		return
	}
	payload, err := encodeGenerationSavedPayload(generationSavedPayload{Version: 1, Values: generationRecipeValues(values, input.Seed)})
	if err != nil {
		logGenerationCompanionError("encode generation variant", err)
		return
	}
	cipher, err := a.contentCipher.Encrypt(string(payload))
	if err != nil {
		logGenerationCompanionError("encrypt generation variant", err)
		return
	}
	if err := a.store.InsertGenerationVariant(ctx, userID, promptID, input.TemplateID, input.PresetID, input.ModelName, input.Seed, cipher); err != nil {
		logGenerationCompanionError("store generation variant", err)
	}
}

func (a *App) generationRecipeViews(ctx context.Context, userID int64) ([]generationRecipeView, error) {
	rows, err := a.store.ListGenerationRecipes(ctx, userID)
	if err != nil {
		return nil, err
	}
	views := make([]generationRecipeView, 0, len(rows))
	for _, row := range rows {
		view, err := a.generationRecipeView(row)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (a *App) generationRecipeView(row domain.GenerationRecipeRow) (generationRecipeView, error) {
	payload, err := a.decodeGenerationSavedPayload(row.PayloadCipher)
	if err != nil {
		return generationRecipeView{}, err
	}
	return generationRecipeView{ID: row.ID, Name: row.Name, TemplateID: row.TemplateID, WorkflowID: row.WorkflowID, Values: payload.Values, UpdatedAt: row.UpdatedAt}, nil
}

func (a *App) generationVariantViews(ctx context.Context, userID int64) ([]generationVariantView, error) {
	rows, err := a.store.ListGenerationVariants(ctx, userID, 60)
	if err != nil {
		return nil, err
	}
	views := make([]generationVariantView, 0, len(rows))
	for _, row := range rows {
		payload, err := a.decodeGenerationSavedPayload(row.PayloadCipher)
		if err != nil {
			return nil, err
		}
		media, err := a.store.ListGenerationVariantMedia(ctx, userID, row.PromptID)
		if err != nil {
			return nil, err
		}
		view := generationVariantView{ID: row.ID, PromptID: row.PromptID, TemplateID: row.TemplateID, WorkflowID: row.WorkflowID, ModelName: row.ModelName, Seed: row.Seed, State: row.State, Values: payload.Values, CreatedAt: row.CreatedAt, FinishedAt: row.FinishedAt, ErrorMessage: row.ErrorMessage}
		if row.FinishedAt != nil {
			view.DurationSeconds = int64(row.FinishedAt.Sub(row.CreatedAt).Seconds())
		} else {
			view.DurationSeconds = int64(time.Since(row.CreatedAt).Seconds())
		}
		for _, item := range media {
			view.Media = append(view.Media, generationMediaView{ID: item.ID, URL: "/generate/library/" + strconv.FormatInt(item.ID, 10), Filename: item.Filename, MediaType: item.MediaType, ExpiresUnix: item.ExpiresAt.UnixMilli(), Sensitive: item.Sensitive})
		}
		views = append(views, view)
	}
	return views, nil
}

func (a *App) decodeGenerationSavedPayload(ciphertext []byte) (generationSavedPayload, error) {
	if a.contentCipher == nil {
		return generationSavedPayload{}, errors.New("шифрование сохранённых параметров недоступно")
	}
	plain, err := a.contentCipher.Decrypt(ciphertext)
	if err != nil {
		return generationSavedPayload{}, err
	}
	var payload generationSavedPayload
	if err := json.Unmarshal([]byte(plain), &payload); err != nil || payload.Version != 1 || payload.Values == nil {
		return generationSavedPayload{}, errors.New("сохранённый набор имеет неизвестный формат")
	}
	return payload, nil
}

func encodeGenerationSavedPayload(payload generationSavedPayload) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxGenerationRecipePayload {
		return nil, errors.New("набор параметров слишком большой")
	}
	return encoded, nil
}

func generationRecipeValues(form url.Values, resolvedSeed int64) map[string]string {
	values := make(map[string]string)
	for name, raw := range form {
		if !allowedGenerationRecipeField(name) || len(raw) == 0 {
			continue
		}
		value := strings.TrimSpace(raw[0])
		if len(value) > 4000 {
			continue
		}
		values[name] = value
	}
	if resolvedSeed >= 0 {
		values["seed"] = strconv.FormatInt(resolvedSeed, 10)
	}
	return values
}

func allowedGenerationRecipeField(name string) bool {
	if strings.HasPrefix(name, "input_image") || strings.HasPrefix(name, "assistant_") || name == "csrf" || name == "client_request_id" || name == "recipe_name" {
		return false
	}
	if strings.HasPrefix(name, "lora_") || strings.HasPrefix(name, "lora_model_strength_") || strings.HasPrefix(name, "lora_clip_strength_") {
		return true
	}
	switch name {
	case "template_id", "generation_workflow", "model", "positive_prompt", "negative_prompt", "width", "height", "steps", "cfg", "denoise", "sampler", "scheduler", "seed", "video_mode", "video_resolution", "video_aspect", "video_use_source_aspect", "video_swap_dimensions", "video_resize_method", "video_proportion", "video_crop_location", "video_pad_color", "video_quality", "video_duration_seconds", "video_reference_size", "video_reference_start", "video_reference_duration", "video_reference_audio", "video_filename", "video_steps", "aspect_ratio", "output_megapixels", "dimension_multiple", "max_longest_side", "base_megapixels", "loras_configured", "upscale_steps", "upscale_denoise", "upscale_auto_denoise", "upscale_sampler", "detail_steps", "detail_denoise", "detail_cfg", "detail_sampler", "detail_scheduler", "color_transfer", "color_method", "color_mode", "color_strength", "source_megapixels", "preserve_original_size", "edit_use_custom_size", "edit_aspect_preset", "edit_swap_dimensions", "edit_resize_method", "edit_proportion", "edit_crop_location", "edit_pad_color", "reference_boost", "grounding_pixels", "upscale_factor", "flux_guidance", "flux_detailer_steps", "flux_active_scale", "flux_token_whiten", "flux_norm_equalize", "flux_upscale_mode", "upscale_cfg", "upscale_scheduler", "post_denoise_blur", "post_denoise_edge", "post_denoise_radius", "post_denoise_strength", "skin_preset", "skin_strength", "skin_coolness", "skin_brightness", "skin_rosy", "skin_evenness", "skin_shadow_lift", "skin_smooth", "skin_texture_preserve", "skin_saturation", "skin_highlight_protect", "skin_mask_sensitivity", "skin_mask_feather", "adjust_hue", "adjust_saturation", "adjust_brightness", "adjust_contrast", "adjust_sharpness", "lut_name", "lut_strength", "lut_enabled":
		return true
	default:
		return false
	}
}

func logGenerationCompanionError(operation string, err error) {
	if err != nil {
		log.Printf("%s: %v", operation, err)
	}
}
