package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

const (
	maxGenerationRequest = 32 << 10
	maxComfyObjectInfo   = 32 << 20
	maxGenerationHistory = 32 << 20
)

type generationOutput struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	URL       string `json:"url"`
}

type generationMediaView struct {
	ID          int64  `json:"id"`
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	MediaType   string `json:"media_type"`
	ExpiresUnix int64  `json:"expires_unix"`
	Sensitive   bool   `json:"sensitive"`
}

type generationStatus struct {
	PromptID      string             `json:"prompt_id"`
	State         string             `json:"state"`
	Message       string             `json:"message"`
	QueuePosition int                `json:"queue_position,omitempty"`
	QueueTotal    int                `json:"queue_total,omitempty"`
	Outputs       []generationOutput `json:"outputs,omitempty"`
}

type generationQueueOverview struct {
	Running int `json:"running"`
	Pending int `json:"pending"`
}

type comfyQueueSnapshot struct {
	Running []json.RawMessage `json:"queue_running"`
	Pending []json.RawMessage `json:"queue_pending"`
}

func (a *App) registerGenerationRoutes(mux *http.ServeMux) {
	quick := func(next http.Handler) http.Handler {
		return a.requireAuth(a.requireServiceAccess("quick_generation", next))
	}
	page := quick(http.HandlerFunc(a.handleGeneratePage))
	run := quick(http.HandlerFunc(a.handleGenerateRun))
	recover := quick(http.HandlerFunc(a.handleRecoverGeneration))
	promptAssistant := quick(http.HandlerFunc(a.handlePromptAssistant))
	status := quick(http.HandlerFunc(a.handleGenerateStatus))
	cancel := quick(http.HandlerFunc(a.handleCancelGeneration))
	queue := quick(http.HandlerFunc(a.handleGenerationQueue))
	output := quick(http.HandlerFunc(a.handleGenerationOutput))
	library := quick(http.HandlerFunc(a.handleGenerationLibraryMedia))
	recentLibrary := quick(http.HandlerFunc(a.handleRecentGenerationLibrary))
	hideLibrary := quick(http.HandlerFunc(a.handleHideGenerationLibraryMedia))
	upload := quick(a.quickGenerationUploadHandler())
	mux.Handle("/generate", page)
	mux.Handle("/generate/", page)
	mux.Handle("/generate/upload/image", upload)
	mux.Handle("/generate/run", run)
	mux.Handle("/generate/recover", recover)
	mux.Handle("/generate/prompt-assistant", promptAssistant)
	mux.Handle("/generate/status", status)
	mux.Handle("/generate/cancel", cancel)
	mux.Handle("/generate/queue", queue)
	mux.Handle("/generate/output", output)
	mux.Handle("/generate/library/hide", hideLibrary)
	mux.Handle("/generate/library/recent", recentLibrary)
	mux.Handle("/generate/library/", library)
}

func (a *App) handleGeneratePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/generate" && r.URL.Path != "/generate/" {
		http.NotFound(w, r)
		return
	}
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		http.Error(w, "не удалось загрузить шаблоны генерации", http.StatusInternalServerError)
		return
	}
	catalog := a.comfyGenerationModels(r.Context())
	presets := buildGenerationPresets(catalog)
	user := a.currentUser(r)
	for index := range presets {
		if presets[index].AdminOnly && user.Role != "admin" {
			presets[index].Restricted = true
			presets[index].Restriction = "Доступно только администратору"
		}
	}
	views := make([]workflowView, 0, len(definitions))
	for _, definition := range definitions {
		if definition.ID != "text-to-image" && definition.ID != "image-to-image" && definition.ID != "minimax-h3-video" {
			continue
		}
		if !user.CanUseQuickGenerationType(definition.ID) {
			continue
		}
		view := workflowView{
			ID: definition.ID, Name: definition.Name, Description: definition.Description,
			RequiresImage: definition.RequiresImage, AllowsImages: definition.AllowsImages,
		}
		if definition.AdminOnly && user.Role != "admin" {
			view.Restricted = true
			view.Restriction = "Доступно только администратору"
		}
		views = append(views, view)
	}
	a.classifyPendingSensitiveContent(r.Context())
	a.queueSensitiveMediaClassification()
	recentMedia := a.recentGenerationMedia(r.Context(), user.ID)
	a.render(w, r, "generate", map[string]any{
		"Title":                 "Быстрая генерация",
		"Workflows":             views,
		"ModelGroups":           catalog.Groups,
		"GenerationPresets":     presets,
		"QuickModels":           quickGenerationModels(catalog),
		"LoraGroups":            catalog.LoraGroups,
		"FluxLoraGroups":        catalog.FluxLoraGroups,
		"ComfyOnline":           catalog.Online,
		"ModelsAvailable":       catalog.AvailableCount > 0,
		"SelectedWorkflow":      r.URL.Query().Get("workflow"),
		"RecentGenerationMedia": recentMedia,
	})
}

func (a *App) recentGenerationMedia(ctx context.Context, userID int64) []generationMediaView {
	if a.store == nil {
		return nil
	}
	items, err := a.store.ListUserGenerationMedia(ctx, userID, 24)
	if err != nil {
		log.Printf("list user generation media: %v", err)
		return nil
	}
	views := make([]generationMediaView, 0, len(items))
	for _, item := range items {
		views = append(views, generationMediaView{
			ID: item.ID, URL: "/generate/library/" + strconv.FormatInt(item.ID, 10), Filename: item.OriginalName,
			MediaType: item.MediaType, ExpiresUnix: item.ExpiresAt.UnixMilli(), Sensitive: item.Sensitive,
		})
	}
	return views
}

func (a *App) handleGenerateRun(w http.ResponseWriter, r *http.Request) {
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
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить шаблоны генерации")
		return
	}
	if input.TemplateID != "text-to-image" && input.TemplateID != "image-to-image" && input.TemplateID != "minimax-h3-video" {
		writeGenerationError(w, http.StatusBadRequest, "неизвестный шаблон workflow")
		return
	}
	definition, ok := findWorkflow(definitions, input.TemplateID)
	if !ok {
		writeGenerationError(w, http.StatusBadRequest, "неизвестный шаблон workflow")
		return
	}
	user := a.currentUser(r)
	if !user.CanUseQuickGenerationType(input.TemplateID) {
		writeGenerationError(w, http.StatusForbidden, "этот тип быстрой генерации отключён администратором")
		return
	}
	if definition.AdminOnly && user.Role != "admin" {
		writeGenerationError(w, http.StatusForbidden, "этот сценарий доступен только администратору")
		return
	}
	catalog := a.comfyGenerationModels(r.Context())
	presets := buildGenerationPresets(catalog)
	preset, ok := findGenerationPreset(presets, input.PresetID, input.TemplateID)
	if !ok {
		writeGenerationError(w, http.StatusBadRequest, "выбранный workflow больше не доступен")
		return
	}
	model, ok := catalog.byID[preset.ModelID]
	if input.ModelID != "" {
		model, ok = catalog.byID[input.ModelID]
	}
	if !ok {
		writeGenerationError(w, http.StatusBadRequest, "модель, привязанная к workflow, больше не доступна в ComfyUI")
		return
	}
	if model.Family != preset.Family {
		writeGenerationError(w, http.StatusBadRequest, "эта модель не разрешена для выбранного workflow")
		return
	}
	if !model.Available {
		writeGenerationError(w, http.StatusBadRequest, model.Reason)
		return
	}
	if definition.RequiresImage && !model.SupportsImage {
		writeGenerationError(w, http.StatusBadRequest, "выбранная модель пока не подготовлена для редактирования фото")
		return
	}
	input.ModelName = model.Name
	input.ModelFamily = model.Family
	input.TextEncoder = model.TextEncoder
	input.VAE = model.VAE
	input.AudioVAE = model.AudioVAE
	input.ReferenceModel = model.ReferenceModel
	input.Lora = model.Lora
	input.IdentityLora = model.IdentityLora
	if model.Family == modelFamilyKrea2 {
		if input.TemplateID == "image-to-image" {
			if input.IdentityLora == "" {
				writeGenerationError(w, http.StatusBadRequest, "не установлена обязательная LoRA Krea2 для сохранения внешности")
				return
			}
			input.LoraNames = [maxGenerationLoraSlots]string{}
			input.LoraModel = [maxGenerationLoraSlots]float64{}
			input.LoraClip = [maxGenerationLoraSlots]float64{}
		} else if !input.LorasConfigured {
			input.LoraNames[0] = model.Lora
			input.LoraModel[0] = model.LoraStrength
			input.LoraClip[0] = 1
		} else {
			for _, name := range input.LoraNames {
				if name != "" && !generationLoraAllowed(catalog.LoraGroups, name) {
					writeGenerationError(w, http.StatusBadRequest, "выбранная LoRA недоступна для PhotoFlow Krea2")
					return
				}
			}
		}
	} else if model.Family == modelFamilyFlux2 {
		for _, name := range input.LoraNames {
			if name != "" && !generationLoraAllowed(catalog.FluxLoraGroups, name) {
				writeGenerationError(w, http.StatusBadRequest, "выбранная LoRA недоступна для Flux2")
				return
			}
		}
	} else {
		input.LoraNames = [maxGenerationLoraSlots]string{}
		input.LoraModel = [maxGenerationLoraSlots]float64{}
		input.LoraClip = [maxGenerationLoraSlots]float64{}
	}
	if model.Family != modelFamilyCheckpoint && definition.Builder == "" {
		definition, ok = findWorkflow(definitions, input.TemplateID+"-"+model.Family)
		if !ok {
			writeGenerationError(w, http.StatusInternalServerError, "workflow для выбранного семейства моделей не найден")
			return
		}
	}
	if input.Seed < 0 {
		input.Seed, err = randomSeed()
		if err != nil {
			writeGenerationError(w, http.StatusInternalServerError, "не удалось подготовить случайный seed")
			return
		}
	}
	if definition.RequiresImage || (definition.AllowsImages && input.imageCount() > 0) {
		maxImages := maxGenerationInputImages(model.Family)
		if input.imageCount() > maxImages {
			writeGenerationError(w, http.StatusBadRequest, fmt.Sprintf("для этого workflow доступно не более %d изображений", maxImages))
			return
		}
		for _, image := range input.images() {
			if err := a.validateGenerationImage(image, user.ID); err != nil {
				writeGenerationError(w, http.StatusForbidden, err.Error())
				return
			}
		}
	}
	prompt, err := definition.buildPrompt(input)
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	requestID := strings.TrimSpace(r.Form.Get("client_request_id"))
	if requestID == "" {
		requestID = newRequestID()
	}
	if !validGenerationRequestID(requestID) {
		writeGenerationError(w, http.StatusBadRequest, "некорректный идентификатор запуска")
		return
	}
	existing, existingPromptID, err := a.store.ClaimGenerationRequest(r.Context(), user.ID, requestID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось подготовить восстановление запуска")
		return
	}
	if existing {
		if existingPromptID == "" {
			writeJSON(w, http.StatusAccepted, map[string]any{"request_id": requestID, "state": "submitting", "message": "Предыдущий запуск ещё подтверждается. Восстанавливаем состояние."})
			return
		}
		response := a.generationRunResponse(r.Context(), existingPromptID)
		response["request_id"] = requestID
		response["message"] = "Восстановлена уже отправленная генерация"
		writeJSON(w, http.StatusAccepted, response)
		return
	}
	releaseRequest := func() {
		if releaseErr := a.store.ReleaseGenerationRequest(r.Context(), user.ID, requestID); releaseErr != nil {
			log.Printf("release failed generation request for user %d: %v", user.ID, releaseErr)
		}
	}
	reservation, err := a.store.ReserveQuickGeneration(r.Context(), user.ID)
	if err != nil {
		releaseRequest()
		status, message := quickGenerationLimitError(err)
		writeGenerationError(w, status, message)
		return
	}
	miningLease, miningWarning, err := a.pauseMiningForQuickGeneration(r.Context(), user)
	if err != nil {
		releaseRequest()
		if releaseErr := a.store.ReleaseQuickGeneration(r.Context(), reservation); releaseErr != nil {
			log.Printf("release quick generation reservation for user %d: %v", user.ID, releaseErr)
		}
		writeGenerationError(w, http.StatusServiceUnavailable, "не удалось освободить ресурсы для приоритетной генерации: "+err.Error())
		return
	}
	promptID, err := a.submitComfyPrompt(r.Context(), user.ID, prompt)
	if err != nil {
		releaseRequest()
		if miningLease != nil {
			a.releaseMiningPause(r.Context(), miningLease.ID)
		}
		if releaseErr := a.store.ReleaseQuickGeneration(r.Context(), reservation); releaseErr != nil {
			log.Printf("release quick generation reservation for user %d: %v", user.ID, releaseErr)
		}
		writeGenerationError(w, http.StatusBadGateway, "ComfyUI не принял workflow: "+err.Error())
		return
	}
	if err := a.store.BindGenerationRequestPrompt(r.Context(), user.ID, requestID, promptID); err != nil {
		log.Printf("bind generation request %s to prompt %s: %v", requestID, promptID, err)
	}
	if err := a.attachMiningPauseToGeneration(r.Context(), miningLease, promptID); err != nil {
		log.Printf("attach mining-pause lease to generation %s: %v", promptID, err)
	}
	a.rememberGeneration(promptID, user.ID)
	a.recordGenerationEvent(r.Context(), user.ID, promptID, definition, input)
	response := a.generationRunResponse(r.Context(), promptID)
	response["request_id"] = requestID
	if miningLease != nil && miningLease.ResumeMining {
		response["mining_paused"] = true
	}
	if miningWarning != "" {
		response["mining_warning"] = miningWarning
	}
	writeJSON(w, http.StatusAccepted, response)
}

func validGenerationRequestID(value string) bool {
	if len(value) < 16 || len(value) > 96 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func (a *App) generationRunResponse(ctx context.Context, promptID string) map[string]any {
	response := map[string]any{"prompt_id": promptID, "message": "Генерация поставлена в очередь", "state": "queued"}
	if queued, running, position, total, err := a.generationQueueState(ctx, promptID); err == nil {
		if running {
			response["state"] = "running"
		} else if queued {
			response["queue_position"] = position
			response["queue_total"] = total
		}
	}
	return response
}

func (a *App) handleRecoverGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
	if !validGenerationRequestID(requestID) {
		writeGenerationError(w, http.StatusBadRequest, "некорректный идентификатор запуска")
		return
	}
	user := a.currentUser(r)
	promptID, err := a.store.GenerationRequestPromptID(r.Context(), user.ID, requestID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось восстановить запуск")
		return
	}
	if promptID == "" {
		writeJSON(w, http.StatusAccepted, map[string]any{"request_id": requestID, "state": "submitting", "message": "Запуск ещё подтверждается. Повторяем проверку."})
		return
	}
	response := a.generationRunResponse(r.Context(), promptID)
	response["request_id"] = requestID
	response["message"] = "Генерация восстановлена"
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleCancelGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRequest)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	promptID := strings.TrimSpace(r.Form.Get("prompt_id"))
	if !validComfyPromptID(promptID) {
		writeGenerationError(w, http.StatusBadRequest, "некорректный идентификатор генерации")
		return
	}
	user := a.currentUser(r)
	owned, err := a.generationOwned(r.Context(), promptID, user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось проверить владельца генерации")
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	queued, running, _, _, err := a.generationQueueState(r.Context(), promptID)
	if err != nil {
		writeGenerationError(w, http.StatusBadGateway, "не удалось получить очередь ComfyUI: "+err.Error())
		return
	}
	if queued {
		err = a.cancelQueuedComfyGeneration(r.Context(), promptID)
	} else if running {
		err = a.interruptRunningComfyGeneration(r.Context())
	}
	if err != nil {
		writeGenerationError(w, http.StatusBadGateway, "не удалось отменить генерацию: "+err.Error())
		return
	}
	a.releaseMiningPauseForGeneration(r.Context(), promptID)
	a.releaseComfyMemoryIfIdle(r.Context())
	a.generationMu.Lock()
	delete(a.generationJobs, promptID)
	a.generationMu.Unlock()
	a.audit(r.Context(), &user.ID, "quick_generation_cancelled", "comfyui", nil, a.clientIP(r), r.UserAgent(), map[string]any{"prompt_id": promptID, "running": running})
	message := "Генерация уже завершилась."
	if queued || running {
		message = "Генерация отменена."
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": queued || running, "message": message})
}

func (a *App) cancelQueuedComfyGeneration(ctx context.Context, promptID string) error {
	return a.postComfyGenerationControl(ctx, "/queue", map[string]any{"delete": []string{promptID}})
}

func (a *App) interruptRunningComfyGeneration(ctx context.Context) error {
	return a.postComfyGenerationControl(ctx, "/interrupt", nil)
}

func (a *App) postComfyGenerationControl(ctx context.Context, endpointPath string, document any) error {
	var body io.Reader = http.NoBody
	if document != nil {
		encoded, err := json.Marshal(document)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, endpointPath)
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return err
	}
	if document != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("ComfyUI вернул HTTP %d", response.StatusCode)
	}
	return nil
}

func quickGenerationLimitError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrQuickGenerationDailyLimit):
		return http.StatusTooManyRequests, "достигнут суточный лимит быстрых генераций"
	case errors.Is(err, store.ErrQuickGenerationTotalLimit):
		return http.StatusTooManyRequests, "достигнут общий лимит быстрых генераций"
	case errors.Is(err, store.ErrQuickGenerationForbidden):
		return http.StatusForbidden, "доступ к быстрой генерации закрыт"
	default:
		log.Printf("reserve quick generation: %v", err)
		return http.StatusInternalServerError, "не удалось проверить лимит генераций"
	}
}

func (a *App) quickGenerationUploadHandler() http.Handler {
	proxy := a.proxyRootHandler("comfyui", a.cfg.ComfyUIUpstream, a.cfg.ComfyUIUpstreamAuthHeader)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		cloned := r.Clone(r.Context())
		cloned.URL.Path = "/upload/image"
		cloned.URL.RawPath = ""
		proxy.ServeHTTP(w, cloned)
	})
}

func maxGenerationInputImages(family string) int {
	switch family {
	case modelFamilyFlux2:
		return 4
	case modelFamilyKrea2:
		return 2
	case modelFamilyMiniMaxH3:
		return 4
	default:
		return 1
	}
}

func (a *App) handleGenerateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	promptID := strings.TrimSpace(r.URL.Query().Get("prompt_id"))
	if !validComfyPromptID(promptID) {
		writeGenerationError(w, http.StatusBadRequest, "некорректный идентификатор генерации")
		return
	}
	user := a.currentUser(r)
	owned, err := a.generationOwned(r.Context(), promptID, user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось проверить владельца генерации")
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	status, err := a.fetchGenerationStatus(r.Context(), promptID, user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusBadGateway, "не удалось получить состояние генерации: "+err.Error())
		return
	}
	if status.State == "completed" || status.State == "error" {
		a.releaseComfyMemoryIfIdle(r.Context())
		a.releaseMiningPauseForGeneration(r.Context(), promptID)
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *App) handleGenerationQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	overview, err := a.generationQueueOverview(r.Context())
	if err != nil {
		writeGenerationError(w, http.StatusBadGateway, "не удалось получить очередь генерации: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (a *App) submitComfyPrompt(ctx context.Context, userID int64, prompt map[string]any) (string, error) {
	document := comfyPromptDocument(a.comfyClientID(userID), prompt)
	body, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/prompt")
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 20 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", response.StatusCode, truncate(string(responseBody), 300))
	}
	var result struct {
		PromptID   string         `json:"prompt_id"`
		NodeErrors map[string]any `json:"node_errors"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", fmt.Errorf("некорректный ответ ComfyUI: %w", err)
	}
	if result.PromptID == "" {
		if len(result.NodeErrors) > 0 {
			return "", errors.New("ComfyUI отклонил узлы workflow")
		}
		return "", errors.New("ComfyUI не вернул prompt_id")
	}
	if !validComfyPromptID(result.PromptID) {
		return "", errors.New("ComfyUI вернул некорректный prompt_id")
	}
	return result.PromptID, nil
}

func comfyPromptDocument(clientID string, prompt map[string]any) map[string]any {
	// ComfyUI extensions inspect this standard metadata before execution. A real
	// workflow object prevents them from treating a missing extra_pnginfo entry
	// as malformed while keeping the gateway API prompt independent of the UI.
	workflow := map[string]any{
		"id":           "ai-access-gateway",
		"version":      0.4,
		"nodes":        []any{},
		"links":        []any{},
		"groups":       []any{},
		"config":       map[string]any{},
		"seed_widgets": map[string]any{},
	}
	return map[string]any{
		"prompt":     prompt,
		"client_id":  clientID,
		"extra_data": map[string]any{"extra_pnginfo": map[string]any{"workflow": workflow}},
	}
}

func (a *App) fetchGenerationStatus(ctx context.Context, promptID string, userID int64) (generationStatus, error) {
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/history/"+url.PathEscape(promptID))
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return generationStatus{}, err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return generationStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return generationStatus{}, fmt.Errorf("history returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGenerationHistory+1))
	if err != nil || len(body) > maxGenerationHistory {
		if err != nil {
			return generationStatus{}, err
		}
		return generationStatus{}, errors.New("ответ history слишком большой")
	}
	var history map[string]json.RawMessage
	if err := json.Unmarshal(body, &history); err != nil {
		return generationStatus{}, err
	}
	rawEntry, exists := history[promptID]
	status := generationStatus{PromptID: promptID, State: "queued", Message: "Генерация ожидает запуска"}
	if !exists {
		queued, running, position, total, queueErr := a.generationQueueState(ctx, promptID)
		if queueErr == nil {
			switch {
			case running:
				status.State, status.Message = "running", "ComfyUI начал выполнение workflow"
			case queued:
				status.QueuePosition, status.QueueTotal = position, total
				if position > 0 && total > 0 {
					status.Message = fmt.Sprintf("В очереди: %d из %d", position, total)
				}
			}
		}
		return status, nil
	}
	var entry struct {
		Outputs map[string]json.RawMessage `json:"outputs"`
		Status  struct {
			StatusStr string `json:"status_str"`
			Completed bool   `json:"completed"`
		} `json:"status"`
	}
	if err := json.Unmarshal(rawEntry, &entry); err != nil {
		return generationStatus{}, err
	}
	outputs, err := parseGenerationOutputs(entry.Outputs)
	if err != nil {
		return generationStatus{}, err
	}
	status.Outputs = outputs
	for _, output := range outputs {
		if a.store == nil {
			break
		}
		// Ownership is recorded only after ComfyUI confirms the output exists.
		_ = a.store.InsertComfyOutputOwnerships(ctx, userID, []domain.ComfyOutputOwnership{{
			PromptID: promptID, Filename: output.Filename, Subfolder: output.Subfolder,
			StorageType: output.Type, MediaType: output.MediaType,
		}})
	}
	if len(outputs) > 0 {
		a.archiveGenerationOutputs(ctx, userID, outputs)
	}
	a.rememberGenerationOutputs(promptID, outputs)
	statusStr := strings.ToLower(strings.TrimSpace(entry.Status.StatusStr))
	if statusStr == "error" || statusStr == "failed" {
		status.State, status.Message = "error", "ComfyUI завершил генерацию с ошибкой"
		return status, nil
	}
	if entry.Status.Completed || statusStr == "success" || len(outputs) > 0 {
		status.State, status.Message = "completed", "Готово"
		return status, nil
	}
	status.State, status.Message = "running", "ComfyUI выполняет workflow"
	return status, nil
}

func (a *App) generationQueueState(ctx context.Context, promptID string) (queued, running bool, position, total int, err error) {
	queue, err := a.fetchGenerationQueue(ctx)
	if err != nil {
		return false, false, 0, 0, err
	}
	for _, raw := range queue.Running {
		if comfyQueueItemPromptID(raw) == promptID {
			return false, true, 0, len(queue.Pending), nil
		}
	}
	for index, raw := range queue.Pending {
		if comfyQueueItemPromptID(raw) == promptID {
			return true, false, index + 1, len(queue.Pending), nil
		}
	}
	return false, false, 0, len(queue.Pending), nil
}

func (a *App) generationQueueOverview(ctx context.Context) (generationQueueOverview, error) {
	queue, err := a.fetchGenerationQueue(ctx)
	if err != nil {
		return generationQueueOverview{}, err
	}
	return generationQueueOverview{Running: len(queue.Running), Pending: len(queue.Pending)}, nil
}

// releaseComfyMemoryIfIdle asks ComfyUI to unload loaded models only after the
// whole queue is empty. This keeps a completed quick generation from retaining
// RAM and VRAM, without interrupting another user's queued work.
func (a *App) releaseComfyMemoryIfIdle(ctx context.Context) {
	overview, err := a.generationQueueOverview(ctx)
	if err != nil {
		log.Printf("inspect ComfyUI queue before freeing memory: %v", err)
		return
	}
	if overview.Running != 0 || overview.Pending != 0 {
		return
	}

	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/free")
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), bytes.NewBufferString(`{"unload_models":true,"free_memory":true}`))
	if err != nil {
		log.Printf("build ComfyUI memory-release request: %v", err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		log.Printf("free ComfyUI memory: %v", err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		log.Printf("free ComfyUI memory: upstream returned HTTP %d", response.StatusCode)
	}
}

func (a *App) fetchGenerationQueue(ctx context.Context) (comfyQueueSnapshot, error) {
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/queue")
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return comfyQueueSnapshot{}, err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return comfyQueueSnapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return comfyQueueSnapshot{}, fmt.Errorf("queue returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCapturedContent+1))
	if err != nil || len(body) > maxCapturedContent {
		if err != nil {
			return comfyQueueSnapshot{}, err
		}
		return comfyQueueSnapshot{}, errors.New("ответ queue слишком большой")
	}
	var queue comfyQueueSnapshot
	if err := json.Unmarshal(body, &queue); err != nil {
		return comfyQueueSnapshot{}, err
	}
	return queue, nil
}

func comfyQueueItemPromptID(raw json.RawMessage) string {
	var item []json.RawMessage
	if json.Unmarshal(raw, &item) != nil || len(item) < 2 {
		return ""
	}
	var promptID string
	_ = json.Unmarshal(item[1], &promptID)
	return promptID
}

func parseGenerationOutputs(nodes map[string]json.RawMessage) ([]generationOutput, error) {
	seen := map[string]struct{}{}
	var outputs []generationOutput
	for _, rawNode := range nodes {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(rawNode, &node); err != nil {
			continue
		}
		for kind, rawItems := range node {
			if kind != "images" && kind != "gifs" && kind != "videos" {
				continue
			}
			var items []struct {
				Filename  string `json:"filename"`
				Subfolder string `json:"subfolder"`
				Type      string `json:"type"`
				Format    string `json:"format"`
			}
			if json.Unmarshal(rawItems, &items) != nil {
				continue
			}
			for _, item := range items {
				if strings.TrimSpace(item.Filename) == "" {
					continue
				}
				if item.Type == "" {
					item.Type = "output"
				}
				mediaType := classifyComfyOutput(kind, item.Filename, item.Format)
				key := item.Filename + "\x00" + item.Subfolder + "\x00" + item.Type
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				query := url.Values{}
				query.Set("filename", item.Filename)
				query.Set("subfolder", item.Subfolder)
				query.Set("type", item.Type)
				query.Set("media_type", mediaType)
				outputs = append(outputs, generationOutput{Filename: item.Filename, Subfolder: item.Subfolder, Type: item.Type, MediaType: mediaType, URL: "/generate/output?" + query.Encode()})
			}
		}
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Filename < outputs[j].Filename })
	return outputs, nil
}

func (a *App) handleGenerationOutput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("filename"))
	subfolder := strings.TrimSpace(r.URL.Query().Get("subfolder"))
	storageType := strings.TrimSpace(r.URL.Query().Get("type"))
	mediaType := strings.TrimSpace(r.URL.Query().Get("media_type"))
	if name == "" || storageType != "output" && storageType != "temp" || mediaType != "image" && mediaType != "video" {
		http.NotFound(w, r)
		return
	}
	user := a.currentUser(r)
	ownerID, known, err := a.store.ComfyOutputOwner(r.Context(), name, subfolder, storageType)
	if err != nil {
		http.Error(w, "не удалось проверить доступ", http.StatusInternalServerError)
		return
	}
	if !known || ownerID != user.ID {
		http.NotFound(w, r)
		return
	}
	body, contentType, status, err := a.fetchGenerationOutput(r.Context(), generationOutput{
		Filename: name, Subfolder: subfolder, Type: storageType, MediaType: mediaType,
	})
	if err != nil {
		http.Error(w, "результат временно недоступен", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", contentType)
	setGenerationDownloadDisposition(w, r, name)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (a *App) handleGenerationLibraryMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/generate/library/"), 10, 64)
	if err != nil || id <= 0 || a.store == nil || a.contentCipher == nil {
		http.NotFound(w, r)
		return
	}
	user := a.currentUser(r)
	media, err := a.store.ContentMediaByIDForUser(r.Context(), id, user.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	payload, err := a.contentCipher.DecryptBytes(media.PayloadCipher)
	if err != nil {
		http.Error(w, "ошибка расшифровки результата", http.StatusInternalServerError)
		return
	}
	contentType, inline := safeAdminMediaType(media.MediaType, media.MIMEType)
	if !inline {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	setGenerationDownloadDisposition(w, r, media.OriginalName)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

// setGenerationDownloadDisposition explicitly starts a browser download when
// requested. Android Chrome then hands the file to Download Manager, which
// indexes it in MediaStore instead of leaving it only in the browser cache.
func setGenerationDownloadDisposition(w http.ResponseWriter, r *http.Request, name string) {
	if r.URL.Query().Get("download") != "1" {
		return
	}
	filename := path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if filename == "." || filename == "/" || filename == "" {
		filename = "generation-result"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
}

func (a *App) handleRecentGenerationLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	writeJSON(w, http.StatusOK, map[string]any{"media": a.recentGenerationMedia(r.Context(), user.ID)})
}

func (a *App) handleHideGenerationLibraryMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("media_id")), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	user := a.currentUser(r)
	hidden, err := a.store.HideGenerationMediaForUser(r.Context(), id, user.ID)
	if err != nil {
		http.Error(w, "не удалось обновить галерею", http.StatusInternalServerError)
		return
	}
	if !hidden {
		http.NotFound(w, r)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"removed": true, "media_id": id})
		return
	}
	http.Redirect(w, r, "/generate#my-results", http.StatusSeeOther)
}

func (a *App) fetchGenerationOutput(ctx context.Context, output generationOutput) ([]byte, string, int, error) {
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/view")
	if output.MediaType == "video" {
		endpoint.Path = singleJoiningSlash(endpoint.Path, "/viewvideo")
	}
	query := endpoint.Query()
	query.Set("filename", output.Filename)
	query.Set("subfolder", output.Subfolder)
	query.Set("type", output.Type)
	endpoint.RawQuery = query.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return nil, "", 0, err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return nil, "", 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", response.StatusCode, fmt.Errorf("ComfyUI returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCapturedMedia+1))
	if err != nil || len(body) > maxCapturedMedia {
		return nil, "", response.StatusCode, errors.New("результат превышает допустимый размер")
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return body, contentType, response.StatusCode, nil
}

func (a *App) archiveGenerationOutputs(ctx context.Context, userID int64, outputs []generationOutput) {
	if a.contentCipher == nil || a.store == nil {
		return
	}
	for _, output := range outputs {
		body, contentType, status, err := a.fetchGenerationOutput(ctx, output)
		if err != nil {
			log.Printf("archive generation output %s: %v", output.Filename, err)
			continue
		}
		capture := &proxyContentCapture{
			userID: userID, service: "comfyui", mediaName: output.Filename,
			mediaSubfolder: output.Subfolder, mediaStorageType: output.Type,
			mediaType: output.MediaType, mimeType: contentType, isMedia: true,
			status: status, response: newLimitedBuffer(maxCapturedMedia),
		}
		_, _ = capture.response.Write(body)
		if err := a.persistComfyMedia(ctx, capture); err != nil {
			log.Printf("persist generation output %s: %v", output.Filename, err)
		}
	}
}

func (a *App) rememberGeneration(promptID string, userID int64) {
	a.generationMu.Lock()
	defer a.generationMu.Unlock()
	if a.generationJobs == nil {
		a.generationJobs = make(map[string]*generationJob)
	}
	for id, job := range a.generationJobs {
		if time.Since(job.CreatedAt) > 2*time.Hour {
			delete(a.generationJobs, id)
		}
	}
	a.generationJobs[promptID] = &generationJob{UserID: userID, CreatedAt: time.Now(), Outputs: make(map[string]struct{})}
}

func (a *App) rememberGenerationOutputs(promptID string, outputs []generationOutput) {
	a.generationMu.Lock()
	defer a.generationMu.Unlock()
	job := a.generationJobs[promptID]
	if job == nil {
		return
	}
	if job.Outputs == nil {
		job.Outputs = make(map[string]struct{})
	}
	for _, output := range outputs {
		job.Outputs[output.Filename+"\x00"+output.Subfolder+"\x00"+output.Type] = struct{}{}
	}
}

// refreshTrackedGenerationStatuses archives completed output even when the
// browser that submitted the task was closed before its next status request.
func (a *App) refreshTrackedGenerationStatuses(ctx context.Context) {
	type trackedGeneration struct {
		promptID string
		userID   int64
	}
	now := time.Now()
	a.generationMu.Lock()
	jobs := make([]trackedGeneration, 0, len(a.generationJobs))
	for promptID, job := range a.generationJobs {
		if now.Sub(job.CreatedAt) > 2*time.Hour {
			delete(a.generationJobs, promptID)
			continue
		}
		jobs = append(jobs, trackedGeneration{promptID: promptID, userID: job.UserID})
	}
	a.generationMu.Unlock()

	for _, job := range jobs {
		status, err := a.fetchGenerationStatus(ctx, job.promptID, job.userID)
		if err != nil {
			log.Printf("refresh ComfyUI generation %s: %v", job.promptID, err)
			continue
		}
		if status.State != "completed" && status.State != "error" {
			continue
		}
		a.releaseComfyMemoryIfIdle(ctx)
		a.releaseMiningPauseForGeneration(ctx, job.promptID)
		a.generationMu.Lock()
		delete(a.generationJobs, job.promptID)
		a.generationMu.Unlock()
	}
}

func (a *App) generationOwned(ctx context.Context, promptID string, userID int64) (bool, error) {
	a.generationMu.Lock()
	job := a.generationJobs[promptID]
	a.generationMu.Unlock()
	if job != nil {
		return job.UserID == userID, nil
	}
	if a.store == nil {
		return false, nil
	}
	return a.store.ContentEventOwnedByUser(ctx, "comfyui", promptID, userID)
}

func (a *App) validateGenerationImage(value string, userID int64) error {
	normalized, err := normalizeComfyDataPath(value, false)
	if err != nil {
		return errors.New("некорректное загруженное фото")
	}
	ownNamespace := comfyUploadNamespace(a.comfyClientID(userID))
	if !strings.HasPrefix(normalized, ownNamespace+"/") {
		return errors.New("фото принадлежит другой сессии")
	}
	return nil
}

func (a *App) recordGenerationEvent(ctx context.Context, userID int64, promptID string, definition workflowDefinition, input generationForm) {
	if a.contentCipher == nil || a.store == nil {
		return
	}
	sensitive := isSensitiveGeneration(input)
	metadataFields := map[string]any{
		"workflow": definition.ID, "preset": input.PresetID, "model_family": input.ModelFamily,
		"model": input.ModelName, "width": input.Width, "height": input.Height,
		"steps": input.Steps, "cfg": input.CFG, "denoise": input.Denoise, "seed": input.Seed,
		"base_megapixels": input.BaseMegapixels, "lora_strength": input.LoraStrength,
		"upscale_steps": input.UpscaleSteps, "upscale_denoise": input.UpscaleDenoise,
		"detail_steps": input.DetailSteps, "detail_denoise": input.DetailDenoise,
		"input_images":      input.imageCount(),
		"sensitive_content": sensitive,
	}
	if input.AssistantRequested {
		metadataFields["prompt_assistant"] = map[string]any{
			"requested": true, "applied": input.AssistantApplied, "template": input.AssistantTemplate,
			"think": input.AssistantThink, "original_prompt": input.AssistantOriginal, "suggestion": input.AssistantSuggestion,
		}
	}
	metadata, _ := json.Marshal(metadataFields)
	promptCipher, err := a.contentCipher.Encrypt(input.Positive)
	if err != nil {
		log.Printf("generation prompt encryption: %v", err)
		return
	}
	negativeCipher, err := a.contentCipher.Encrypt(input.Negative)
	if err != nil {
		log.Printf("generation negative prompt encryption: %v", err)
		return
	}
	metadataCipher, err := a.contentCipher.Encrypt(string(metadata))
	if err != nil {
		log.Printf("generation metadata encryption: %v", err)
		return
	}
	if _, err := a.store.InsertContentEvent(ctx, domain.ContentEventRecord{UserID: userID, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: promptID, Model: input.ModelName, PromptCipher: promptCipher, ResponseCipher: negativeCipher, MetadataCipher: metadataCipher, Sensitive: sensitive}); err != nil {
		log.Printf("store generation event: %v", err)
	}
}

func validComfyPromptID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'f') && (char < 'A' || char > 'F') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func writeGenerationError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
