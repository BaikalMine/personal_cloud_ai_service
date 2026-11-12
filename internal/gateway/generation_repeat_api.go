package gateway

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

var errGenerationRepeatAccess = errors.New("generation repeat access denied")

type generationRepeatConfirmation struct {
	Purpose    string                     `json:"purpose"`
	UserID     int64                      `json:"user"`
	RequestID  string                     `json:"request"`
	Constraint generationRepeatConstraint `json:"constraint"`
	ExpiresAt  int64                      `json:"expires"`
}

func (a *App) encodeGenerationRepeatConfirmation(value generationRepeatConfirmation) (string, error) {
	if a.contentCipher == nil {
		return "", errGenerationRepeatWaiting
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	cipher, err := a.contentCipher.Encrypt(string(data))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(cipher), nil
}

func (a *App) decodeGenerationRepeatConfirmation(token string) (generationRepeatConfirmation, error) {
	var value generationRepeatConfirmation
	if len(token) == 0 || len(token) > 4096 || a.contentCipher == nil {
		return value, errGenerationRepeatDrift
	}
	cipher, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return value, errGenerationRepeatDrift
	}
	plain, err := a.contentCipher.Decrypt(cipher)
	if err != nil || json.Unmarshal([]byte(plain), &value) != nil || value.Purpose != "generation-repeat-v1" || value.Constraint.Version != 1 {
		return generationRepeatConfirmation{}, errGenerationRepeatDrift
	}
	return value, nil
}

func (a *App) generationRepeatUser(ctx context.Context, r *http.Request) (*User, error) {
	user := a.currentUser(r)
	if user == nil || a.store == nil {
		return nil, errGenerationRepeatAccess
	}
	fresh, err := a.store.UserByID(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if fresh.Disabled || fresh.AccountExpiresAt.Valid && !fresh.AccountExpiresAt.Time.After(time.Now()) || !fresh.CanUseService("quick_generation") {
		return nil, errGenerationRepeatAccess
	}
	return &fresh, nil
}

func writeGenerationRepeatError(w http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "Проверка повтора временно недоступна. Повторите запрос"
	switch {
	case errors.Is(err, store.ErrGenerationInputUnavailable):
		status, message = http.StatusConflict, generationInputUnavailableMessage
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, store.ErrGenerationRepeatSource):
		status, message = http.StatusNotFound, "Исходное задание недоступно"
	case errors.Is(err, errGenerationRepeatAccess), errors.Is(err, store.ErrQuickGenerationForbidden):
		status, message = http.StatusForbidden, "Доступ к повтору этого задания закрыт"
	case errors.Is(err, errGenerationRepeatDrift), errors.Is(err, store.ErrGenerationRepeatConflict):
		status, message = http.StatusConflict, "Условия повтора изменились. Проверьте их и подтвердите заново"
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func (a *App) resumeGenerationRepeat(ctx context.Context, user *User, receipt domain.GenerationRepeatReceipt) (domain.GenerationJob, error) {
	if receipt.JobID == nil {
		return domain.GenerationJob{}, sql.ErrNoRows
	}
	job, err := a.store.GenerationJobByID(ctx, *receipt.JobID)
	if err != nil {
		return job, err
	}
	if job.UserID == nil || *job.UserID != user.ID || job.PublicID != receipt.JobPublicID || job.RequestID != receipt.RequestID || !user.CanUseQuickGenerationType(job.TemplateID) {
		return job, errGenerationRepeatAccess
	}
	if job.State == domain.GenerationJobWaitingForResources && job.DispatchQueuedAt == nil && job.CancellationRequestedAt == nil && job.ResourcesReleasedAt == nil {
		if _, _, err = a.store.ReserveQuickGenerationForJob(ctx, job.ID, user.ID); err != nil {
			if errors.Is(err, store.ErrQuickGenerationDailyLimit) || errors.Is(err, store.ErrQuickGenerationTotalLimit) || errors.Is(err, store.ErrQuickGenerationForbidden) {
				_, message := quickGenerationLimitError(err)
				return a.failGenerationJob(ctx, job, "generation_repeat_quota_failed", message, err), nil
			}
			return job, err
		}
		job, err = a.store.QueueGenerationJobDispatch(ctx, job.ID)
		if errors.Is(err, store.ErrGenerationJobStateConflict) {
			return a.store.GenerationJobByID(ctx, *receipt.JobID)
		}
	}
	return job, err
}

func (a *App) writeGenerationRepeatReceipt(w http.ResponseWriter, ctx context.Context, user *User, receipt domain.GenerationRepeatReceipt) {
	if receipt.Cancelled {
		writeJSON(w, http.StatusOK, map[string]any{"accepted": false, "cancelled": true, "request_id": receipt.RequestID, "source_id": receipt.SourcePublicID})
		return
	}
	if receipt.JobID == nil {
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "removed": true, "request_id": receipt.RequestID, "source_id": receipt.SourcePublicID, "job_id": receipt.JobPublicID, "message": "Повтор уже был создан; его запись удалена. Повторной отправки не будет"})
		return
	}
	job, err := a.resumeGenerationRepeat(ctx, user, receipt)
	if err != nil {
		writeGenerationRepeatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "request_id": receipt.RequestID, "source_id": receipt.SourcePublicID, "job_id": job.PublicID, "job": a.generationJobView(job)})
}

func (a *App) handleGenerationRepeatStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", 405)
		return
	}
	query := r.URL.Query()
	requestID := query.Get("request_id")
	if len(query) != 1 || len(query["request_id"]) != 1 || !validGenerationRequestID(requestID) {
		writeJSON(w, 400, map[string]string{"error": "Некорректный идентификатор повтора"})
		return
	}
	user, err := a.generationRepeatUser(r.Context(), r)
	if err != nil {
		writeGenerationRepeatError(w, err)
		return
	}
	receipt, err := a.store.GenerationRepeatByRequest(r.Context(), user.ID, requestID)
	if err != nil {
		writeGenerationRepeatError(w, err)
		return
	}
	a.writeGenerationRepeatReceipt(w, r.Context(), user, receipt)
}

func (a *App) handleGenerationRepeat(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if r.URL.RawQuery != "" || !a.validCSRF(r) {
		writeJSON(w, 403, map[string]string{"error": "Проверка безопасности не пройдена"})
		return
	}
	submit := r.URL.Path == "/generate/jobs/repeat/submit"
	for key, values := range r.PostForm {
		if len(values) != 1 || key != "csrf" && key != "source_job_id" && key != "client_request_id" && !(submit && key == "confirmation") {
			writeJSON(w, 400, map[string]string{"error": "Некорректные параметры повтора"})
			return
		}
	}
	sourceID, requestID := r.PostForm.Get("source_job_id"), r.PostForm.Get("client_request_id")
	if !validGenerationRequestID(sourceID) || !validGenerationRequestID(requestID) || sourceID == requestID {
		writeJSON(w, 400, map[string]string{"error": "Некорректный источник повтора"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	user, err := a.generationRepeatUser(ctx, r)
	if err != nil {
		writeGenerationRepeatError(w, err)
		return
	}
	var confirmed generationRepeatConfirmation
	if r.URL.Path == "/generate/jobs/repeat/discard" {
		receipt, err := a.store.DiscardGenerationRepeatRequest(ctx, user.ID, requestID, sourceID)
		if err != nil {
			writeGenerationRepeatError(w, err)
			return
		}
		a.writeGenerationRepeatReceipt(w, ctx, user, receipt)
		return
	}
	if submit {
		confirmed, err = a.decodeGenerationRepeatConfirmation(r.PostForm.Get("confirmation"))
		if err != nil || confirmed.UserID != user.ID || confirmed.RequestID != requestID || confirmed.Constraint.SourceID != sourceID {
			writeGenerationRepeatError(w, errGenerationRepeatDrift)
			return
		}
	}
	receipt, err := a.store.GenerationRepeatByRequest(ctx, user.ID, requestID)
	if err == nil {
		if receipt.SourcePublicID != sourceID || submit && !receipt.Cancelled && receipt.ConfirmationSHA256 != confirmed.Constraint.digest() {
			writeGenerationRepeatError(w, errGenerationRepeatDrift)
			return
		}
		a.writeGenerationRepeatReceipt(w, ctx, user, receipt)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeGenerationRepeatError(w, err)
		return
	}
	if submit && confirmed.ExpiresAt <= time.Now().Unix() {
		writeGenerationRepeatError(w, errGenerationRepeatDrift)
		return
	}
	prepared, err := a.prepareGenerationRepeat(ctx, user, sourceID)
	if err != nil {
		writeGenerationRepeatError(w, err)
		return
	}
	if !submit {
		if prepared.View.State != "pending" && prepared.View.State != "blocked" {
			confirmed = generationRepeatConfirmation{Purpose: "generation-repeat-v1", UserID: user.ID, RequestID: requestID, Constraint: prepared.Constraint, ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
			prepared.View.Confirmation, err = a.encodeGenerationRepeatConfirmation(confirmed)
			if err != nil {
				writeGenerationRepeatError(w, err)
				return
			}
			prepared.View.ExpiresAt = confirmed.ExpiresAt * 1000
		}
		views, viewErr := a.generationJobViews(ctx, []domain.GenerationJob{prepared.Source}, user.ID)
		if viewErr == nil && len(views) == 1 {
			prepared.View.Source = &views[0]
		}
		writeJSON(w, http.StatusOK, prepared.View)
		return
	}
	if prepared.View.State == "pending" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Проверка ещё не завершена. Дождитесь результата и подтвердите повтор заново"})
		return
	}
	if prepared.View.State == "blocked" || prepared.Constraint.digest() != confirmed.Constraint.digest() {
		writeGenerationRepeatError(w, errGenerationRepeatDrift)
		return
	}
	values := cloneStringMap(prepared.Payload.Values)
	for key := range values {
		if strings.HasPrefix(key, "batch_") || strings.HasPrefix(key, "assistant_") || key == "client_request_id" || key == "csrf" {
			delete(values, key)
		}
	}
	values["seed"] = prepared.View.Seed
	input := repeatExecutionInput(prepared.Prepared.Input)
	payload := generationSavedPayload{Version: 1, Values: values, ExecutionInput: &input, Repeat: &prepared.Constraint}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > maxGenerationRequest*2 {
		writeGenerationRepeatError(w, errGenerationRepeatDrift)
		return
	}
	cipher, err := a.contentCipher.Encrypt(string(encoded))
	if err != nil {
		writeGenerationRepeatError(w, err)
		return
	}
	receipt, _, err = a.store.CreateGenerationRepeat(ctx, domain.CreateGenerationRepeatParams{
		Job:            domain.CreateGenerationJobParams{PublicID: newRequestID(), UserID: user.ID, UsernameSnapshot: user.Username, RequestID: requestID},
		Prepared:       domain.PreparedGenerationJob{InputNames: generationJobInputNames(input), TemplateID: input.TemplateID, WorkflowID: input.PresetID, ModelName: input.ModelName, Seed: input.Seed, PayloadCipher: cipher, Dependencies: generationJobDependencies(input, user), InputCount: generationJobInputCount(input)},
		SourcePublicID: sourceID, SourcePayloadCipher: prepared.Source.PayloadCipher, SourceExecutionSHA256: prepared.Constraint.SourceExecutionSHA256, ConfirmationSHA256: prepared.Constraint.digest(),
	})
	if err != nil {
		writeGenerationRepeatError(w, err)
		return
	}
	a.writeGenerationRepeatReceipt(w, ctx, user, receipt)
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
