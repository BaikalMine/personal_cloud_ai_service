package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/promptassistant"
)

const (
	maxPromptAssistantInput          = 4000
	maxPromptAssistantReferenceBytes = 64 << 20
	promptAssistantMemoryReservation = 192 << 20
)

func (a *App) handlePromptAssistant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeGenerationError(w, http.StatusMethodNotAllowed, "метод не поддерживается")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRequest)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	prompt := strings.TrimSpace(r.Form.Get("prompt"))
	if prompt == "" || len(prompt) > maxPromptAssistantInput {
		writeGenerationError(w, http.StatusBadRequest, "введите промт длиной до 4000 символов")
		return
	}
	user := a.currentUser(r)
	if err := validateGenerationPrompt(prompt); err != nil {
		a.audit(r.Context(), &user.ID, "generation_safety_blocked", "prompt_assistant", nil, a.clientIP(r), r.UserAgent(), map[string]any{"reason": "minor_sexual_content"})
		writeGenerationError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	mode := promptassistant.Mode(strings.TrimSpace(r.Form.Get("template_id")))
	if mode != promptassistant.ModeTextToImage && mode != promptassistant.ModeImageToImage && mode != promptassistant.ModeTextToVideo {
		writeGenerationError(w, http.StatusBadRequest, "неизвестный режим генерации")
		return
	}
	if user == nil || !user.CanUseQuickGenerationType(promptAssistantTemplateID(mode)) {
		writeGenerationError(w, http.StatusForbidden, "этот тип быстрой генерации недоступен")
		return
	}
	profile := promptassistant.Profile(strings.TrimSpace(r.Form.Get("assistant_template")))
	if profile == "" {
		profile = promptassistant.ProfileWorkflowDefault
	}
	if !promptassistant.ValidProfile(mode, profile) {
		writeGenerationError(w, http.StatusBadRequest, "неизвестный шаблон промт-ассистента")
		return
	}
	if a.promptAssistant == nil || !a.promptAssistant.Configured() {
		writeGenerationError(w, http.StatusServiceUnavailable, "локальный промт-ассистент не настроен")
		return
	}
	releaseAssistant, acquired := acquireBoundedSlot(r.Context(), a.promptAssistantSlots, time.Second)
	if !acquired {
		writeGenerationError(w, http.StatusTooManyRequests, "промт-ассистент уже обрабатывает другой запрос")
		return
	}
	defer releaseAssistant()
	releaseMedia, acquired := a.mediaByteLimiter().tryAcquire(promptAssistantMemoryReservation)
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
	thinkValue := r.Form.Get("assistant_think")
	if thinkValue != "" && thinkValue != "true" && thinkValue != "false" {
		writeGenerationError(w, http.StatusBadRequest, "некорректное значение режима рассуждений")
		return
	}
	think := thinkValue == "true"
	miningLease, miningWarning, err := a.pauseMiningForQuickGeneration(r.Context(), user, 0)
	if err != nil {
		writeGenerationError(w, http.StatusServiceUnavailable, "не удалось освободить ресурсы для приоритетной работы ассистента: "+err.Error())
		return
	}
	if miningLease != nil {
		defer func() {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer releaseCancel()
			a.releaseMiningPause(releaseCtx, miningLease.ID)
		}()
	}
	assistantTimeout := 95 * time.Second
	if profile == promptassistant.ProfileMiniMaxH3 {
		assistantTimeout = 155 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), assistantTimeout)
	defer cancel()
	var result string
	operation := "enhance_image"
	assistantStarted := time.Now()
	if profile == promptassistant.ProfileMiniMaxH3 {
		operation = "enhance_video"
		video, contextErr := promptAssistantVideoContext(r, mode, len(references))
		if contextErr != nil {
			writeGenerationError(w, http.StatusBadRequest, contextErr.Error())
			return
		}
		result, err = a.promptAssistant.EnhanceVideo(ctx, mode, profile, prompt, references, video, think)
	} else {
		result, err = a.promptAssistant.Enhance(ctx, mode, profile, prompt, references, think)
	}
	a.observeServiceCall(r.Context(), dependencyOllama, operation, assistantStarted, err, false, "assistant_request_failed", "")
	if err != nil {
		logGateway(r.Context(), slog.LevelError, "prompt_assistant_failed", "Prompt assistant request failed",
			"operation", operation,
			"error", err,
		)
		writeGenerationError(w, http.StatusBadGateway, "не удалось получить вариант от локальной модели")
		return
	}
	if err := validateGenerationPrompt(result); err != nil {
		a.audit(r.Context(), &user.ID, "generation_safety_blocked", "prompt_assistant", nil, a.clientIP(r), r.UserAgent(), map[string]any{"reason": "minor_sexual_content", "source": "assistant_response"})
		writeGenerationError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	correlation := correlationID(r)
	a.recordPromptAssistantEvent(r.Context(), user.ID, correlation, mode, profile, prompt, result, think, len(references))
	response := map[string]any{"prompt": result, "model": a.cfg.PromptAssistantModel, "correlation_id": correlation}
	if miningWarning != "" {
		response["mining_warning"] = miningWarning
	}
	if miningLease != nil && miningLease.ResumeMining {
		response["mining_paused"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) recordPromptAssistantEvent(ctx context.Context, userID int64, correlation string, mode promptassistant.Mode, profile promptassistant.Profile, prompt, result string, think bool, referenceCount int) {
	if a.contentCipher == nil || a.store == nil {
		return
	}
	metadata, err := json.Marshal(map[string]any{
		"prompt_assistant": map[string]any{
			"requested": true, "applied": false, "template": profile, "think": think,
			"original_prompt": prompt, "suggestion": result, "mode": mode, "reference_count": referenceCount,
		},
	})
	if err != nil {
		return
	}
	promptCipher, promptErr := a.contentCipher.Encrypt(prompt)
	responseCipher, responseErr := a.contentCipher.Encrypt(result)
	metadataCipher, metadataErr := a.contentCipher.Encrypt(string(metadata))
	if promptErr != nil || responseErr != nil || metadataErr != nil {
		logGateway(ctx, slog.LevelError, "prompt_assistant_audit_encrypt_failed", "Prompt assistant audit encryption failed")
		return
	}
	if _, err := a.store.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: userID, CorrelationID: correlation, Service: "ollama", Kind: "prompt_assistant", ExternalID: newRequestID(),
		Model: a.cfg.PromptAssistantModel, GenerationState: "completed", PromptCipher: promptCipher,
		ResponseCipher: responseCipher, MetadataCipher: metadataCipher, ExpiresAt: time.Now().Add(a.retentionPolicy().AIContent),
	}); err != nil {
		logGateway(ctx, slog.LevelError, "prompt_assistant_audit_store_failed", "Prompt assistant audit storage failed", "error", err)
	}
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
	duration := 5
	if raw := strings.TrimSpace(r.Form.Get("video_duration_seconds")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < miniMaxH3MinimumSeconds || value > 60 {
			return promptassistant.VideoContext{}, fmt.Errorf("длительность MiniMax H3 должна быть от 5 до 60 секунд")
		}
		duration = value
	}
	if imageCount < 0 || imageCount > 4 {
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
	return promptassistant.VideoContext{Mode: videoMode, DurationSeconds: duration, ImageCount: imageCount, AudioReference: audioReference, VideoReference: videoReference}, nil
}

func (a *App) promptAssistantImageReferences(ctx context.Context, userID int64, r *http.Request, mode promptassistant.Mode) ([]promptassistant.ImageReference, error) {
	if mode != promptassistant.ModeImageToImage && mode != promptassistant.ModeTextToVideo {
		return nil, nil
	}
	references := make([]promptassistant.ImageReference, 0, 4)
	var totalBytes int
	for number := 1; number <= 4; number++ {
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
			return nil, fmt.Errorf("не удалось подготовить изображение %d для ассистента", number)
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
		references = append(references, promptassistant.ImageReference{Number: referenceNumber, Role: role, MIMEType: mimeType, Image: image})
	}
	return references, nil
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
