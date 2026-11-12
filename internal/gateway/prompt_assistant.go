package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/promptassistant"
)

const (
	maxPromptAssistantReferenceBytes = 64 << 20
	promptAssistantMemoryReservation = 192 << 20
	// A 64 MP, 16-bit RGBA input can require 512 MiB before downscaling.
	promptAssistantImageMemoryReservation = 576 << 20
)

var qwen21ReferenceMention = regexp.MustCompile(`(?i)<?\b(?:picture|image|photo)\s*#?\s*(10|[1-9])\b>?`)

func promptAssistantSupportsNegative(mode promptassistant.Mode, profile promptassistant.Profile, workflow string) bool {
	if mode != promptassistant.ModeTextToImage && mode != promptassistant.ModeImageToImage {
		return false
	}
	if profile == promptassistant.ProfileQwen21Edit || workflow == kreaProPresetID {
		return true
	}
	if mode == promptassistant.ModeImageToImage {
		return workflow == "qwen-image21-edit"
	}
	return workflow == kreaUltraPresetID || workflow == mossPresetID || workflow == "qwen-image21"
}

func normalizeQwen21ReferenceMentions(prompt string, referenceCount int) string {
	return qwen21ReferenceMention.ReplaceAllStringFunc(prompt, func(mention string) string {
		parts := qwen21ReferenceMention.FindStringSubmatch(mention)
		index, err := strconv.Atoi(parts[1])
		if err != nil || index > referenceCount {
			return mention
		}
		return fmt.Sprintf("<image%d>", index)
	})
}

type promptAssistantAuditMetadata struct {
	PromptAssistant promptAssistantAudit `json:"prompt_assistant"`
}

type promptAssistantAudit struct {
	LongParts          int                                      `json:"long_parts,omitempty"`
	Requested          bool                                     `json:"requested"`
	Applied            bool                                     `json:"applied"`
	Decision           string                                   `json:"decision,omitempty"`
	Template           promptassistant.Profile                  `json:"template"`
	Think              bool                                     `json:"think"`
	OriginalPrompt     string                                   `json:"original_prompt"`
	OriginalNegative   string                                   `json:"original_negative,omitempty"`
	Suggestion         string                                   `json:"suggestion"`
	NegativeSuggestion string                                   `json:"negative_suggestion,omitempty"`
	AudioSuggestion    string                                   `json:"audio_suggestion,omitempty"`
	FinalPrompt        string                                   `json:"final_prompt,omitempty"`
	FinalNegative      string                                   `json:"final_negative,omitempty"`
	Mode               promptassistant.Mode                     `json:"mode"`
	ReferenceCount     int                                      `json:"reference_count"`
	References         []promptassistant.ReferenceUnderstanding `json:"references"`
	Usage              promptassistant.Usage                    `json:"usage"`
	Policy             promptassistant.RequestPolicy            `json:"policy"`
	LatencyMS          int64                                    `json:"latency_ms"`
	Status             string                                   `json:"status"`
	ErrorCode          string                                   `json:"error_code,omitempty"`
}

func (a *App) handlePromptAssistant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeGenerationError(w, http.StatusMethodNotAllowed, "метод не поддерживается")
		return
	}
	durable := r.URL.Path == "/generate/prompt-assistant/jobs"
	if durable && (!a.cfg.PromptAssistantDurableJobs || a.gpuCoordinator == nil) {
		writeGenerationError(w, http.StatusConflict, "приём новых запросов ассистента временно приостановлен; сохранённые ответы остаются доступны")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRequest)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	mode := promptassistant.Mode(strings.TrimSpace(r.Form.Get("template_id")))
	if mode != promptassistant.ModeTextToImage && mode != promptassistant.ModeImageToImage && mode != promptassistant.ModeTextToVideo {
		writeGenerationError(w, http.StatusBadRequest, "неизвестный режим генерации")
		return
	}
	prompt := strings.TrimSpace(r.Form.Get("prompt"))
	longParts := 0
	if r.Form.Get("video_long_enabled") == "true" {
		video, err := promptAssistantVideoContext(r, mode, 0)
		if err != nil {
			writeGenerationError(w, http.StatusBadRequest, err.Error())
			return
		}
		longParts = video.LongParts
	}
	promptLimit := promptassistant.PromptCharacterLimitForVideo(mode, longParts)
	if utf8.RuneCountInString(prompt) > promptLimit {
		writeGenerationError(w, http.StatusBadRequest, fmt.Sprintf("введите промт длиной до %d символов", promptLimit))
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeGenerationError(w, http.StatusUnauthorized, "требуется вход")
		return
	}
	if err := validateGenerationPrompt(prompt); err != nil {
		a.audit(r.Context(), &user.ID, "generation_safety_blocked", "prompt_assistant", nil, a.clientIP(r), r.UserAgent(), map[string]any{"reason": "minor_sexual_content"})
		writeGenerationError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if !user.CanUseQuickGenerationType(promptAssistantTemplateID(mode)) {
		writeGenerationError(w, http.StatusForbidden, "этот тип быстрой генерации недоступен")
		return
	}
	profile := promptassistant.Profile(strings.TrimSpace(r.Form.Get("assistant_template")))
	if profile == "" {
		profile = promptassistant.ProfileWorkflowDefault
	}
	if mode == promptassistant.ModeImageToImage {
		profile = imageEditAssistantProfile(r.Form.Get("generation_workflow"), profile)
	}
	if !promptassistant.ValidProfile(mode, profile) {
		writeGenerationError(w, http.StatusBadRequest, "неизвестный шаблон промт-ассистента")
		return
	}
	if longParts > 0 && !promptassistant.IsMiniMaxH3Profile(profile) {
		writeGenerationError(w, http.StatusBadRequest, "для длинного видео выберите промт-ассистент MiniMax H3 REF2VA")
		return
	}
	edit := promptassistant.EditContext{}
	if mode == promptassistant.ModeTextToImage && r.Form.Get("generation_workflow") == mossPresetID {
		edit.ImageFamily = modelFamilyMoss
	}
	if promptAssistantSupportsNegative(mode, profile, r.Form.Get("generation_workflow")) {
		edit.NegativePrompt = strings.TrimSpace(r.Form.Get("negative_prompt"))
		edit.CFG = 1
		if value := strings.TrimSpace(r.Form.Get("cfg")); value != "" {
			parsedCFG, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil || !(parsedCFG >= 1 && parsedCFG <= 30) || parsedCFG == 1 && edit.NegativePrompt != "" {
				writeGenerationError(w, http.StatusBadRequest, "негативный промт работает только при CFG больше 1")
				return
			}
			edit.CFG = parsedCFG
		}
		if utf8.RuneCountInString(edit.NegativePrompt) > 4000 {
			writeGenerationError(w, http.StatusBadRequest, "негативный промт слишком длинный")
			return
		}
		if err := validateGenerationPrompt(edit.NegativePrompt); err != nil {
			writeGenerationError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	if a.promptAssistant == nil || !a.promptAssistant.Configured() {
		status := http.StatusServiceUnavailable
		if durable {
			status = http.StatusConflict
		}
		writeGenerationError(w, status, "локальный промт-ассистент не настроен")
		return
	}
	modelID := r.Form.Get("assistant_model")
	assistant, modelErr := a.promptAssistantForUser(r.Context(), user.ID, modelID)
	if modelErr != nil {
		status := http.StatusServiceUnavailable
		if durable {
			status = http.StatusConflict
		}
		message := "не удалось проверить доступ к модели ассистента"
		if errors.Is(modelErr, errAssistantModelUnknown) {
			status, message = http.StatusBadRequest, modelErr.Error()
		} else if errors.Is(modelErr, errAssistantModelDenied) {
			status, message = http.StatusForbidden, modelErr.Error()
		}
		writeGenerationError(w, status, message)
		return
	}
	if modelID != "" && !durable {
		writeGenerationError(w, http.StatusServiceUnavailable, "выбор модели требует доступной очереди с восстановлением ответа")
		return
	}
	releaseAssistant, acquired := acquireBoundedSlot(r.Context(), a.promptAssistantSlots, time.Second)
	if !acquired {
		writeGenerationError(w, http.StatusTooManyRequests, "промт-ассистент уже обрабатывает другой запрос")
		return
	}
	defer releaseAssistant()
	releaseMedia, acquired := a.mediaByteLimiter().tryAcquire(promptAssistantMediaReservation(r, mode))
	if !acquired {
		writeGenerationError(w, http.StatusTooManyRequests, "обработка медиа уже заняла доступный объём памяти; повторите запрос позже")
		return
	}
	defer releaseMedia()
	references, err := a.promptAssistantImageReferences(r.Context(), user.ID, r, mode)
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer releasePromptAssistantImages(references)
	if prompt == "" && (mode != promptassistant.ModeTextToVideo || len(references) == 0) {
		writeGenerationError(w, http.StatusBadRequest, fmt.Sprintf("введите промт длиной до %d символов", promptLimit))
		return
	}
	thinkValue := r.Form.Get("assistant_think")
	if thinkValue != "" && thinkValue != "true" && thinkValue != "false" {
		writeGenerationError(w, http.StatusBadRequest, "некорректное значение режима рассуждений")
		return
	}
	think := thinkValue == "true"
	video := promptassistant.VideoContext{}
	operation := "enhance_image"
	if promptassistant.IsMiniMaxH3Profile(profile) {
		operation = "enhance_video"
		video, err = promptAssistantVideoContext(r, mode, len(references))
		if err != nil {
			writeGenerationError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !promptassistant.MiniMaxH3ProfileMatchesMode(profile, video.Mode) {
			writeGenerationError(w, http.StatusBadRequest, "выбранный промт-ассистент не соответствует режиму MiniMax H3")
			return
		}
		if err := a.prepareAssistantVideo(r.Context(), user.ID, r, &video); err != nil {
			writeGenerationError(w, http.StatusBadRequest, err.Error())
			return
		}
		defer clear(video.VideoStoryboard)
		defer func() {
			for _, page := range video.VideoStoryboards {
				clear(page)
			}
		}()
	}
	if durable {
		a.enqueuePromptAssistantHTTP(w, r, *user, assistant, modelID, mode, profile, prompt, references, video, edit, think)
		return
	}
	gpuKey := newRequestID()
	gpuExecution := promptassistant.ExecutionNotDispatched
	var miningLease *domain.QuickGenerationMiningLease
	defer func() {
		a.gpuCoordinator.finishInference(domain.GPUWorkAssistant, gpuKey, gpuExecution)
		a.finishInferenceMiningPause(miningLease, gpuExecution)
	}()
	gpuWork, gpuErr := a.gpuCoordinator.wait(r.Context(), domain.GPUWorkAssistant, gpuKey, user, time.Now(), nil)
	if gpuErr != nil {
		writeGenerationError(w, http.StatusServiceUnavailable, "не удалось дождаться GPU: "+gpuErr.Error())
		return
	}
	miningLease, miningWarning, err := a.pauseMiningForInference(r.Context(), user, gpuWork)
	if err != nil {
		writeGenerationError(w, http.StatusServiceUnavailable, "не удалось освободить ресурсы для приоритетной работы ассистента: "+err.Error())
		return
	}
	policy := assistant.PolicyForVideoRequest(mode, profile, video, think, len(references) > 0 || len(video.VideoStoryboard) > 0)
	// A browser disconnect must not discard the executor's completion evidence.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), policy.Timeout)
	defer cancel()
	correlation := correlationID(r)
	assistantStarted := time.Now()
	result := promptassistant.Result{Policy: policy}
	if gpuErr := a.gpuCoordinator.dispatch(ctx, gpuWork); gpuErr != nil {
		writeGenerationError(w, http.StatusServiceUnavailable, "допуск GPU изменился, повторите запрос")
		return
	}
	ctx, stopGPUHeartbeat := a.gpuCoordinator.maintain(ctx, gpuWork)
	defer stopGPUHeartbeat()
	gpuExecution = promptassistant.ExecutionUnconfirmed
	if promptassistant.IsMiniMaxH3Profile(profile) {
		result, err = assistant.EnhanceVideoResult(ctx, mode, profile, prompt, references, video, think)
	} else {
		result, err = assistant.EnhanceEditResult(ctx, mode, profile, prompt, references, edit, think)
	}
	gpuExecution = result.Execution
	latency := time.Since(assistantStarted)
	auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancelAudit()
	a.observeServiceCall(auditCtx, dependencyOllama, operation, assistantStarted, err, false, "assistant_request_failed", "")
	if err != nil {
		errorCode := promptAssistantFailureCode(err)
		a.recordPromptAssistantEvent(auditCtx, user.ID, correlation, mode, profile, prompt, edit.NegativePrompt, result, think, latency, "error", errorCode)
		logGateway(r.Context(), slog.LevelError, "prompt_assistant_failed", "Prompt assistant request failed",
			"operation", operation,
			"error_code", errorCode,
			"error", err,
		)
		writeGenerationError(w, http.StatusBadGateway, "не удалось получить вариант от локальной модели")
		return
	}
	if profile == promptassistant.ProfileQwen21Edit {
		result.Prompt = normalizeQwen21ReferenceMentions(result.Prompt, len(references))
	}
	if edit.CFG == 1 {
		result.NegativePrompt = ""
	}
	if err := validateGenerationPrompt(result.Prompt + "\n" + result.AudioPrompt + "\n" + result.NegativePrompt); err != nil {
		a.recordPromptAssistantEvent(auditCtx, user.ID, correlation, mode, profile, prompt, edit.NegativePrompt, result, think, latency, "blocked", "unsafe_model_output")
		a.audit(auditCtx, &user.ID, "generation_safety_blocked", "prompt_assistant", nil, a.clientIP(r), r.UserAgent(), map[string]any{"reason": "minor_sexual_content", "source": "assistant_response"})
		writeGenerationError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	a.recordPromptAssistantEvent(auditCtx, user.ID, correlation, mode, profile, prompt, edit.NegativePrompt, result, think, latency, "completed", "")
	assistantModel := strings.TrimSpace(result.Model)
	if assistantModel == "" {
		assistantModel = a.cfg.PromptAssistantModel
	}
	response := map[string]any{
		"prompt": result.Prompt, "negative_prompt": result.NegativePrompt, "audio_prompt": result.AudioPrompt, "references": result.References, "model": assistantModel,
		"correlation_id": correlation, "usage": result.Usage, "policy": result.Policy,
	}
	if miningWarning != "" {
		response["mining_warning"] = miningWarning
	}
	if miningLease != nil && miningLease.ResumeMining && miningWarning == "" {
		response["mining_paused"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

func imageEditAssistantProfile(workflow string, profile promptassistant.Profile) promptassistant.Profile {
	switch workflow {
	case "qwen-image21-edit":
		return promptassistant.ProfileQwen21Edit
	case "photoflow-flux2-edit":
		if profile == promptassistant.ProfileWorkflowDefault || profile == promptassistant.ProfileKrea2Edit || profile == promptassistant.ProfileQwen21Edit {
			return promptassistant.ProfileFluxEdit
		}
	case "photoflow-krea2-edit", kreaProPresetID:
		if profile == promptassistant.ProfileWorkflowDefault || profile == promptassistant.ProfileFluxEdit || profile == promptassistant.ProfileQwen21Edit {
			return promptassistant.ProfileKrea2Edit
		}
	}
	return profile
}

func promptAssistantContentState(status string) string {
	switch status {
	case "completed", "error", "cancelled":
		return status
	default:
		// Safety outcomes remain detailed in the audit/metrics status, while
		// content history uses its existing lifecycle vocabulary.
		return "error"
	}
}

func (a *App) recordPromptAssistantEvent(ctx context.Context, userID int64, correlation string, mode promptassistant.Mode, profile promptassistant.Profile, prompt, originalNegative string, result promptassistant.Result, think bool, latency time.Duration, status, errorCode string) {
	if a.contentCipher == nil || a.store == nil {
		return
	}
	metadata, err := json.Marshal(promptAssistantAuditMetadata{PromptAssistant: promptAssistantAudit{
		Requested: true, Template: profile, Think: think, OriginalPrompt: prompt, OriginalNegative: originalNegative, Suggestion: result.Prompt, NegativeSuggestion: result.NegativePrompt, AudioSuggestion: result.AudioPrompt,
		Mode: mode, LongParts: result.LongParts, ReferenceCount: len(result.References), References: result.References, Usage: result.Usage,
		Policy: result.Policy, LatencyMS: latency.Milliseconds(), Status: status, ErrorCode: errorCode,
	}})
	if err != nil {
		return
	}
	promptCipher, promptErr := a.contentCipher.Encrypt(prompt)
	responseCipher, responseErr := a.contentCipher.Encrypt(result.Prompt)
	metadataCipher, metadataErr := a.contentCipher.Encrypt(string(metadata))
	if promptErr != nil || responseErr != nil || metadataErr != nil {
		logGateway(ctx, slog.LevelError, "prompt_assistant_audit_encrypt_failed", "Prompt assistant audit encryption failed")
		return
	}
	assistantModel := strings.TrimSpace(result.Model)
	if assistantModel == "" {
		assistantModel = a.cfg.PromptAssistantModel
	}
	eventID, err := a.store.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: userID, CorrelationID: correlation, Service: "ollama", Kind: "prompt_assistant", ExternalID: newRequestID(),
		Model: assistantModel, GenerationState: promptAssistantContentState(status), PromptCipher: promptCipher,
		ResponseCipher: responseCipher, MetadataCipher: metadataCipher, ExpiresAt: time.Now().Add(a.retentionPolicy().AIContent),
	})
	if err != nil {
		logGateway(ctx, slog.LevelError, "prompt_assistant_audit_store_failed", "Prompt assistant audit storage failed", "error", err)
		return
	}
	if _, err := a.store.InsertPromptAssistantRun(ctx, domain.PromptAssistantRunRecord{
		ContentEventID: eventID, UserID: userID, CorrelationID: correlation, Mode: string(mode), Profile: string(profile),
		Model: assistantModel, Status: status, LatencyMS: latency.Milliseconds(),
		PromptTokens: result.Usage.PromptTokens, CompletionTokens: result.Usage.CompletionTokens,
		TotalDurationMS: result.Usage.TotalDurationMS, LoadDurationMS: result.Usage.LoadDurationMS,
		EvalDurationMS: result.Usage.CompletionTimeMS, NumPredict: result.Policy.NumPredict,
		TimeoutMS: result.Policy.TimeoutMS, KeepAlive: result.Policy.KeepAlive,
		ReferenceCount: len(result.References), ErrorCode: errorCode,
	}); err != nil {
		logGateway(ctx, slog.LevelError, "prompt_assistant_metrics_store_failed", "Prompt assistant metrics storage failed", "error", err)
	}
}

func promptAssistantFailureCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "структурирован") || strings.Contains(message, "не описала референс"):
		return "invalid_structure"
	case strings.Contains(message, "слишком большой"):
		return "response_too_large"
	case strings.Contains(message, "http"):
		return "model_http_error"
	default:
		return "model_request_failed"
	}
}

func (a *App) handlePromptAssistantDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeGenerationError(w, http.StatusMethodNotAllowed, "метод не поддерживается")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeGenerationError(w, http.StatusUnauthorized, "требуется вход")
		return
	}
	correlation := strings.TrimSpace(r.Form.Get("correlation_id"))
	if !validCorrelationID(correlation) {
		writeGenerationError(w, http.StatusBadRequest, "некорректный идентификатор варианта")
		return
	}
	decision := strings.TrimSpace(r.Form.Get("decision"))
	if decision != "applied" && decision != "edited_after_apply" && decision != "kept_original" {
		writeGenerationError(w, http.StatusBadRequest, "некорректное решение по варианту")
		return
	}
	finalPrompt := strings.TrimSpace(r.Form.Get("final_prompt"))
	finalNegative := strings.TrimSpace(r.Form.Get("final_negative_prompt"))
	eventID, metadataCipher, err := a.store.PromptAssistantEventMetadata(r.Context(), user.ID, correlation)
	if errors.Is(err, sql.ErrNoRows) {
		writeGenerationError(w, http.StatusNotFound, "вариант ассистента уже недоступен")
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось прочитать вариант ассистента")
		return
	}
	metadataJSON, err := a.contentCipher.Decrypt(metadataCipher)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось прочитать вариант ассистента")
		return
	}
	var metadata promptAssistantAuditMetadata
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil || !metadata.PromptAssistant.Requested {
		writeGenerationError(w, http.StatusConflict, "данные варианта ассистента повреждены")
		return
	}
	promptLimit := promptassistant.PromptCharacterLimitForVideo(metadata.PromptAssistant.Mode, metadata.PromptAssistant.LongParts)
	if utf8.RuneCountInString(finalPrompt) > promptLimit {
		writeGenerationError(w, http.StatusBadRequest, fmt.Sprintf("итоговый промт длиннее %d символов", promptLimit))
		return
	}
	if metadata.PromptAssistant.Template == promptassistant.ProfileQwen21Edit {
		if utf8.RuneCountInString(finalNegative) > 4000 {
			writeGenerationError(w, http.StatusBadRequest, "негативный промт слишком длинный")
			return
		}
		if err := validateGenerationPrompt(finalNegative); err != nil {
			writeGenerationError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	} else {
		finalNegative = ""
	}
	if decision != "kept_original" {
		if finalPrompt == "" {
			writeGenerationError(w, http.StatusBadRequest, "итоговый промт пуст")
			return
		}
		if err := validateGenerationPrompt(finalPrompt); err != nil {
			writeGenerationError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if metadata.PromptAssistant.LongParts > 0 {
			if err := promptassistant.ValidateLongVideoTimeline(finalPrompt, metadata.PromptAssistant.LongParts); err != nil {
				writeGenerationError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}
	if decision == "kept_original" {
		finalPrompt = metadata.PromptAssistant.OriginalPrompt
		finalNegative = metadata.PromptAssistant.OriginalNegative
	} else if finalPrompt != metadata.PromptAssistant.Suggestion || finalNegative != metadata.PromptAssistant.NegativeSuggestion {
		decision = "edited_after_apply"
	}
	metadata.PromptAssistant.Decision = decision
	metadata.PromptAssistant.Applied = decision != "kept_original"
	metadata.PromptAssistant.FinalPrompt = finalPrompt
	metadata.PromptAssistant.FinalNegative = finalNegative
	updatedJSON, err := json.Marshal(metadata)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось сохранить решение")
		return
	}
	updatedCipher, err := a.contentCipher.Encrypt(string(updatedJSON))
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось сохранить решение")
		return
	}
	if err := a.store.SetPromptAssistantDecision(r.Context(), user.ID, eventID, decision, updatedCipher); err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось сохранить решение")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decision": decision})
}

func releasePromptAssistantImages(references []promptassistant.ImageReference) {
	totalBytes := 0
	for index := range references {
		totalBytes += len(references[index].Image)
		clear(references[index].Image)
		references[index].Image = nil
	}
	if totalBytes >= 8<<20 {
		debug.FreeOSMemory()
	}
}

func promptAssistantTemplateID(mode promptassistant.Mode) string {
	switch mode {
	case promptassistant.ModeTextToImage:
		return "text-to-image"
	case promptassistant.ModeImageToImage:
		return "image-to-image"
	case promptassistant.ModeTextToVideo:
		return "minimax-h3-video"
	default:
		return ""
	}
}

func promptAssistantVideoContext(r *http.Request, mode promptassistant.Mode, imageCount int) (promptassistant.VideoContext, error) {
	if mode != promptassistant.ModeTextToVideo {
		return promptassistant.VideoContext{}, fmt.Errorf("шаблон MiniMax H3 доступен только для видео")
	}
	videoMode := strings.TrimSpace(r.Form.Get("video_mode"))
	if videoMode == "" {
		videoMode = "frames"
	}
	if videoMode != "frames" && videoMode != "references" {
		return promptassistant.VideoContext{}, fmt.Errorf("некорректный режим MiniMax H3")
	}
	longParts := 0
	longTransition := ""
	if r.Form.Get("video_long_enabled") == "true" {
		value, err := strconv.Atoi(r.Form.Get("video_long_parts"))
		if err != nil || value < 2 || value > miniMaxLongMaximumParts || r.Form.Get("generation_workflow") == aiHubWorkflowID {
			return promptassistant.VideoContext{}, fmt.Errorf("выберите от 2 до %d частей MiniMax H3", miniMaxLongMaximumParts)
		}
		longParts, videoMode = value, "references"
		longTransition = r.Form.Get("video_long_transition")
		if longTransition == "" {
			longTransition = "continuous"
		}
		if longTransition != "continuous" && longTransition != "cinematic" {
			return promptassistant.VideoContext{}, fmt.Errorf("выберите плавное продолжение или киномонтаж")
		}
	}
	duration := 5
	if raw := strings.TrimSpace(r.Form.Get("video_duration_seconds")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < miniMaxH3MinimumSeconds || value > 60 {
			return promptassistant.VideoContext{}, fmt.Errorf("длительность MiniMax H3 должна быть от 5 до 60 секунд")
		}
		duration = value
	}
	if imageCount < 0 || imageCount > promptAssistantReferenceLimit(r, mode) {
		return promptassistant.VideoContext{}, fmt.Errorf("некорректное количество фото MiniMax H3")
	}
	if videoMode == "frames" && imageCount > 2 {
		return promptassistant.VideoContext{}, fmt.Errorf("в режиме кадров MiniMax H3 доступно до двух фото")
	}
	audioValue := strings.TrimSpace(r.Form.Get("video_has_audio"))
	if audioValue != "" && audioValue != "true" && audioValue != "false" {
		return promptassistant.VideoContext{}, fmt.Errorf("некорректное значение аудиореференса MiniMax H3")
	}
	audioReference := audioValue == "true"
	if audioReference && videoMode != "references" {
		return promptassistant.VideoContext{}, fmt.Errorf("аудиореференс MiniMax H3 доступен только в режиме референсов")
	}
	videoValue := strings.TrimSpace(r.Form.Get("video_has_video"))
	if videoValue != "" && videoValue != "true" && videoValue != "false" {
		return promptassistant.VideoContext{}, fmt.Errorf("некорректное значение видеореференса MiniMax H3")
	}
	videoReference := videoValue == "true"
	if videoReference && videoMode != "references" {
		return promptassistant.VideoContext{}, fmt.Errorf("видеореференс MiniMax H3 доступен только в режиме референсов")
	}
	if longParts > 0 {
		duration = longParts * miniMaxLongPartSeconds
	}
	return promptassistant.VideoContext{Mode: videoMode, DurationSeconds: duration, LongParts: longParts, LongTransition: longTransition, ImageCount: imageCount, AudioReference: audioReference, VideoReference: videoReference}, nil
}

func promptAssistantReferenceLimit(r *http.Request, mode promptassistant.Mode) int {
	if mode == promptassistant.ModeImageToImage && r.Form.Get("generation_workflow") == "qwen-image21-edit" {
		return 10
	}
	if mode == promptassistant.ModeTextToVideo && r.Form.Get("generation_workflow") == aiHubWorkflowID && r.Form.Get("video_mode") == "references" {
		if r.Form.Get("aihub_mode") == "carousel" {
			return 5
		}
		return 9
	}
	return 4
}

func promptAssistantMediaReservation(r *http.Request, mode promptassistant.Mode) int64 {
	if mode != promptassistant.ModeImageToImage && mode != promptassistant.ModeTextToVideo {
		return promptAssistantMemoryReservation
	}
	for number := 1; number <= promptAssistantReferenceLimit(r, mode); number++ {
		field := "input_image"
		if number > 1 {
			field = fmt.Sprintf("input_image_%d", number)
		}
		if strings.TrimSpace(r.Form.Get(field)) != "" {
			return promptAssistantImageMemoryReservation
		}
	}
	return promptAssistantMemoryReservation
}

func (a *App) promptAssistantImageReferences(ctx context.Context, userID int64, r *http.Request, mode promptassistant.Mode) ([]promptassistant.ImageReference, error) {
	if mode != promptassistant.ModeImageToImage && mode != promptassistant.ModeTextToVideo {
		return nil, nil
	}
	maximum := promptAssistantReferenceLimit(r, mode)
	references := make([]promptassistant.ImageReference, 0, maximum)
	preparedAll := false
	defer func() {
		if !preparedAll {
			releasePromptAssistantImages(references)
		}
	}()
	var totalBytes int
	for number := 1; number <= maximum; number++ {
		role, err := promptAssistantImageRole(r, number)
		if err != nil {
			return nil, err
		}
		field := "input_image"
		if number > 1 {
			field = fmt.Sprintf("input_image_%d", number)
		}
		filename := strings.TrimSpace(r.Form.Get(field))
		if filename == "" {
			continue
		}
		correction, err := promptAssistantImageCorrection(r, number)
		if err != nil {
			return nil, err
		}
		if err := a.validateGenerationImage(filename, userID); err != nil {
			return nil, err
		}
		fetchStarted := time.Now()
		image, mimeType, err := a.fetchGenerationInputImage(ctx, filename)
		a.observeMediaOperation("assistant_reference_fetch", int64(len(image)), fetchStarted, err)
		if err != nil {
			return nil, fmt.Errorf("не удалось прочитать изображение %d для ассистента", number)
		}
		totalBytes += len(image)
		if totalBytes > maxPromptAssistantReferenceBytes {
			return nil, fmt.Errorf("общий размер изображений для ассистента превышает 64 МБ")
		}
		prepareStarted := time.Now()
		prepared, preparedMIME, changed, prepareErr := prepareVisionReference(image, mimeType)
		a.observeMediaOperation("assistant_reference_prepare", int64(len(image)), prepareStarted, prepareErr)
		if prepareErr != nil {
			clear(image)
			logGateway(ctx, slog.LevelWarn, "assistant_reference_prepare_failed", "Assistant image preparation failed",
				"reference_number", number, "error_code", prepareErr.Error())
			return nil, promptAssistantImagePreparationError(number, prepareErr)
		}
		if changed {
			clear(image)
		}
		image = prepared
		mimeType = preparedMIME
		a.mediaOperationRegistry().observe("assistant_reference_payload", int64(len(image)), 0, nil)
		referenceNumber := number
		if mode == promptassistant.ModeTextToVideo {
			// MiniMax numbers only the references that actually reach the node.
			// Keep the assistant's <Picture N> identifiers in that same order.
			referenceNumber = len(references) + 1
		}
		references = append(references, promptassistant.ImageReference{Number: referenceNumber, Role: role, MIMEType: mimeType, Image: image, Correction: correction})
	}
	preparedAll = true
	return references, nil
}

func promptAssistantImagePreparationError(number int, err error) error {
	switch {
	case errors.Is(err, errVisionReferenceSize):
		return fmt.Errorf("изображение %d слишком большое для ассистента: максимум %d Мп и %d пикселей по стороне", number, maxComfyImagePixels/1_000_000, maxComfyImageSide)
	case errors.Is(err, errVisionReferenceFormat), errors.Is(err, errVisionReferenceDecode):
		return fmt.Errorf("не удалось прочитать изображение %d для ассистента: файл повреждён или формат не поддерживается; используйте PNG, JPEG или WebP", number)
	default:
		return fmt.Errorf("не удалось подготовить изображение %d для ассистента", number)
	}
}

func promptAssistantImageRole(r *http.Request, number int) (promptassistant.ImageReferenceRole, error) {
	role := promptassistant.ImageReferenceRole(strings.TrimSpace(r.Form.Get(fmt.Sprintf("image_role_%d", number))))
	if role == "" {
		return promptassistant.ImageReferenceBaseScene, nil
	}
	if !promptassistant.ValidImageReferenceRole(role) {
		return "", fmt.Errorf("некорректная роль для изображения %d", number)
	}
	return role, nil
}

func promptAssistantImageCorrection(r *http.Request, number int) (string, error) {
	value := strings.TrimSpace(r.Form.Get(fmt.Sprintf("image_correction_%d", number)))
	if utf8.RuneCountInString(value) > 500 {
		return "", fmt.Errorf("уточнение для фото %d должно содержать не более 500 символов", number)
	}
	if err := validateGenerationPrompt(value); err != nil {
		return "", err
	}
	return value, nil
}
