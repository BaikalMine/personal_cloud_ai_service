package gateway

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/promptassistant"
)

const (
	maxPromptAssistantInput          = 4000
	maxPromptAssistantReferenceBytes = 64 << 20
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
	references, err := a.promptAssistantImageReferences(r.Context(), user.ID, r, mode)
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	thinkValue := r.Form.Get("assistant_think")
	if thinkValue != "" && thinkValue != "true" && thinkValue != "false" {
		writeGenerationError(w, http.StatusBadRequest, "некорректное значение режима рассуждений")
		return
	}
	think := thinkValue == "true"
	miningLease, miningWarning, err := a.pauseMiningForQuickGeneration(r.Context(), user)
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
	if profile == promptassistant.ProfileMiniMaxH3 {
		video, contextErr := promptAssistantVideoContext(r, mode)
		if contextErr != nil {
			writeGenerationError(w, http.StatusBadRequest, contextErr.Error())
			return
		}
		result, err = a.promptAssistant.EnhanceVideo(ctx, mode, profile, prompt, references, video, think)
	} else {
		result, err = a.promptAssistant.Enhance(ctx, mode, profile, prompt, references, think)
	}
	if err != nil {
		log.Printf("prompt assistant: %v", err)
		writeGenerationError(w, http.StatusBadGateway, "не удалось получить вариант от локальной модели")
		return
	}
	if err := validateGenerationPrompt(result); err != nil {
		a.audit(r.Context(), &user.ID, "generation_safety_blocked", "prompt_assistant", nil, a.clientIP(r), r.UserAgent(), map[string]any{"reason": "minor_sexual_content", "source": "assistant_response"})
		writeGenerationError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	response := map[string]any{"prompt": result, "model": a.cfg.PromptAssistantModel}
	if miningWarning != "" {
		response["mining_warning"] = miningWarning
	}
	if miningLease != nil && miningLease.ResumeMining {
		response["mining_paused"] = true
	}
	writeJSON(w, http.StatusOK, response)
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

func promptAssistantVideoContext(r *http.Request, mode promptassistant.Mode) (promptassistant.VideoContext, error) {
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
	imageCount := 0
	if raw := strings.TrimSpace(r.Form.Get("video_image_count")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 4 {
			return promptassistant.VideoContext{}, fmt.Errorf("некорректное количество фото MiniMax H3")
		}
		imageCount = value
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
	if videoMode == "references" && imageCount == 0 && !audioReference && !videoReference {
		return promptassistant.VideoContext{}, fmt.Errorf("для режима референсов добавьте фото, видео или аудио")
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
		image, mimeType, err := a.fetchGenerationInputImage(ctx, filename)
		if err != nil {
			return nil, fmt.Errorf("не удалось прочитать изображение %d для ассистента", number)
		}
		totalBytes += len(image)
		if totalBytes > maxPromptAssistantReferenceBytes {
			return nil, fmt.Errorf("общий размер изображений для ассистента превышает 64 МБ")
		}
		references = append(references, promptassistant.ImageReference{Number: number, Role: role, MIMEType: mimeType, Image: image})
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
