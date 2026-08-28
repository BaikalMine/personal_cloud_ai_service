package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/promptassistant"
)

const maxPromptAssistantInput = 4000

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
	mode := promptassistant.Mode(strings.TrimSpace(r.Form.Get("template_id")))
	if mode != promptassistant.ModeTextToImage && mode != promptassistant.ModeImageToImage && mode != promptassistant.ModeTextToVideo {
		writeGenerationError(w, http.StatusBadRequest, "неизвестный режим генерации")
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
	references, err := promptAssistantImageReferences(r, mode)
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
	if a.promptAssistant == nil || !a.promptAssistant.Configured() {
		writeGenerationError(w, http.StatusServiceUnavailable, "локальный промт-ассистент не настроен")
		return
	}
	user := a.currentUser(r)
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
	ctx, cancel := context.WithTimeout(r.Context(), 95*time.Second)
	defer cancel()
	result, err := a.promptAssistant.Enhance(ctx, mode, profile, prompt, references, think)
	if err != nil {
		log.Printf("prompt assistant: %v", err)
		writeGenerationError(w, http.StatusBadGateway, "не удалось получить вариант от локальной модели")
		return
	}
	a.recordPromptAssistantEvent(ctx, user.ID, mode, profile, prompt, result, references, think)
	response := map[string]any{"prompt": result, "model": a.cfg.PromptAssistantModel}
	if miningWarning != "" {
		response["mining_warning"] = miningWarning
	}
	if miningLease != nil && miningLease.ResumeMining {
		response["mining_paused"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

func promptAssistantImageReferences(r *http.Request, mode promptassistant.Mode) ([]promptassistant.ImageReference, error) {
	if mode != promptassistant.ModeImageToImage {
		return nil, nil
	}
	references := make([]promptassistant.ImageReference, 0, 4)
	for number := 1; number <= 4; number++ {
		role := promptassistant.ImageReferenceRole(strings.TrimSpace(r.Form.Get(fmt.Sprintf("image_role_%d", number))))
		if role == "" {
			continue
		}
		if !promptassistant.ValidImageReferenceRole(role) {
			return nil, fmt.Errorf("некорректная роль для изображения %d", number)
		}
		references = append(references, promptassistant.ImageReference{Number: number, Role: role})
	}
	return references, nil
}

func (a *App) recordPromptAssistantEvent(ctx context.Context, userID int64, mode promptassistant.Mode, profile promptassistant.Profile, prompt, result string, references []promptassistant.ImageReference, think bool) {
	if a.store == nil || a.contentCipher == nil {
		return
	}
	metadata, err := json.Marshal(map[string]any{
		"workflow": string(mode), "template": string(profile), "think": think, "session": "single_request", "keep_alive": "0",
		"purpose": "quick_generation_prompt_assistant", "image_references": references,
	})
	if err != nil {
		return
	}
	promptCipher, err := a.contentCipher.Encrypt(prompt)
	if err != nil {
		log.Printf("prompt assistant prompt encryption: %v", err)
		return
	}
	responseCipher, err := a.contentCipher.Encrypt(result)
	if err != nil {
		log.Printf("prompt assistant response encryption: %v", err)
		return
	}
	metadataCipher, err := a.contentCipher.Encrypt(string(metadata))
	if err != nil {
		log.Printf("prompt assistant metadata encryption: %v", err)
		return
	}
	if _, err := a.store.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: userID, Service: "ollama", Kind: "prompt_assistant", ExternalID: newRequestID(),
		Model: a.cfg.PromptAssistantModel, PromptCipher: promptCipher, ResponseCipher: responseCipher, MetadataCipher: metadataCipher,
	}); err != nil {
		log.Printf("store prompt assistant event: %v", err)
	}
}
