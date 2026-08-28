package gateway

import (
	"context"
	"encoding/json"
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
	result, err := a.promptAssistant.Enhance(ctx, mode, profile, prompt, think)
	if err != nil {
		log.Printf("prompt assistant: %v", err)
		writeGenerationError(w, http.StatusBadGateway, "не удалось получить вариант от локальной модели")
		return
	}
	a.recordPromptAssistantEvent(ctx, user.ID, mode, profile, prompt, result, think)
	response := map[string]any{"prompt": result, "model": a.cfg.PromptAssistantModel}
	if miningWarning != "" {
		response["mining_warning"] = miningWarning
	}
	if miningLease != nil && miningLease.ResumeMining {
		response["mining_paused"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) recordPromptAssistantEvent(ctx context.Context, userID int64, mode promptassistant.Mode, profile promptassistant.Profile, prompt, result string, think bool) {
	if a.store == nil || a.contentCipher == nil {
		return
	}
	metadata, err := json.Marshal(map[string]any{
		"workflow": string(mode), "template": string(profile), "think": think, "session": "single_request", "keep_alive": "0",
		"purpose": "quick_generation_prompt_assistant",
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
