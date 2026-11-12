package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/inference"
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/store"
)

type promptAssistantJobSnapshot struct {
	ModelID          string                              `json:"model_id,omitempty"`
	Request          promptassistant.PreparedEnhancement `json:"request"`
	Original         string                              `json:"original"`
	OriginalNegative string                              `json:"original_negative,omitempty"`
	OriginalAudio    string                              `json:"original_audio,omitempty"`
	CFG              float64                             `json:"cfg,omitempty"`
	Profile          promptassistant.Profile             `json:"profile"`
	Think            bool                                `json:"think"`
	Context          string                              `json:"context"`
}

type promptAssistantJobView struct {
	ID               string                       `json:"id"`
	State            string                       `json:"state"`
	Status           string                       `json:"status"`
	Context          string                       `json:"context"`
	Original         string                       `json:"original"`
	OriginalNegative string                       `json:"original_negative,omitempty"`
	OriginalAudio    string                       `json:"original_audio,omitempty"`
	ExpiresAt        time.Time                    `json:"expires_at"`
	CreatedAt        time.Time                    `json:"created_at"`
	Progress         *inference.AssistantProgress `json:"progress,omitempty"`
	Result           json.RawMessage              `json:"result,omitempty"`
}

type promptAssistantJobRuntime struct {
	MiningWarning string `json:"mining_warning,omitempty"`
	MiningPaused  bool   `json:"mining_paused,omitempty"`
}

func promptAssistantRequestAAD(id string) []byte {
	return []byte("prompt-assistant-job/request/v1/" + id)
}

func (a *App) enqueuePromptAssistantHTTP(w http.ResponseWriter, r *http.Request, user User, assistant *promptassistant.Client, modelID string, mode promptassistant.Mode, profile promptassistant.Profile, prompt string, refs []promptassistant.ImageReference, video promptassistant.VideoContext, edit promptassistant.EditContext, think bool) {
	id, panelContext := strings.TrimSpace(r.Form.Get("request_id")), r.Form.Get("context")
	if !validCorrelationID(id) || len(id) < 16 || len(id) > 64 || len(panelContext) > 16<<10 {
		writeGenerationError(w, http.StatusBadRequest, "некорректный идентификатор запроса")
		return
	}
	prepared, err := assistant.PrepareEnhancementWithEditContext(mode, profile, prompt, refs, video, edit, think)
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer clear(prepared.Body)
	job, err := a.makePromptAssistantJob(id, user.ID, promptAssistantJobSnapshot{ModelID: modelID, Request: prepared, Original: prompt, OriginalNegative: edit.NegativePrompt, OriginalAudio: video.AudioPrompt, CFG: edit.CFG, Profile: profile, Think: think, Context: panelContext})
	if err == nil {
		job, err = a.store.EnqueuePromptAssistantJob(r.Context(), job)
	}
	if err != nil {
		writePromptAssistantJobError(w, err)
		return
	}
	a.writePromptAssistantJob(r.Context(), w, job, http.StatusAccepted)
}

func (a *App) makePromptAssistantJob(id string, userID int64, snapshot promptAssistantJobSnapshot) (domain.PromptAssistantJob, error) {
	job := domain.PromptAssistantJob{ID: id, UserID: userID, Mode: string(snapshot.Request.Mode), ModelID: snapshot.ModelID}
	metadata, err := json.Marshal(snapshot)
	if err != nil {
		return job, err
	}
	defer clear(metadata)
	requestHash := sha256.New()
	requestHash.Write(snapshot.Request.Body)
	requestHash.Write(metadata)
	bodyHash := sha256.Sum256(snapshot.Request.Body)
	job.RequestHash, job.BodyHash = hex.EncodeToString(requestHash.Sum(nil)), hex.EncodeToString(bodyHash[:])
	job.RequestCipher, err = a.contentCipher.EncryptBytesWithAAD(snapshot.Request.Body, promptAssistantRequestAAD(id))
	if err != nil {
		return job, err
	}
	job.MetadataCipher, err = a.contentCipher.EncryptBytes(metadata)
	return job, err
}

func (a *App) promptAssistantSnapshot(job domain.PromptAssistantJob) (promptAssistantJobSnapshot, error) {
	var snapshot promptAssistantJobSnapshot
	plain, err := a.contentCipher.DecryptBytes(job.MetadataCipher)
	if err != nil {
		return snapshot, err
	}
	defer clear(plain)
	err = json.Unmarshal(plain, &snapshot)
	if err == nil && snapshot.ModelID != job.ModelID {
		err = errors.New("assistant model choice does not match its saved request")
	}
	return snapshot, err
}

func (a *App) writePromptAssistantJob(ctx context.Context, w http.ResponseWriter, job domain.PromptAssistantJob, status int) {
	if job.State == "cancelled" && len(job.MetadataCipher) == 0 {
		writeJSON(w, status, promptAssistantJobView{ID: job.ID, State: job.State, Status: job.Status, ExpiresAt: job.ExpiresAt})
		return
	}
	snapshot, err := a.promptAssistantSnapshot(job)
	if err != nil {
		writePromptAssistantJobError(w, err)
		return
	}
	view := promptAssistantJobView{ID: job.ID, State: job.State, Status: job.Status, Context: snapshot.Context, Original: snapshot.Original, OriginalNegative: snapshot.OriginalNegative, ExpiresAt: job.ExpiresAt, CreatedAt: job.CreatedAt}
	view.OriginalAudio = snapshot.OriginalAudio
	if job.InferenceWorkID != "" && (job.State == "queued" || job.State == "running" || job.State == "completed") {
		// Live presentation is optional; a missing snapshot must not hide a saved result.
		view.Progress, _ = inference.NewClient(a.store, a.contentCipher).Progress(ctx, job.InferenceWorkID)
	}
	if job.State == "completed" && len(job.ResultCipher) > 0 {
		view.Result, err = a.contentCipher.DecryptBytes(job.ResultCipher)
		if err != nil {
			writePromptAssistantJobError(w, err)
			return
		}
		defer clear(view.Result)
	}
	writeJSON(w, status, view)
}

func writePromptAssistantJobError(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "не удалось прочитать или сохранить задание ассистента"
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status, message = http.StatusNotFound, "задание не найдено или срок хранения истёк"
	case errors.Is(err, store.ErrPromptAssistantConflict):
		status, message = http.StatusConflict, "этот идентификатор уже использован для другого запроса"
	case errors.Is(err, store.ErrPromptAssistantQuota):
		status, message = http.StatusTooManyRequests, "очередь ассистента заполнена; дождитесь завершения предыдущих заданий"
	case errors.Is(err, store.ErrGPUWorkInput):
		status, message = http.StatusBadRequest, "некорректный запрос ассистента"
	}
	writeGenerationError(w, status, message)
}

func (a *App) handlePromptAssistantJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.handlePromptAssistant(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		writeGenerationError(w, 405, "метод не поддерживается")
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeGenerationError(w, 401, "требуется вход")
		return
	}
	job, err := a.store.PromptAssistantJob(r.Context(), user.ID, r.URL.Query().Get("id"))
	if err != nil {
		writePromptAssistantJobError(w, err)
		return
	}
	if job.Mode != "" && !user.CanUseQuickGenerationType(job.Mode) {
		writeGenerationError(w, 403, "этот тип генерации недоступен")
		return
	}
	a.writePromptAssistantJob(r.Context(), w, job, 200)
}

func (a *App) handleCancelPromptAssistantJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeGenerationError(w, 405, "метод не поддерживается")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if !a.validCSRF(r) {
		writeGenerationError(w, 403, "проверка безопасности не пройдена")
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeGenerationError(w, 401, "требуется вход")
		return
	}
	id := r.Form.Get("id")
	if !validCorrelationID(id) || len(id) < 16 || len(id) > 64 {
		writePromptAssistantJobError(w, store.ErrGPUWorkInput)
		return
	}
	job, err := a.store.CancelPromptAssistantSubmission(r.Context(), user.ID, id)
	if err != nil {
		writePromptAssistantJobError(w, err)
		return
	}
	if len(job.MetadataCipher) == 0 {
		writeJSON(w, http.StatusAccepted, promptAssistantJobView{ID: job.ID, State: job.State, Status: job.Status, ExpiresAt: job.ExpiresAt})
		return
	}
	a.writePromptAssistantJob(r.Context(), w, job, 202)
}

func (a *App) promptAssistantCompletionRecords(job domain.PromptAssistantJob, snapshot promptAssistantJobSnapshot, result promptassistant.Result, state, code string, latency time.Duration) (domain.ContentEventRecord, domain.PromptAssistantRunRecord, error) {
	event := domain.ContentEventRecord{UserID: job.UserID, CorrelationID: job.ID, Service: "ollama", Kind: "prompt_assistant", ExternalID: job.ID, Model: result.Model, GenerationState: promptAssistantContentState(state), ExpiresAt: time.Now().Add(a.retentionPolicy().AIContent)}
	metadata, err := json.Marshal(promptAssistantAuditMetadata{PromptAssistant: promptAssistantAudit{Requested: true, Template: snapshot.Profile, Think: snapshot.Think, OriginalPrompt: snapshot.Original, OriginalNegative: snapshot.OriginalNegative, Suggestion: result.Prompt, NegativeSuggestion: result.NegativePrompt, AudioSuggestion: result.AudioPrompt, Mode: snapshot.Request.Mode, LongParts: snapshot.Request.LongParts, ReferenceCount: len(result.References), References: result.References, Usage: result.Usage, Policy: result.Policy, LatencyMS: latency.Milliseconds(), Status: state, ErrorCode: code}})
	if err != nil {
		return event, domain.PromptAssistantRunRecord{}, err
	}
	event.PromptCipher, err = a.contentCipher.Encrypt(snapshot.Original)
	if err != nil {
		return event, domain.PromptAssistantRunRecord{}, err
	}
	event.ResponseCipher, err = a.contentCipher.Encrypt(result.Prompt)
	if err != nil {
		return event, domain.PromptAssistantRunRecord{}, err
	}
	event.MetadataCipher, err = a.contentCipher.Encrypt(string(metadata))
	run := domain.PromptAssistantRunRecord{UserID: job.UserID, CorrelationID: job.ID, Mode: job.Mode, Profile: string(snapshot.Profile), Model: result.Model, Status: state, LatencyMS: latency.Milliseconds(), PromptTokens: result.Usage.PromptTokens, CompletionTokens: result.Usage.CompletionTokens, TotalDurationMS: result.Usage.TotalDurationMS, LoadDurationMS: result.Usage.LoadDurationMS, EvalDurationMS: result.Usage.CompletionTimeMS, NumPredict: result.Policy.NumPredict, TimeoutMS: result.Policy.TimeoutMS, KeepAlive: result.Policy.KeepAlive, ReferenceCount: len(result.References), ErrorCode: code}
	return event, run, err
}

func (a *App) completePromptAssistantJob(ctx context.Context, job domain.PromptAssistantJob, snapshot promptAssistantJobSnapshot, result promptassistant.Result, message string, latency time.Duration, runErr error) error {
	state, auditState, code := "completed", "completed", ""
	var payload []byte
	if runErr == nil {
		if snapshot.Profile == promptassistant.ProfileQwen21Edit {
			result.Prompt = normalizeQwen21ReferenceMentions(result.Prompt, len(result.References))
		}
		if snapshot.CFG > 0 && snapshot.CFG <= 1 {
			result.NegativePrompt = ""
		}
		if runErr = validateGenerationPrompt(result.Prompt + "\n" + result.AudioPrompt + "\n" + result.NegativePrompt); runErr != nil {
			auditState, code = "blocked", "unsafe_model_output"
			result.Prompt = ""
			result.AudioPrompt = ""
			message = "Вариант не прошёл проверку. Измените запрос."
		}
	}
	if runErr != nil {
		state = "failed"
		if auditState != "blocked" {
			auditState, code = "error", promptAssistantFailureCode(runErr)
		}
	} else {
		var runtime promptAssistantJobRuntime
		plain, err := a.contentCipher.DecryptBytes(job.RuntimeCipher)
		if err != nil {
			return err
		}
		defer clear(plain)
		if err = json.Unmarshal(plain, &runtime); err != nil {
			return err
		}
		response := map[string]any{"prompt": result.Prompt, "negative_prompt": result.NegativePrompt, "audio_prompt": result.AudioPrompt, "references": result.References, "model": result.Model, "correlation_id": job.ID, "usage": result.Usage, "policy": result.Policy}
		if runtime.MiningWarning != "" {
			response["mining_warning"] = runtime.MiningWarning
		}
		if runtime.MiningPaused {
			response["mining_paused"] = true
		}
		payload, err = json.Marshal(response)
		if err != nil {
			return err
		}
		defer clear(payload)
		payload, err = a.contentCipher.EncryptBytes(payload)
		if err != nil {
			return err
		}
	}
	event, run, err := a.promptAssistantCompletionRecords(job, snapshot, result, auditState, code, latency)
	if err != nil {
		return err
	}
	changed, err := a.store.FinishPromptAssistantJob(ctx, job, state, message, payload, event, run)
	if err == nil && changed {
		if job.InferenceWorkID == "" {
			a.releaseUnsentPromptAssistant(ctx, job)
		}
		operation := "enhance_image"
		if promptassistant.IsMiniMaxH3Profile(snapshot.Profile) {
			operation = "enhance_video"
		}
		a.observeServiceCall(ctx, dependencyOllama, operation, time.Now().Add(-latency), runErr, false, code, "")
	}
	return err
}
