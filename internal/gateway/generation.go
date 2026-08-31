package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
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

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

const (
	maxGenerationRequest           = 32 << 10
	maxComfyObjectInfo             = 32 << 20
	maxGenerationHistory           = 32 << 20
	maxGenerationOutputFingerprint = int64(2 << 30)
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
	PromptID             string             `json:"prompt_id"`
	JobID                string             `json:"job_id,omitempty"`
	RequestID            string             `json:"request_id,omitempty"`
	JobState             string             `json:"job_state,omitempty"`
	State                string             `json:"state"`
	Message              string             `json:"message"`
	QueuePosition        int                `json:"queue_position,omitempty"`
	QueueTotal           int                `json:"queue_total,omitempty"`
	EstimatedWaitSeconds int                `json:"estimated_wait_seconds,omitempty"`
	Outputs              []generationOutput `json:"outputs,omitempty"`
	Known                bool               `json:"-"`
}

type generationQueueOverview struct {
	Running              int    `json:"running"`
	Pending              int    `json:"pending"`
	CurrentTask          string `json:"current_task"`
	EstimatedWaitSeconds int    `json:"estimated_wait_seconds,omitempty"`
	AverageTaskSeconds   int    `json:"average_task_seconds,omitempty"`
}

type generationQuotaView struct {
	DailyLimit     int   `json:"daily_limit"`
	DailyUsed      int   `json:"daily_used"`
	DailyRemaining int   `json:"daily_remaining"`
	TotalLimit     int64 `json:"total_limit"`
	TotalUsed      int64 `json:"total_used"`
	TotalRemaining int64 `json:"total_remaining"`
	HasLimits      bool  `json:"has_limits"`
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
	gallery := quick(http.HandlerFunc(a.handleGenerationGalleryPage))
	run := quick(http.HandlerFunc(a.handleGenerateRun))
	recover := quick(http.HandlerFunc(a.handleRecoverGeneration))
	promptAssistant := quick(http.HandlerFunc(a.handlePromptAssistant))
	status := quick(http.HandlerFunc(a.handleGenerateStatus))
	cancel := quick(http.HandlerFunc(a.handleCancelGeneration))
	queue := quick(http.HandlerFunc(a.handleGenerationQueue))
	output := quick(http.HandlerFunc(a.handleGenerationOutput))
	library := quick(http.HandlerFunc(a.handleGenerationLibraryMedia))
	recentLibrary := quick(http.HandlerFunc(a.handleRecentGenerationLibrary))
	libraryImages := quick(a.requireQuickGenerationTypes([]string{"minimax-h3-video"}, http.HandlerFunc(a.handleGenerationLibraryImages)))
	hideLibrary := quick(http.HandlerFunc(a.handleHideGenerationLibraryMedia))
	reuseLibraryImage := quick(a.requireQuickGenerationTypes([]string{"minimax-h3-video"}, http.HandlerFunc(a.handleReuseGenerationLibraryImage)))
	upload := quick(a.requireQuickGenerationTypes([]string{"image-to-image", "minimax-h3-video"}, a.quickGenerationUploadHandler()))
	uploadAudio := quick(a.requireQuickGenerationTypes([]string{"minimax-h3-video"}, a.quickGenerationAudioUploadHandler()))
	uploadVideo := quick(a.requireQuickGenerationTypes([]string{"minimax-h3-video"}, a.quickGenerationVideoUploadHandler()))
	mux.Handle("/generate", page)
	mux.Handle("/generate/", page)
	mux.Handle("/gallery", gallery)
	mux.Handle("/gallery/", gallery)
	mux.Handle("/generate/upload/image", upload)
	mux.Handle("/generate/upload/audio", uploadAudio)
	mux.Handle("/generate/upload/video", uploadVideo)
	mux.Handle("/generate/run", run)
	mux.Handle("/generate/recover", recover)
	mux.Handle("/generate/jobs", quick(http.HandlerFunc(a.handleGenerationJobs)))
	mux.Handle("/generate/jobs/detail", quick(http.HandlerFunc(a.handleGenerationJobDetail)))
	mux.Handle("/generate/jobs/events", quick(http.HandlerFunc(a.handleGenerationJobEvents)))
	mux.Handle("/generate/jobs/cancel", quick(http.HandlerFunc(a.handleGenerationJobCancel)))
	mux.Handle("/generate/jobs/retry", quick(http.HandlerFunc(a.handleGenerationJobRetry)))
	mux.Handle("/generate/prompt-assistant", promptAssistant)
	mux.Handle("/generate/status", status)
	mux.Handle("/generate/cancel", cancel)
	mux.Handle("/generate/queue", queue)
	mux.Handle("/generate/output", output)
	mux.Handle("/generate/library/hide", hideLibrary)
	mux.Handle("/generate/library/reuse-image", reuseLibraryImage)
	mux.Handle("/generate/library/images", libraryImages)
	mux.Handle("/generate/library/recent", recentLibrary)
	mux.Handle("/generate/library/", library)
	a.registerGenerationCompanionRoutes(mux, quick)
}

func (a *App) requireQuickGenerationTypes(templateIDs []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := a.currentUser(r)
		for _, templateID := range templateIDs {
			if user != nil && user.CanUseQuickGenerationType(templateID) {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeGenerationError(w, http.StatusForbidden, "этот тип быстрой генерации недоступен")
	})
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
	policy, policyErr := a.generationAccessPolicy(r.Context(), user)
	if policyErr != nil {
		http.Error(w, "не удалось загрузить права быстрой генерации", http.StatusInternalServerError)
		return
	}
	quota, quotaErr := a.generationQuotaView(r.Context(), user.ID)
	if quotaErr != nil {
		http.Error(w, "не удалось загрузить лимиты быстрой генерации", http.StatusInternalServerError)
		return
	}
	for index := range presets {
		if presets[index].AdminOnly && user.Role != "admin" {
			presets[index].Restricted = true
			presets[index].Restriction = "Доступно только администратору"
		}
	}
	presets = filterGenerationPresets(presets, policy)
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
		"QuickModels":           filterGenerationModels(quickGenerationModels(catalog), policy),
		"LoraGroups":            filterGenerationLoraGroups(catalog.LoraGroups, policy.KreaLoraGroups),
		"FluxLoraGroups":        filterGenerationLoraGroups(catalog.FluxLoraGroups, policy.FluxLoraGroups),
		"MiniMaxLoraGroups":     catalog.MiniMaxLoraGroups,
		"ComfyOnline":           catalog.Online,
		"ModelsAvailable":       catalog.AvailableCount > 0,
		"SelectedWorkflow":      r.URL.Query().Get("workflow"),
		"RecentGenerationMedia": recentMedia,
		"GenerationQuota":       quota,
	})
}

func (a *App) generationQuotaView(ctx context.Context, userID int64) (generationQuotaView, error) {
	quota, err := a.store.QuickGenerationQuota(ctx, userID)
	if err != nil {
		return generationQuotaView{}, err
	}
	view := generationQuotaView{
		DailyLimit: quota.DailyLimit, DailyUsed: quota.DailyUsed,
		TotalLimit: quota.TotalLimit, TotalUsed: quota.TotalUsed,
		HasLimits: quota.DailyLimit > 0 || quota.TotalLimit > 0,
	}
	if view.DailyLimit > 0 {
		view.DailyRemaining = view.DailyLimit - view.DailyUsed
		if view.DailyRemaining < 0 {
			view.DailyRemaining = 0
		}
	}
	if view.TotalLimit > 0 {
		view.TotalRemaining = view.TotalLimit - view.TotalUsed
		if view.TotalRemaining < 0 {
			view.TotalRemaining = 0
		}
	}
	return view, nil
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
			MediaType: item.MediaType, ExpiresUnix: item.ExpiresAt.UnixMilli(), Sensitive: item.Sensitive || item.VisualPending,
		})
	}
	return views
}

func (a *App) handleGenerateRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	started := time.Now()
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRequest)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	user := a.currentUser(r)
	input, err := parseGenerationForm(r)
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
	var parentJobID *int64
	if parentPublicID := strings.TrimSpace(r.Form.Get("parent_job_id")); parentPublicID != "" {
		parent, parentErr := a.store.GenerationJobByPublicID(r.Context(), user.ID, parentPublicID)
		if errors.Is(parentErr, sql.ErrNoRows) {
			writeGenerationError(w, http.StatusBadRequest, "исходное задание для повтора больше недоступно")
			return
		}
		if parentErr != nil {
			writeGenerationError(w, http.StatusInternalServerError, "не удалось подготовить повтор генерации")
			return
		}
		parentJobID = &parent.ID
	}
	job, shouldSubmit, err := a.store.ClaimGenerationJob(r.Context(), domain.CreateGenerationJobParams{
		PublicID: newRequestID(), UserID: user.ID, UsernameSnapshot: user.Username, RequestID: requestID, ParentJobID: parentJobID,
	})
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось создать задание генерации")
		return
	}
	if !shouldSubmit {
		response := a.generationJobResponse(r.Context(), job)
		response["message"] = "Восстановлена уже отправленная генерация"
		writeJSON(w, http.StatusAccepted, response)
		return
	}
	job, _, err = a.store.TransitionGenerationJob(r.Context(), job.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobPreparing, Message: "Проверяем параметры и workflow",
	})
	if err != nil {
		writeGenerationJobError(w, http.StatusInternalServerError, job, "не удалось подготовить задание")
		return
	}
	preparation, err := a.prepareGeneration(r.Context(), user, input, true)
	if err != nil {
		code := "generation_preflight_failed"
		status := http.StatusBadRequest
		if errors.Is(err, errMinorSexualContent) {
			code = "minor_content_blocked"
			status = http.StatusUnprocessableEntity
			a.audit(r.Context(), &user.ID, "generation_safety_blocked", "quick_generation", nil, a.clientIP(r), r.UserAgent(), map[string]any{"reason": "minor_sexual_content", "job_id": job.PublicID})
		}
		job = a.failGenerationJob(r.Context(), job, code, err.Error(), err)
		writeGenerationJobError(w, status, job, err.Error())
		return
	}
	input, definition, prompt := preparation.Input, preparation.Definition, preparation.Prompt
	inputCount := generationJobInputCount(input)
	if inputCount > 0 {
		job, _, err = a.store.TransitionGenerationJob(r.Context(), job.ID, domain.GenerationJobTransitionParams{
			State: domain.GenerationJobUploading, Message: "Проверяем и закрепляем референсы",
		})
		if err != nil {
			job = a.failGenerationJob(r.Context(), job, "generation_input_state_failed", "Не удалось закрепить референсы", err)
			writeGenerationJobError(w, http.StatusInternalServerError, job, "не удалось подготовить референсы")
			return
		}
	}
	payloadCipher, err := a.generationJobPayloadCipher(input, r.Form)
	if err != nil {
		job = a.failGenerationJob(r.Context(), job, "generation_payload_failed", "Не удалось сохранить параметры запуска", err)
		writeGenerationJobError(w, http.StatusInternalServerError, job, "не удалось сохранить параметры запуска")
		return
	}
	job, err = a.store.PrepareGenerationJob(r.Context(), job.ID, domain.PreparedGenerationJob{
		TemplateID: input.TemplateID, WorkflowID: input.PresetID, ModelName: input.ModelName, Seed: input.Seed,
		PayloadCipher: payloadCipher, Dependencies: generationJobDependencies(input, user), InputCount: inputCount,
	})
	if err != nil {
		job = a.failGenerationJob(r.Context(), job, "generation_payload_store_failed", "Не удалось сохранить параметры запуска", err)
		writeGenerationJobError(w, http.StatusInternalServerError, job, "не удалось сохранить параметры запуска")
		return
	}
	job, _, err = a.store.TransitionGenerationJob(r.Context(), job.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobWaitingForResources, Message: "Ожидаем ресурсы",
	})
	if err != nil {
		job = a.failGenerationJob(r.Context(), job, "generation_resource_state_failed", "Не удалось подготовить ресурсы", err)
		writeGenerationJobError(w, http.StatusInternalServerError, job, "не удалось подготовить ресурсы")
		return
	}
	_, _, err = a.store.ReserveQuickGenerationForJob(r.Context(), job.ID, user.ID)
	if err != nil {
		status, message := quickGenerationLimitError(err)
		code := "generation_quota_failed"
		if errors.Is(err, store.ErrQuickGenerationDailyLimit) {
			code = "generation_daily_limit"
		} else if errors.Is(err, store.ErrQuickGenerationTotalLimit) {
			code = "generation_total_limit"
		} else if errors.Is(err, store.ErrQuickGenerationForbidden) {
			code = "generation_access_revoked"
		}
		job = a.failGenerationJob(r.Context(), job, code, message, err)
		writeGenerationJobError(w, status, job, message)
		return
	}
	miningLease, miningWarning, err := a.pauseMiningForQuickGeneration(r.Context(), user, job.ID)
	if err != nil {
		message := "Не удалось освободить ресурсы для приоритетной генерации"
		job = a.failGenerationJob(r.Context(), job, "mining_pause_failed", message, err)
		writeGenerationJobError(w, http.StatusServiceUnavailable, job, message+": "+err.Error())
		return
	}
	promptID, err := a.submitComfyPrompt(r.Context(), user.ID, job.PublicID, prompt)
	if err != nil {
		job = a.failGenerationJob(r.Context(), job, "comfy_submission_failed", "ComfyUI не принял workflow", err)
		writeGenerationJobError(w, http.StatusBadGateway, job, "ComfyUI не принял workflow: "+err.Error())
		return
	}
	if err := a.attachMiningPauseToGeneration(r.Context(), miningLease, promptID); err != nil {
		log.Printf("attach mining-pause lease to generation %s: %v", promptID, err)
	}
	a.rememberGeneration(promptID, user.ID)
	boundJob, bindErr := a.store.BindGenerationJobPrompt(r.Context(), job.ID, promptID)
	if bindErr != nil {
		log.Printf("bind generation job %s to prompt %s: %v", job.PublicID, promptID, bindErr)
	} else {
		job = boundJob
		if _, commitErr := a.store.CommitQuickGenerationForJob(r.Context(), job.ID); commitErr != nil {
			log.Printf("commit quota for generation job %s: %v", job.PublicID, commitErr)
		}
		a.recordGenerationEvent(r.Context(), job.ID, user.ID, promptID, definition, input)
		a.rememberGenerationVariant(r.Context(), job.ID, user.ID, promptID, input, r.Form)
		if linkErr := a.store.LinkGenerationJobContentEvent(r.Context(), job.ID, user.ID, promptID); linkErr != nil {
			log.Printf("link generation job %s content projection: %v", job.PublicID, linkErr)
		}
		if linkErr := a.store.LinkGenerationJobVariant(r.Context(), job.ID, promptID); linkErr != nil {
			log.Printf("link generation job %s variant projection: %v", job.PublicID, linkErr)
		}
		if queuedJob, _, transitionErr := a.store.TransitionGenerationJob(r.Context(), job.ID, domain.GenerationJobTransitionParams{
			State: domain.GenerationJobQueued, Message: "Генерация поставлена в очередь ComfyUI",
		}); transitionErr != nil {
			log.Printf("queue generation job %s: %v", job.PublicID, transitionErr)
		} else {
			job = queuedJob
		}
	}
	bytesIn := r.ContentLength
	if bytesIn < 0 {
		bytesIn = 0
	}
	a.recordProxyRequest(r.Context(), user.ID, "comfyui", http.MethodPost, quickGenerationTelemetryPath(requestID), http.StatusAccepted, time.Since(started), bytesIn, 0, false, a.clientIP(r), r.UserAgent())
	a.incProxyCount("comfyui", http.StatusAccepted)
	response := a.generationJobResponse(r.Context(), job)
	response["prompt_id"] = promptID
	if bindErr != nil {
		response["state"] = "submitting"
		response["message"] = "ComfyUI принял генерацию. Восстанавливаем серверную запись."
	}
	if quota, quotaErr := a.generationQuotaView(r.Context(), user.ID); quotaErr != nil {
		log.Printf("load quick generation quota for user %d: %v", user.ID, quotaErr)
	} else {
		response["quota"] = quota
	}
	if miningLease != nil && miningLease.ResumeMining {
		response["mining_paused"] = true
	}
	if miningWarning != "" {
		response["mining_warning"] = miningWarning
	}
	writeJSON(w, http.StatusAccepted, response)
}

func quickGenerationTelemetryPath(requestID string) string {
	return "/generate/run/" + strings.TrimSpace(requestID)
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

type generationPreparation struct {
	Input      generationForm
	Definition workflowDefinition
	Model      generationModel
	Prompt     map[string]any
	ObjectInfo comfyObjectInfoSnapshot
}

// prepareGeneration is deliberately shared by the preflight endpoint and the
// real submission path so an item reported as ready is checked by the same
// model, LoRA, workflow and image rules at submission time.
func (a *App) prepareGeneration(ctx context.Context, user *User, input generationForm, resolveSeed bool) (generationPreparation, error) {
	if err := validateGenerationPrompt(input.Positive, input.AssistantOriginal, input.AssistantSuggestion); err != nil {
		return generationPreparation{}, err
	}
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		return generationPreparation{}, errors.New("не удалось загрузить шаблоны генерации")
	}
	if input.TemplateID != "text-to-image" && input.TemplateID != "image-to-image" && input.TemplateID != "minimax-h3-video" {
		return generationPreparation{}, errors.New("неизвестный шаблон workflow")
	}
	definition, ok := findWorkflow(definitions, input.TemplateID)
	if !ok {
		return generationPreparation{}, errors.New("неизвестный шаблон workflow")
	}
	if user == nil || !user.CanUseQuickGenerationType(input.TemplateID) {
		return generationPreparation{}, errors.New("этот тип быстрой генерации отключён администратором")
	}
	if definition.AdminOnly && user.Role != "admin" {
		return generationPreparation{}, errors.New("этот сценарий доступен только администратору")
	}
	catalog := a.comfyGenerationModels(ctx)
	preset, ok := findGenerationPreset(buildGenerationPresets(catalog), input.PresetID, input.TemplateID)
	if !ok {
		return generationPreparation{}, errors.New("выбранный workflow больше не доступен")
	}
	model, ok := catalog.byID[preset.ModelID]
	if input.ModelID != "" {
		model, ok = catalog.byID[input.ModelID]
	}
	if !ok {
		return generationPreparation{}, errors.New("модель, привязанная к workflow, больше не доступна в ComfyUI")
	}
	if model.Family != preset.Family {
		return generationPreparation{}, errors.New("эта модель не разрешена для выбранного workflow")
	}
	if input.TemplateID == "image-to-image" && model.Family == modelFamilyKrea2 && model.ID != preset.ModelID {
		return generationPreparation{}, errors.New("для Krea2: редактирование доступна только совместимая оригинальная Turbo-модель")
	}
	if !model.Available {
		return generationPreparation{}, errors.New(model.Reason)
	}
	if requiresImageEditingSupport(definition) && !model.SupportsImage {
		return generationPreparation{}, errors.New("выбранная модель пока не подготовлена для редактирования фото")
	}
	input.ModelName = model.Name
	input.ModelFamily = model.Family
	input.TextEncoder = model.TextEncoder
	input.VAE = model.VAE
	input.AudioVAE = model.AudioVAE
	input.ReferenceModel = model.ReferenceModel
	input.Lora = model.Lora
	input.IdentityLora = model.IdentityLora
	input.VideoIntegratedTurbo = model.VideoIntegratedTurbo
	input.VideoReferenceOnly = model.VideoReferenceOnly
	if model.Family == modelFamilyMiniMaxH3 {
		if input.VideoSteps == 0 {
			input.VideoSteps = model.DefaultSteps
		}
		if input.VideoSampler == "" {
			input.VideoSampler = model.DefaultSampler
		}
		if input.VideoShiftVideo == 0 {
			input.VideoShiftVideo = model.DefaultVideoShift
		}
		if input.VideoShiftAudio == 0 {
			input.VideoShiftAudio = model.DefaultAudioShift
		}
	}
	if model.Family == modelFamilyKrea2 {
		if input.TemplateID == "image-to-image" {
			if input.IdentityLora == "" {
				return generationPreparation{}, errors.New("не установлена обязательная LoRA Krea2 для сохранения внешности")
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
					return generationPreparation{}, errors.New("выбранная LoRA недоступна для PhotoFlow Krea2")
				}
			}
		}
	} else if model.Family == modelFamilyFlux2 {
		for _, name := range input.LoraNames {
			if name != "" && !generationLoraAllowed(catalog.FluxLoraGroups, name) {
				return generationPreparation{}, errors.New("выбранная LoRA недоступна для Flux2")
			}
		}
	} else if model.Family == modelFamilyMiniMaxH3 {
		for _, name := range input.LoraNames {
			if name != "" && !generationLoraAllowed(catalog.MiniMaxLoraGroups, name) {
				return generationPreparation{}, errors.New("выбранная LoRA недоступна для MiniMax H3")
			}
		}
	} else {
		input.LoraNames = [maxGenerationLoraSlots]string{}
		input.LoraModel = [maxGenerationLoraSlots]float64{}
		input.LoraClip = [maxGenerationLoraSlots]float64{}
	}
	if err := a.assertGenerationPolicy(ctx, user, preset, model, input, catalog); err != nil {
		return generationPreparation{}, err
	}
	if model.Family != modelFamilyCheckpoint && definition.Builder == "" {
		definition, ok = findWorkflow(definitions, input.TemplateID+"-"+model.Family)
		if !ok {
			return generationPreparation{}, errors.New("workflow для выбранного семейства моделей не найден")
		}
	}
	if resolveSeed && input.Seed < 0 {
		input.Seed, err = randomSeed()
		if err != nil {
			return generationPreparation{}, errors.New("не удалось подготовить случайный seed")
		}
	}
	if definition.RequiresImage || (definition.AllowsImages && input.imageCount() > 0) {
		maxImages := maxGenerationInputImages(model.Family)
		if input.imageCount() > maxImages {
			return generationPreparation{}, fmt.Errorf("для этого workflow доступно не более %d изображений", maxImages)
		}
		for _, image := range input.images() {
			if err := a.validateGenerationImage(image, user.ID); err != nil {
				return generationPreparation{}, err
			}
		}
	}
	if model.Family == modelFamilyMiniMaxH3 {
		if err := a.resolveMiniMaxH3ReferenceDimensions(ctx, &input); err != nil {
			return generationPreparation{}, err
		}
	}
	if strings.TrimSpace(input.InputAudio) != "" {
		if model.Family != modelFamilyMiniMaxH3 {
			return generationPreparation{}, errors.New("аудиореференс доступен только для MiniMax H3")
		}
		if err := a.validateGenerationAudio(input.InputAudio, user.ID); err != nil {
			return generationPreparation{}, err
		}
	}
	if strings.TrimSpace(input.InputVideo) != "" {
		if model.Family != modelFamilyMiniMaxH3 {
			return generationPreparation{}, errors.New("видеореференс доступен только для MiniMax H3")
		}
		if err := a.validateGenerationVideo(input.InputVideo, user.ID); err != nil {
			return generationPreparation{}, err
		}
	}
	prompt, err := definition.buildPrompt(input)
	if err != nil {
		return generationPreparation{}, err
	}
	if issues := validateComfyPrompt(catalog.ObjectInfo.Schema, prompt); len(issues) > 0 {
		return generationPreparation{}, &workflowCompatibilityError{Issues: issues}
	}
	return generationPreparation{Input: input, Definition: definition, Model: model, Prompt: prompt, ObjectInfo: catalog.ObjectInfo}, nil
}

// requiresImageEditingSupport distinguishes an image-edit pipeline from a
// workflow that merely consumes a reference frame, such as MiniMax H3.
func requiresImageEditingSupport(definition workflowDefinition) bool {
	return definition.RequiresImage && definition.Builder != "minimax_h3"
}

func (a *App) generationRunResponse(ctx context.Context, promptID string) map[string]any {
	response := map[string]any{"prompt_id": promptID, "message": "Генерация поставлена в очередь", "state": "queued"}
	if queued, running, position, total, err := a.generationQueueState(ctx, promptID); err == nil {
		if running {
			response["state"] = "running"
		} else if queued {
			response["queue_position"] = position
			response["queue_total"] = total
			if overview, overviewErr := a.generationQueueOverview(ctx); overviewErr == nil && overview.AverageTaskSeconds > 0 {
				response["estimated_wait_seconds"] = (position + overview.Running) * overview.AverageTaskSeconds
			}
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
	job, err := a.store.GenerationJobByRequest(r.Context(), user.ID, requestID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось восстановить запуск")
		return
	}
	if job.PromptID == "" && !job.State.Terminal() {
		recovered, found, recoverErr := a.recoverGenerationJobPrompt(r.Context(), job)
		if recoverErr != nil {
			log.Printf("recover generation job %s prompt: %v", job.PublicID, recoverErr)
		} else if found {
			job = recovered
		}
	}
	if job.PromptID != "" && !job.State.Terminal() {
		if status, statusErr := a.fetchGenerationStatus(r.Context(), job.PromptID, user.ID); statusErr == nil {
			if updated, reconcileErr := a.reconcileGenerationJobStatus(r.Context(), job, status); reconcileErr == nil {
				job = updated
			} else {
				log.Printf("reconcile recovered generation job %s: %v", job.PublicID, reconcileErr)
			}
		}
	}
	response := a.generationJobResponse(r.Context(), job)
	statusCode := http.StatusOK
	if job.PromptID == "" && !job.State.Terminal() {
		statusCode = http.StatusAccepted
		response["message"] = "Запуск ещё подтверждается. Проверяем очередь ComfyUI."
	}
	writeJSON(w, statusCode, response)
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
	job, err := a.store.GenerationJobByPromptID(r.Context(), promptID)
	if errors.Is(err, sql.ErrNoRows) {
		a.handleLegacyCancelGeneration(w, r, user, promptID)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить задание генерации")
		return
	}
	if job.UserID == nil || *job.UserID != user.ID {
		http.NotFound(w, r)
		return
	}
	if job.State.Terminal() {
		response := a.generationJobResponse(r.Context(), job)
		response["cancelled"] = job.State == domain.GenerationJobCancelled
		writeJSON(w, http.StatusOK, response)
		return
	}
	job, _, err = a.store.RequestGenerationJobCancellation(r.Context(), job.ID, user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusConflict, "генерацию уже нельзя отменить")
		return
	}
	job, cancelled, err := a.continueGenerationJobCancellation(r.Context(), job)
	if err != nil {
		if errors.Is(err, errGenerationCancellationNotSent) {
			if cleared, _, clearErr := a.store.ClearGenerationJobCancellation(r.Context(), job.ID, user.ID, "Генерация продолжается"); clearErr == nil {
				job = cleared
			}
		}
		writeGenerationError(w, http.StatusBadGateway, "не удалось отменить генерацию: "+err.Error())
		return
	}
	if cancelled {
		a.audit(r.Context(), &user.ID, "quick_generation_cancelled", "comfyui", nil, a.clientIP(r), r.UserAgent(), map[string]any{"prompt_id": promptID, "job_id": job.PublicID})
	}
	response := a.generationJobResponse(r.Context(), job)
	response["cancelled"] = cancelled
	statusCode := http.StatusAccepted
	if job.State.Terminal() {
		statusCode = http.StatusOK
	}
	writeJSON(w, statusCode, response)
}

func (a *App) handleLegacyCancelGeneration(w http.ResponseWriter, r *http.Request, user *User, promptID string) {
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
		err = a.interruptRunningComfyGeneration(r.Context(), promptID)
	}
	if err != nil {
		writeGenerationError(w, http.StatusBadGateway, "не удалось отменить генерацию: "+err.Error())
		return
	}
	a.releaseMiningPauseForGeneration(r.Context(), promptID)
	a.releaseComfyMemoryIfIdle(r.Context())
	a.scheduleComfyMemoryRelease()
	a.generationMu.Lock()
	delete(a.generationJobs, promptID)
	a.generationMu.Unlock()
	a.syncGenerationAuditState(r.Context(), user.ID, promptID, "cancelled")
	message := "Генерация уже завершилась."
	if queued || running {
		message = "Генерация отменена."
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": queued || running, "message": message})
}

func (a *App) cancelQueuedComfyGeneration(ctx context.Context, promptID string) error {
	return a.postComfyGenerationControl(ctx, "/queue", map[string]any{"delete": []string{promptID}})
}

func (a *App) interruptRunningComfyGeneration(ctx context.Context, promptID string) error {
	if !validComfyPromptID(promptID) {
		return errors.New("invalid ComfyUI prompt id")
	}
	return a.postComfyGenerationControl(ctx, "/api/jobs/"+promptID+"/cancel", nil)
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
	return a.quickGenerationUploadHandlerWithLimit(maxComfyUploadBody)
}

func (a *App) quickGenerationAudioUploadHandler() http.Handler {
	return a.quickGenerationUploadHandlerWithLimit(32 << 20)
}

func (a *App) quickGenerationVideoUploadHandler() http.Handler {
	return a.quickGenerationUploadHandlerWithLimit(512 << 20)
}

func (a *App) quickGenerationUploadHandlerWithLimit(maxBody int64) http.Handler {
	proxy := a.proxyRootHandler("comfyui", a.cfg.ComfyUIUpstream, a.cfg.ComfyUIUpstreamAuthHeader)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
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
	job, err := a.store.GenerationJobByPromptID(r.Context(), promptID)
	if errors.Is(err, sql.ErrNoRows) {
		a.handleLegacyGenerateStatus(w, r, user, promptID)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить задание генерации")
		return
	}
	if job.UserID == nil || *job.UserID != user.ID {
		http.NotFound(w, r)
		return
	}
	if job.State.Terminal() {
		writeJSON(w, http.StatusOK, generationStatusForJob(job, generationStatus{}))
		return
	}
	if job.CancellationRequestedAt != nil {
		updated, _, cancelErr := a.continueGenerationJobCancellation(r.Context(), job)
		if cancelErr != nil {
			log.Printf("continue cancellation for generation job %s: %v", job.PublicID, cancelErr)
		} else {
			job = updated
		}
		writeJSON(w, http.StatusOK, generationStatusForJob(job, generationStatus{}))
		return
	}
	status, err := a.fetchGenerationStatus(r.Context(), promptID, user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusBadGateway, "не удалось получить состояние генерации: "+err.Error())
		return
	}
	if projectionErr := a.ensureGenerationJobProjections(r.Context(), job); projectionErr != nil {
		log.Printf("ensure generation job %s projections: %v", job.PublicID, projectionErr)
	}
	updated, reconcileErr := a.reconcileGenerationJobStatus(r.Context(), job, status)
	if reconcileErr != nil {
		log.Printf("reconcile generation job %s status: %v", job.PublicID, reconcileErr)
	} else {
		job = updated
	}
	writeJSON(w, http.StatusOK, generationStatusForJob(job, status))
}

func (a *App) handleLegacyGenerateStatus(w http.ResponseWriter, r *http.Request, user *User, promptID string) {
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
		a.syncGenerationAuditState(r.Context(), user.ID, promptID, status.State)
		a.releaseComfyMemoryIfIdle(r.Context())
		a.scheduleComfyMemoryRelease()
		a.releaseMiningPauseForGeneration(r.Context(), promptID)
	} else if status.State == "running" {
		a.syncGenerationAuditState(r.Context(), user.ID, promptID, status.State)
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

func (a *App) submitComfyPrompt(ctx context.Context, userID int64, jobPublicID string, prompt map[string]any) (string, error) {
	releaseAdmission, err := a.acquireComfyPromptAdmission(ctx, userID)
	if err != nil {
		return "", err
	}
	defer releaseAdmission()
	document := comfyPromptDocument(a.comfyClientID(userID), jobPublicID, prompt)
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

func comfyPromptDocument(clientID, jobPublicID string, prompt map[string]any) map[string]any {
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
	extraData := map[string]any{"extra_pnginfo": map[string]any{"workflow": workflow}}
	if jobPublicID = strings.TrimSpace(jobPublicID); jobPublicID != "" {
		extraData["gateway_job_id"] = jobPublicID
	}
	return map[string]any{
		"prompt":     prompt,
		"client_id":  clientID,
		"extra_data": extraData,
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
				status.Known = true
				status.State, status.Message = "running", "ComfyUI начал выполнение workflow"
			case queued:
				status.Known = true
				status.QueuePosition, status.QueueTotal = position, total
				if position > 0 && total > 0 {
					status.Message = fmt.Sprintf("В очереди: %d из %d", position, total)
					if overview, overviewErr := a.generationQueueOverview(ctx); overviewErr == nil && overview.AverageTaskSeconds > 0 {
						status.EstimatedWaitSeconds = (position + overview.Running) * overview.AverageTaskSeconds
					}
				}
			}
		}
		return status, nil
	}
	status.Known = true
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
		_ = a.insertComfyOutputOwnerships(ctx, userID, []domain.ComfyOutputOwnership{{
			PromptID: promptID, Filename: output.Filename, Subfolder: output.Subfolder,
			StorageType: output.Type, MediaType: output.MediaType,
		}})
	}
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
	overview := generationQueueOverview{Running: len(queue.Running), Pending: len(queue.Pending)}
	if overview.Running > 0 {
		overview.CurrentTask = "ComfyUI выполняет текущую задачу"
	} else if overview.Pending > 0 {
		overview.CurrentTask = "ComfyUI готовится к следующей задаче"
	} else {
		overview.CurrentTask = "Очередь свободна"
	}
	if a.store != nil {
		if average, averageErr := a.store.AverageGenerationDuration(ctx); averageErr == nil && average > 0 {
			overview.AverageTaskSeconds = int(average.Round(time.Second).Seconds())
			overview.EstimatedWaitSeconds = (overview.Running + overview.Pending) * overview.AverageTaskSeconds
		}
	}
	return overview, nil
}

// releaseComfyMemoryIfIdle asks ComfyUI to unload loaded models only after the
// whole queue is empty. This keeps a completed quick generation from retaining
// RAM and VRAM, without interrupting another user's queued work.
func (a *App) releaseComfyMemoryIfIdle(ctx context.Context) bool {
	overview, err := a.generationQueueOverview(ctx)
	if err != nil {
		log.Printf("inspect ComfyUI queue before freeing memory: %v", err)
		return false
	}
	if overview.Running != 0 || overview.Pending != 0 {
		return false
	}

	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/free")
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), bytes.NewBufferString(`{"unload_models":true,"free_memory":true}`))
	if err != nil {
		log.Printf("build ComfyUI memory-release request: %v", err)
		return false
	}
	request.Header.Set("Content-Type", "application/json")
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		log.Printf("free ComfyUI memory: %v", err)
		return false
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		log.Printf("free ComfyUI memory: upstream returned HTTP %d", response.StatusCode)
		return false
	}
	log.Printf("released idle ComfyUI model cache")
	return true
}

func (a *App) trimComfyWorkingSetIfIdle(ctx context.Context) {
	overview, err := a.generationQueueOverview(ctx)
	if err != nil || overview.Running != 0 || overview.Pending != 0 {
		return
	}
	result, err := a.systemMonitor.TrimComfyMemory(ctx)
	if err != nil {
		log.Printf("trim idle ComfyUI working set: %v", err)
		return
	}
	if result.Trimmed > 0 {
		log.Printf("trimmed idle ComfyUI working set for %d process(es)", result.Trimmed)
	} else if result.Message != "" {
		log.Printf("ComfyUI working set was not trimmed: %s", result.Message)
	}
}

func (a *App) observeComfyQueueForMemoryRelease(ctx context.Context) (int64, error) {
	overview, err := a.generationQueueOverview(ctx)
	if err != nil {
		return 0, err
	}
	busy := overview.Running != 0 || overview.Pending != 0
	a.comfyQueueMu.Lock()
	wasBusy := a.comfyQueueWasBusy
	a.comfyQueueWasBusy = busy
	a.comfyQueueMu.Unlock()
	if wasBusy && !busy {
		a.scheduleComfyMemoryRelease()
		return 1, nil
	}
	return 0, nil
}

func (a *App) scheduleComfyMemoryRelease() {
	if a.comfyMemorySlots == nil {
		return
	}
	select {
	case a.comfyMemorySlots <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-a.comfyMemorySlots }()
		for _, delay := range []time.Duration{8 * time.Second, 30 * time.Second} {
			time.Sleep(delay)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			released := a.releaseComfyMemoryIfIdle(ctx)
			cancel()
			if !released {
				continue
			}
			// /free wakes ComfyUI's main loop asynchronously. Let it unload model
			// references before Windows discards unused physical pages.
			time.Sleep(3 * time.Second)
			trimCtx, trimCancel := context.WithTimeout(context.Background(), 10*time.Second)
			a.trimComfyWorkingSetIfIdle(trimCtx)
			trimCancel()
		}
	}()
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
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
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
	releaseDownload, acquired := acquireBoundedSlot(r.Context(), a.mediaDownloadSlots, 2*time.Second)
	if !acquired {
		http.Error(w, "слишком много одновременных загрузок", http.StatusTooManyRequests)
		return
	}
	defer releaseDownload()
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
	http.ServeContent(w, r, media.OriginalName, time.Time{}, bytes.NewReader(payload))
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
	http.Redirect(w, r, "/gallery", http.StatusSeeOther)
}

func (a *App) fetchGenerationOutput(ctx context.Context, output generationOutput) ([]byte, string, int, error) {
	releaseDownload, acquired := acquireBoundedSlot(ctx, a.mediaDownloadSlots, 2*time.Second)
	if !acquired {
		return nil, "", 0, errors.New("too many concurrent media downloads")
	}
	defer releaseDownload()
	endpoint := *a.cfg.ComfyUIUpstream
	// /view preserves the original media bytes and supports HTTP ranges. The
	// VideoHelperSuite preview route transcodes MP4 to WebM, which breaks the
	// filename/content-type contract used by the gallery and archive.
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/view")
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

type generationOutputArchive struct {
	Body        []byte
	ContentType string
	Status      int
	SizeBytes   int64
	ContentHash string
}

func readGenerationOutputArchive(reader io.Reader, archiveLimit, fingerprintLimit int64) ([]byte, int64, string, error) {
	if archiveLimit < 0 || fingerprintLimit <= 0 || archiveLimit > fingerprintLimit {
		return nil, 0, "", errors.New("invalid generation output limits")
	}
	digest := sha256.New()
	capture := newLimitedBuffer(int(archiveLimit))
	sizeBytes, err := io.Copy(io.MultiWriter(digest, &capture), io.LimitReader(reader, fingerprintLimit+1))
	if err != nil {
		return nil, 0, "", err
	}
	if sizeBytes > fingerprintLimit {
		return nil, sizeBytes, "", errors.New("generation output exceeds fingerprint limit")
	}
	body := capture.data
	if capture.truncated {
		body = nil
	}
	return body, sizeBytes, hex.EncodeToString(digest.Sum(nil)), nil
}

func (a *App) fetchGenerationOutputArchive(ctx context.Context, output generationOutput) (generationOutputArchive, error) {
	releaseDownload, acquired := acquireBoundedSlot(ctx, a.mediaDownloadSlots, 2*time.Second)
	if !acquired {
		return generationOutputArchive{}, errors.New("too many concurrent media downloads")
	}
	defer releaseDownload()
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/view")
	query := endpoint.Query()
	query.Set("filename", output.Filename)
	query.Set("subfolder", output.Subfolder)
	query.Set("type", output.Type)
	endpoint.RawQuery = query.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return generationOutputArchive{}, err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 2 * time.Minute, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return generationOutputArchive{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return generationOutputArchive{}, fmt.Errorf("ComfyUI returned HTTP %d", response.StatusCode)
	}
	body, sizeBytes, contentHash, err := readGenerationOutputArchive(response.Body, maxCapturedMedia, maxGenerationOutputFingerprint)
	if err != nil {
		return generationOutputArchive{}, err
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return generationOutputArchive{
		Body: body, ContentType: contentType, Status: response.StatusCode,
		SizeBytes: sizeBytes, ContentHash: contentHash,
	}, nil
}

// fetchGenerationInputImage retrieves a previously validated, namespaced upload
// for the local vision assistant. The file remains inside the ComfyUI host.
func (a *App) fetchGenerationInputImage(ctx context.Context, value string) ([]byte, string, error) {
	normalized, err := normalizeComfyDataPath(value, false)
	if err != nil {
		return nil, "", err
	}
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/view")
	query := endpoint.Query()
	query.Set("filename", path.Base(normalized))
	if folder := path.Dir(normalized); folder != "." {
		query.Set("subfolder", folder)
	}
	query.Set("type", "input")
	endpoint.RawQuery = query.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return nil, "", err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 20 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("ComfyUI returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20+1))
	if err != nil || len(body) > 32<<20 {
		return nil, "", errors.New("изображение превышает допустимый размер")
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	return body, contentType, nil
}

// resolveMiniMaxH3ReferenceDimensions follows AspectRatioSimplifier: the
// source image wins when requested, while max_resolution clamps the longer
// side and all dimensions are aligned down to 32 pixels.
func (a *App) resolveMiniMaxH3ReferenceDimensions(ctx context.Context, input *generationForm) error {
	quality := input.VideoQuality
	if quality == 0 {
		quality = miniMaxH3DefaultQuality
	}
	width, height := 0, 0
	// MiniMax H3 always inherits the first picture's aspect ratio. Manual
	// presets are only a fallback for text-only or non-image reference runs.
	useSource := strings.TrimSpace(input.InputImage) != ""
	if useSource {
		body, _, err := a.fetchGenerationInputImage(ctx, input.InputImage)
		if err != nil {
			return errors.New("не удалось прочитать первый референс MiniMax H3")
		}
		width, height, err = generationImageDimensions(body)
		if err != nil {
			return errors.New("не удалось определить разрешение первого референса MiniMax H3")
		}
	} else {
		var err error
		width, height, err = miniMaxH3AspectDimensions(input.VideoAspect)
		if err != nil {
			return err
		}
	}
	if input.VideoSwapDimensions {
		width, height = height, width
	}
	var err error
	input.Width, input.Height, err = miniMaxH3VideoDimensions(width, height, quality)
	return err
}

func generationImageDimensions(body []byte) (int, int, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err == nil && config.Width > 0 && config.Height > 0 {
		return config.Width, config.Height, nil
	}
	if width, height, ok := webPImageDimensions(body); ok {
		return width, height, nil
	}
	return 0, 0, errors.New("неподдерживаемый формат изображения")
}

func webPImageDimensions(body []byte) (int, int, bool) {
	if len(body) < 30 || string(body[:4]) != "RIFF" || string(body[8:12]) != "WEBP" {
		return 0, 0, false
	}
	switch string(body[12:16]) {
	case "VP8X":
		width := 1 + int(body[24]) + int(body[25])<<8 + int(body[26])<<16
		height := 1 + int(body[27]) + int(body[28])<<8 + int(body[29])<<16
		return width, height, width > 0 && height > 0
	case "VP8 ":
		if body[23] != 0x9d || body[24] != 0x01 || body[25] != 0x2a {
			return 0, 0, false
		}
		width := int(body[26]) | int(body[27]&0x3f)<<8
		height := int(body[28]) | int(body[29]&0x3f)<<8
		return width, height, width > 0 && height > 0
	case "VP8L":
		if len(body) < 25 || body[20] != 0x2f {
			return 0, 0, false
		}
		bits := uint32(body[21]) | uint32(body[22])<<8 | uint32(body[23])<<16 | uint32(body[24])<<24
		width := int(bits&0x3fff) + 1
		height := int((bits>>14)&0x3fff) + 1
		return width, height, width > 0 && height > 0
	default:
		return 0, 0, false
	}
}

func (a *App) archiveGenerationOutputs(ctx context.Context, userID int64, outputs []generationOutput) error {
	if a.store == nil {
		return errors.New("generation archive store is unavailable")
	}
	var archiveErrors []error
	for _, output := range outputs {
		archive, err := a.fetchGenerationOutputArchive(ctx, output)
		if err != nil {
			log.Printf("archive generation output %s: %v", output.Filename, err)
			archiveErrors = append(archiveErrors, fmt.Errorf("archive %s: %w", output.Filename, err))
			continue
		}
		if output.Type == "output" {
			err = a.store.ScheduleComfyOutputCleanup(ctx, domain.ComfyOutputCleanupTombstone{
				Filename: output.Filename, Subfolder: output.Subfolder, StorageType: output.Type,
				SizeBytes: archive.SizeBytes, ContentHash: archive.ContentHash,
			}, time.Now().Add(a.retentionPolicy().GenerationMedia))
			if err != nil {
				log.Printf("schedule generation output cleanup %s: %v", output.Filename, err)
			}
		}
		if a.contentCipher == nil || archive.Body == nil {
			continue
		}
		capture := &proxyContentCapture{
			userID: userID, service: "comfyui", mediaName: output.Filename,
			mediaSubfolder: output.Subfolder, mediaStorageType: output.Type,
			mediaType: output.MediaType, mimeType: archive.ContentType, isMedia: true,
			status: archive.Status, response: newLimitedBuffer(maxCapturedMedia),
		}
		_, _ = capture.response.Write(archive.Body)
		if err := a.persistComfyMedia(ctx, capture); err != nil {
			log.Printf("persist generation output %s: %v", output.Filename, err)
			archiveErrors = append(archiveErrors, fmt.Errorf("persist %s: %w", output.Filename, err))
		}
	}
	return errors.Join(archiveErrors...)
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

func (a *App) rememberGenerationOutputs(promptID string, outputs []generationOutput) []generationOutput {
	a.generationMu.Lock()
	defer a.generationMu.Unlock()
	job := a.generationJobs[promptID]
	if job == nil {
		return outputs
	}
	if job.Outputs == nil {
		job.Outputs = make(map[string]struct{})
	}
	newOutputs := make([]generationOutput, 0, len(outputs))
	for _, output := range outputs {
		key := output.Filename + "\x00" + output.Subfolder + "\x00" + output.Type
		if _, exists := job.Outputs[key]; exists {
			continue
		}
		job.Outputs[key] = struct{}{}
		newOutputs = append(newOutputs, output)
	}
	return newOutputs
}

func (a *App) forgetGenerationOutputs(promptID string, outputs []generationOutput) {
	a.generationMu.Lock()
	defer a.generationMu.Unlock()
	job := a.generationJobs[promptID]
	if job == nil || job.Outputs == nil {
		return
	}
	for _, output := range outputs {
		delete(job.Outputs, output.Filename+"\x00"+output.Subfolder+"\x00"+output.Type)
	}
}

// refreshTrackedGenerationStatuses reconciles every durable job independently
// of the browser and survives a Gateway restart while ComfyUI keeps working.
func (a *App) refreshTrackedGenerationStatuses(ctx context.Context) (int64, error) {
	if a.store == nil {
		return 0, nil
	}
	jobs, err := a.store.ListActiveGenerationJobs(ctx, 500)
	if err != nil {
		log.Printf("load active generation jobs: %v", err)
		return 0, err
	}
	now := time.Now()
	var processed int64
	var reconciliationErrors []error
	for _, job := range jobs {
		if ctx.Err() != nil {
			return processed, errors.Join(append(reconciliationErrors, ctx.Err())...)
		}
		processed++
		if job.UserID == nil {
			a.failGenerationJob(ctx, job, "generation_owner_deleted", "Владелец задания удалён", errors.New("generation owner was deleted"))
			continue
		}
		if job.PromptID == "" {
			if job.CancellationRequestedAt != nil {
				if _, _, cancelErr := a.continueGenerationJobCancellation(ctx, job); cancelErr != nil {
					log.Printf("cancel promptless generation job %s: %v", job.PublicID, cancelErr)
					reconciliationErrors = append(reconciliationErrors, fmt.Errorf("cancel %s: %w", job.PublicID, cancelErr))
				}
				continue
			}
			recovered, found, recoverErr := a.recoverGenerationJobPrompt(ctx, job)
			if recoverErr != nil {
				log.Printf("recover generation job %s prompt: %v", job.PublicID, recoverErr)
				reconciliationErrors = append(reconciliationErrors, fmt.Errorf("recover %s: %w", job.PublicID, recoverErr))
				continue
			}
			if !found {
				if now.Sub(job.StateChangedAt) > 2*time.Minute {
					if _, expireErr := a.expireGenerationJob(ctx, job, "ComfyUI не подтвердил запуск"); expireErr != nil {
						log.Printf("expire unconfirmed generation job %s: %v", job.PublicID, expireErr)
						reconciliationErrors = append(reconciliationErrors, fmt.Errorf("expire unconfirmed %s: %w", job.PublicID, expireErr))
					}
				}
				continue
			}
			job = recovered
		}
		if projectionErr := a.ensureGenerationJobProjections(ctx, job); projectionErr != nil {
			log.Printf("ensure generation job %s projections: %v", job.PublicID, projectionErr)
			reconciliationErrors = append(reconciliationErrors, fmt.Errorf("project %s: %w", job.PublicID, projectionErr))
		}
		if job.CancellationRequestedAt != nil {
			if _, _, cancelErr := a.continueGenerationJobCancellation(ctx, job); cancelErr != nil {
				log.Printf("continue generation job %s cancellation: %v", job.PublicID, cancelErr)
				reconciliationErrors = append(reconciliationErrors, fmt.Errorf("continue cancellation %s: %w", job.PublicID, cancelErr))
			}
			continue
		}
		status, err := a.fetchGenerationStatus(ctx, job.PromptID, *job.UserID)
		if err != nil {
			log.Printf("refresh ComfyUI generation job %s: %v", job.PublicID, err)
			reconciliationErrors = append(reconciliationErrors, fmt.Errorf("refresh %s: %w", job.PublicID, err))
			continue
		}
		if !status.Known && now.Sub(job.StateChangedAt) > 5*time.Minute {
			if _, expireErr := a.expireGenerationJob(ctx, job, "Задание исчезло из очереди ComfyUI"); expireErr != nil {
				log.Printf("expire lost generation job %s: %v", job.PublicID, expireErr)
				reconciliationErrors = append(reconciliationErrors, fmt.Errorf("expire lost %s: %w", job.PublicID, expireErr))
			}
			continue
		}
		updated, reconcileErr := a.reconcileGenerationJobStatus(ctx, job, status)
		if reconcileErr != nil {
			log.Printf("reconcile generation job %s: %v", job.PublicID, reconcileErr)
			reconciliationErrors = append(reconciliationErrors, fmt.Errorf("reconcile %s: %w", job.PublicID, reconcileErr))
			continue
		}
		if updated.State.Terminal() {
			a.generationMu.Lock()
			delete(a.generationJobs, updated.PromptID)
			a.generationMu.Unlock()
		}
	}
	return processed, errors.Join(reconciliationErrors...)
}

func (a *App) syncGenerationAuditState(ctx context.Context, userID int64, promptID, state string) {
	if a.store == nil {
		return
	}
	if state != "queued" && state != "running" && state != "completed" && state != "error" && state != "cancelled" {
		return
	}
	if err := a.store.SetGenerationVariantState(ctx, userID, promptID, state); err != nil {
		log.Printf("store generation variant state %s: %v", promptID, err)
	}
	if err := a.store.SetContentGenerationState(ctx, userID, promptID, state); err != nil {
		log.Printf("store content generation state %s: %v", promptID, err)
	}
}

func (a *App) generationOwned(ctx context.Context, promptID string, userID int64) (bool, error) {
	if a.store != nil {
		job, err := a.store.GenerationJobByPromptID(ctx, promptID)
		if err == nil {
			return job.UserID != nil && *job.UserID == userID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
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
	return a.validateGenerationAsset(value, userID, "фото")
}

func (a *App) validateGenerationAudio(value string, userID int64) error {
	return a.validateGenerationAsset(value, userID, "аудиореференс")
}

func (a *App) validateGenerationVideo(value string, userID int64) error {
	return a.validateGenerationAsset(value, userID, "видеореференс")
}

func (a *App) validateGenerationAsset(value string, userID int64, label string) error {
	normalized, err := normalizeComfyDataPath(value, false)
	if err != nil {
		return fmt.Errorf("некорректный %s", label)
	}
	ownNamespace := comfyUploadNamespace(a.comfyClientID(userID))
	if !strings.HasPrefix(normalized, ownNamespace+"/") {
		return fmt.Errorf("%s принадлежит другой сессии", label)
	}
	return nil
}

func (a *App) recordGenerationEvent(ctx context.Context, jobID, userID int64, promptID string, definition workflowDefinition, input generationForm) {
	if a.contentCipher == nil || a.store == nil {
		return
	}
	metadataFields := generationAuditMetadata(definition, input)
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
	var generationJobID *int64
	if jobID > 0 {
		generationJobID = &jobID
	}
	if _, err := a.store.InsertContentEvent(ctx, domain.ContentEventRecord{UserID: userID, GenerationJobID: generationJobID, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: promptID, Model: input.ModelName, GenerationState: "queued", PromptCipher: promptCipher, ResponseCipher: negativeCipher, MetadataCipher: metadataCipher, Sensitive: isSensitiveGeneration(input), ExpiresAt: time.Now().Add(a.retentionPolicy().AIContent)}); err != nil {
		log.Printf("store generation event: %v", err)
	}
}

func generationAuditMetadata(definition workflowDefinition, input generationForm) map[string]any {
	sensitive := isSensitiveGeneration(input)
	metadata := map[string]any{
		"workflow": definition.ID, "preset": input.PresetID, "model_family": input.ModelFamily,
		"model": input.ModelName, "width": input.Width, "height": input.Height,
		"steps": input.Steps, "cfg": input.CFG, "denoise": input.Denoise, "seed": input.Seed,
		"sampler": input.Sampler, "scheduler": input.Scheduler,
		"aspect_ratio": input.AspectRatio, "output_megapixels": input.OutputMegapixels,
		"base_megapixels": input.BaseMegapixels, "lora_strength": input.LoraStrength,
		"upscale_steps": input.UpscaleSteps, "upscale_denoise": input.UpscaleDenoise,
		"detail_steps": input.DetailSteps, "detail_denoise": input.DetailDenoise,
		"input_images":      input.imageCount(),
		"sensitive_content": sensitive,
	}
	loras := make([]map[string]any, 0, maxGenerationLoraSlots)
	for index, name := range input.LoraNames {
		if strings.TrimSpace(name) == "" {
			continue
		}
		loras = append(loras, map[string]any{
			"slot": index + 1, "name": name,
			"model_strength": input.LoraModel[index], "clip_strength": input.LoraClip[index],
		})
	}
	if len(loras) > 0 {
		metadata["loras"] = loras
	}
	switch definition.ID {
	case "text-to-image-krea2":
		metadata["krea2_text"] = map[string]any{
			"base_megapixels": input.BaseMegapixels,
			"upscale":         map[string]any{"steps": input.UpscaleSteps, "denoise": input.UpscaleDenoise, "auto_denoise": input.UpscaleAutoDenoise, "sampler": input.UpscaleSampler},
			"detail":          map[string]any{"steps": input.DetailSteps, "denoise": input.DetailDenoise, "cfg": input.DetailCFG, "sampler": input.DetailSampler, "scheduler": input.DetailScheduler},
			"color_transfer":  map[string]any{"enabled": input.ColorTransfer, "method": input.ColorMethod, "mode": input.ColorMode, "strength": input.ColorStrength},
		}
	case "image-to-image-krea2":
		metadata["krea2_edit"] = map[string]any{
			"preserve_original_size": input.PreserveOriginalSize, "reference_boost": input.ReferenceBoost, "grounding_pixels": input.GroundingPixels,
			"upscale":      map[string]any{"factor": input.UpscaleFactor, "steps": input.UpscaleSteps, "denoise": input.UpscaleDenoise, "cfg": input.UpscaleCFG, "sampler": input.UpscaleSampler, "scheduler": input.UpscaleScheduler},
			"post_denoise": map[string]any{"blur": input.PostDenoiseBlur, "edge": input.PostDenoiseEdge, "radius": input.PostDenoiseRadius, "strength": input.PostDenoiseStrength},
			"skin":         map[string]any{"preset": input.SkinPreset, "strength": input.SkinStrength, "coolness": input.SkinCoolness, "brightness": input.SkinBrightness, "rosy": input.SkinRosy, "evenness": input.SkinEvenness, "shadow_lift": input.SkinShadowLift, "smooth": input.SkinSmooth, "texture_preserve": input.SkinTexturePreserve, "saturation": input.SkinSaturation, "highlight_protect": input.SkinHighlightProtect, "mask_sensitivity": input.SkinMaskSensitivity, "mask_feather": input.SkinMaskFeather},
			"adjust":       map[string]any{"hue": input.AdjustHue, "saturation": input.AdjustSaturation, "brightness": input.AdjustBrightness, "contrast": input.AdjustContrast, "sharpness": input.AdjustSharpness},
			"lut":          map[string]any{"enabled": input.LUTEnabled, "name": input.LUTName, "strength": input.LUTStrength},
		}
	case "image-to-image-flux2":
		metadata["flux2_edit"] = map[string]any{
			"source_megapixels": input.SourceMegapixels, "preserve_original_size": input.PreserveOriginalSize,
			"frame":        map[string]any{"custom_size": input.EditUseCustomSize, "aspect": input.EditAspectPreset, "swap": input.EditSwapDimensions, "resize_method": input.EditResizeMethod, "proportion": input.EditProportion, "crop": input.EditCropLocation, "pad_color": input.EditPadColor, "max_longest_side": input.MaxLongestSide},
			"conditioning": map[string]any{"guidance": input.FluxGuidance, "detailer_steps": input.FluxDetailerSteps, "active_scale": input.FluxActiveScale, "token_whiten": input.FluxTokenWhiten, "norm_equalize": input.FluxNormEqualize},
			"upscale_mode": input.FluxUpscaleMode,
			"lut":          map[string]any{"enabled": input.LUTEnabled, "name": input.LUTName, "strength": input.LUTStrength},
		}
	case "minimax-h3-video":
		metadata["minimax_h3"] = map[string]any{
			"mode": input.VideoMode, "resolution": input.VideoResolution, "quality": input.VideoQuality,
			"aspect": input.VideoAspect, "source_aspect": input.VideoUseSourceAspect, "swap_dimensions": input.VideoSwapDimensions,
			"frame_fit":        map[string]any{"method": input.VideoResizeMethod, "proportion": input.VideoProportion, "crop": input.VideoCropLocation, "pad_color": input.VideoPadColor},
			"duration_seconds": input.VideoDurationSeconds, "reference_size": input.VideoReferenceSize,
			"video_reference": map[string]any{"attached": strings.TrimSpace(input.InputVideo) != "", "start": input.VideoReferenceStart, "duration": input.VideoReferenceDuration, "use_audio": input.VideoReferenceAudio},
			"steps":           input.VideoSteps, "turbo": input.VideoTurbo, "integrated_turbo": input.VideoIntegratedTurbo, "sampler": input.VideoSampler, "scheduler": input.VideoScheduler,
			"shift_video": input.VideoShiftVideo, "shift_audio": input.VideoShiftAudio,
			"sage_attention": input.VideoSageAttention, "clear_vram": input.VideoClearVRAM,
			"memory_optimization": map[string]any{"enabled": input.VideoMemoryOptimize, "mlp": input.VideoMemoryMLP, "chunk_rows": input.VideoMemoryChunkRows, "precision": input.VideoMemoryPrecision, "qkv_streaming": input.VideoMemoryQKV, "attention_memory": input.VideoMemoryAttention},
			"aimdo_residency":     map[string]any{"enabled": input.VideoAIMDOEnabled, "residency": input.VideoAIMDOResidency},
			"sparse_attention":    map[string]any{"enabled": input.VideoSparseAttention, "budget": input.VideoSparseBudget, "early_schedule": input.VideoSparseSchedule, "early_steps": input.VideoSparseEarlyStep, "early_kv": input.VideoSparseEarlyKV, "late_steps": input.VideoSparseLateStep, "late_kv": input.VideoSparseLateKV, "backend": input.VideoSparseBackend},
			"rife":                map[string]any{"enabled": input.VideoRIFEEnabled, "checkpoint": input.VideoRIFECheckpoint, "multiplier": input.VideoRIFEMultiplier, "fast_mode": input.VideoRIFEFastMode, "ensemble": input.VideoRIFEEnsemble, "dtype": input.VideoRIFEDtype, "compile": input.VideoRIFECompile, "batch_size": input.VideoRIFEBatchSize},
			"rtx":                 map[string]any{"enabled": input.VideoRTXEnabled, "scale": input.VideoRTXScale, "quality": input.VideoRTXQuality},
			"color_match":         map[string]any{"enabled": input.VideoColorMatch, "method": input.VideoColorMethod, "strength": input.VideoColorStrength},
			"sharpen":             map[string]any{"enabled": input.VideoSharpenEnabled, "method": input.VideoSharpenMethod, "strength": input.VideoSharpenStrength, "radius": input.VideoSharpenRadius, "threshold": input.VideoSharpenThreshold, "iterations": input.VideoSharpenIterations},
			"audio_start":         input.VideoAudioStart, "output_crf": input.VideoOutputCRF, "filename": input.VideoFilename,
		}
	}
	if input.AssistantRequested {
		metadata["prompt_assistant"] = map[string]any{
			"requested": true, "applied": input.AssistantApplied, "template": input.AssistantTemplate,
			"think": input.AssistantThink, "original_prompt": input.AssistantOriginal, "suggestion": input.AssistantSuggestion,
		}
	}
	return metadata
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
