package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type loraVerificationConfirmation struct {
	Purpose        string                  `json:"purpose"`
	UserID         int64                   `json:"user"`
	RequestID      string                  `json:"request"`
	SourceID       string                  `json:"source"`
	Digest         string                  `json:"digest"`
	PreparedDigest string                  `json:"prepared"`
	ProfileSHA256  string                  `json:"profile"`
	Artifact       domain.LoraFileIdentity `json:"artifact"`
	ExpiresAt      int64                   `json:"expires"`
}

func loraVerificationRequest(values url.Values) (domain.LoraVerificationPlan, string, error) {
	var input domain.LoraVerificationPlan
	if !loraDatasetIDPattern.MatchString(values.Get("lora_verification_source")) || !validGenerationRequestID(values.Get("client_request_id")) {
		return input, "", domain.ErrLoraVerification
	}
	for _, entries := range values {
		if len(entries) != 1 {
			return input, "", domain.ErrLoraVerification
		}
	}
	decoder := json.NewDecoder(strings.NewReader(values.Get("lora_verification_plan")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, "", domain.ErrLoraVerification
	}
	plan, err := domain.NormalizeLoraVerificationPlan(input)
	if err != nil {
		return plan, "", err
	}
	canonical := cloneGenerationValues(values)
	for _, name := range []string{"csrf", "lora_verification_confirmation", "client_request_id", "correlation_id"} {
		canonical.Del(name)
	}
	encoded, _ := json.Marshal(plan)
	canonical.Set("lora_verification_plan", string(encoded))
	hash := sha256.Sum256([]byte(canonical.Encode()))
	return plan, hex.EncodeToString(hash[:]), nil
}

func (a *App) loraVerificationUser(ctx context.Context, r *http.Request) (*User, error) {
	user := a.currentUser(r)
	if user == nil {
		return nil, errLoraUseScopeDenied
	}
	fresh, err := a.store.UserByID(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if fresh.Disabled || fresh.AccountExpiresAt.Valid && !fresh.AccountExpiresAt.Time.After(time.Now()) || !taskAccessScope(&fresh).Training || !fresh.CanUseService("quick_generation") {
		return nil, errLoraUseScopeDenied
	}
	return &fresh, nil
}

func writeLoraVerificationError(w http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "Не удалось получить проверку LoRA. Повторите запрос."
	switch {
	case errors.Is(err, store.ErrGenerationInputUnavailable):
		status, message = http.StatusConflict, generationInputUnavailableMessage
	case errors.Is(err, sql.ErrNoRows):
		status, message = http.StatusNotFound, "Проверка или исходная LoRA недоступна."
	case errors.Is(err, errLoraUseScopeDenied), errors.Is(err, store.ErrQuickGenerationForbidden):
		status, message = http.StatusForbidden, "Доступ к проверке LoRA закрыт."
	case errors.Is(err, domain.ErrLoraVerification):
		status, message = http.StatusBadRequest, "Проверьте промпты, seed и веса LoRA: допустимо от 2 до 20 изображений."
	case errors.Is(err, store.ErrGenerationBatchConflict), errors.Is(err, store.ErrLoraVerificationSource):
		status, message = http.StatusConflict, "Параметры или исходная LoRA изменились. Подтвердите проверку заново."
	case errors.Is(err, store.ErrQuickGenerationDailyLimit), errors.Is(err, store.ErrQuickGenerationTotalLimit):
		status, message = quickGenerationLimitError(err)
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func (a *App) prepareLoraVerification(ctx context.Context, user *User, values url.Values, plan domain.LoraVerificationPlan) (domain.LoraTrainingJob, []generationBatchCandidate, string, error) {
	source, err := a.store.LoraTrainingJobByPublicID(ctx, values.Get("lora_verification_source"), user.ID, false)
	if err != nil {
		return source, nil, "", err
	}
	usage, _, err := a.loraUseOptions(ctx, user, source)
	if err != nil {
		return source, nil, "", err
	}
	if usage.State != "ready" {
		return source, nil, "", fmt.Errorf("%w: %s", store.ErrLoraVerificationSource, usage.Message)
	}
	modelAllowed := false
	for _, model := range usage.Models {
		modelAllowed = modelAllowed || model.Available && model.ID == values.Get("model")
	}
	if !modelAllowed || values.Get("template_id") != usage.Preset.TemplateID || values.Get("generation_workflow") != usage.Preset.ID {
		return source, nil, "", store.ErrLoraVerificationSource
	}
	expanded, err := buildLoraVerificationValues(values, plan, usage.Reference)
	if err != nil {
		return source, nil, "", err
	}
	candidates := make([]generationBatchCandidate, 0, len(expanded))
	hash := sha256.New()
	for _, item := range expanded {
		for key := range item.Values {
			if strings.HasPrefix(key, "lora_verification_") {
				item.Values.Del(key)
			}
		}
		input, err := parseGenerationValues(ctx, item.Values)
		if err != nil {
			return source, nil, "", err
		}
		prepared, err := a.prepareGeneration(ctx, user, input, false)
		if err != nil {
			return source, nil, "", err
		}
		if prepared.Input.Seed != item.Member.Seed {
			return source, nil, "", domain.ErrLoraVerification
		}
		if err := json.NewEncoder(hash).Encode(struct {
			Input  generationForm
			Prompt map[string]any
		}{prepared.Input, prepared.Prompt}); err != nil {
			return source, nil, "", err
		}
		cipher, err := a.generationJobPayloadCipher(prepared.Input, item.Values)
		if err != nil {
			return source, nil, "", err
		}
		candidates = append(candidates, generationBatchCandidate{Input: prepared.Input, Values: item.Values, PayloadCipher: cipher, ExperimentValue: item.Member.Weight})
	}
	return source, candidates, hex.EncodeToString(hash.Sum(nil)), nil
}

func (a *App) encodeLoraVerificationConfirmation(value loraVerificationConfirmation) (string, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	cipher, err := a.contentCipher.Encrypt(string(plain))
	return base64.RawURLEncoding.EncodeToString(cipher), err
}

func (a *App) decodeLoraVerificationConfirmation(token string) (loraVerificationConfirmation, error) {
	var value loraVerificationConfirmation
	if len(token) > 4096 {
		return value, store.ErrGenerationBatchConflict
	}
	cipher, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return value, store.ErrGenerationBatchConflict
	}
	plain, err := a.contentCipher.Decrypt(cipher)
	if err != nil || json.Unmarshal([]byte(plain), &value) != nil || value.Purpose != "lora-verification-v1" {
		return loraVerificationConfirmation{}, store.ErrGenerationBatchConflict
	}
	return value, nil
}

func (a *App) handleLoraVerificationWrite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Метод не поддерживается."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRequest)
	if !a.validCSRF(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Проверка безопасности не пройдена."})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeLoraVerificationError(w, domain.ErrLoraVerification)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	user, err := a.loraVerificationUser(ctx, r)
	if err != nil {
		writeLoraVerificationError(w, err)
		return
	}
	plan, digest, err := loraVerificationRequest(r.Form)
	if err != nil {
		writeLoraVerificationError(w, err)
		return
	}
	isQuote := r.URL.Path == "/api/lora-training/verifications/quote"
	var confirmation loraVerificationConfirmation
	if !isQuote {
		confirmation, err = a.decodeLoraVerificationConfirmation(r.Form.Get("lora_verification_confirmation"))
		if err != nil || confirmation.UserID != user.ID || confirmation.RequestID != r.Form.Get("client_request_id") || confirmation.SourceID != r.Form.Get("lora_verification_source") || confirmation.Digest != digest {
			writeLoraVerificationError(w, store.ErrGenerationBatchConflict)
			return
		}
		// A committed request remains recoverable after token expiry or source removal.
		batchID, lookupErr := a.store.LoraVerificationBatchByRequest(ctx, user.ID, confirmation.RequestID)
		if lookupErr == nil {
			v, readErr := a.store.LoraVerificationByBatch(ctx, user.ID, batchID)
			if readErr != nil {
				writeLoraVerificationError(w, readErr)
				return
			}
			if v.RequestSHA256 != digest || v.TrainingPublicID != confirmation.SourceID || v.Artifact != confirmation.Artifact || v.ProfileSHA256 != confirmation.ProfileSHA256 {
				writeLoraVerificationError(w, store.ErrGenerationBatchConflict)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"batch_id": batchID, "created": false})
			return
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			writeLoraVerificationError(w, lookupErr)
			return
		}
		if confirmation.ExpiresAt <= time.Now().Unix() {
			writeLoraVerificationError(w, store.ErrGenerationBatchConflict)
			return
		}
	}
	source, candidates, preparedDigest, err := a.prepareLoraVerification(ctx, user, r.Form, plan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrLoraVerificationSource) {
			writeLoraVerificationError(w, err)
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return
	}
	quota, err := a.generationQuotaView(ctx, user.ID)
	if err != nil {
		writeLoraVerificationError(w, err)
		return
	}
	if quota.Image.DailyLimit > 0 && quota.Image.DailyRemaining < len(candidates) {
		writeLoraVerificationError(w, store.ErrQuickGenerationDailyLimit)
		return
	}
	if quota.Image.TotalLimit > 0 && quota.Image.TotalRemaining < int64(len(candidates)) {
		writeLoraVerificationError(w, store.ErrQuickGenerationTotalLimit)
		return
	}
	if isQuote {
		confirmation = loraVerificationConfirmation{Purpose: "lora-verification-v1", UserID: user.ID, RequestID: r.Form.Get("client_request_id"), SourceID: source.PublicID, Digest: digest, PreparedDigest: preparedDigest, ProfileSHA256: source.Provenance.ProfileSHA256, Artifact: *source.Provenance.Artifact, ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
		token, err := a.encodeLoraVerificationConfirmation(confirmation)
		if err != nil {
			writeLoraVerificationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"confirmation": token, "expires_at": confirmation.ExpiresAt * 1000, "count": len(candidates), "quota": quota, "source_id": source.PublicID, "source_name": source.Name, "model": candidates[0].Input.ModelName, "workflow_adapters": []string{candidates[0].Input.Lora, candidates[0].Input.IdentityLora}})
		return
	}
	if preparedDigest != confirmation.PreparedDigest || source.Provenance.ProfileSHA256 != confirmation.ProfileSHA256 || *source.Provenance.Artifact != confirmation.Artifact {
		writeLoraVerificationError(w, store.ErrGenerationBatchConflict)
		return
	}
	members, _ := plan.Members()
	plain, _ := json.Marshal(plan)
	cipher, err := a.contentCipher.Encrypt(string(plain))
	if err != nil {
		writeLoraVerificationError(w, err)
		return
	}
	first := candidates[0].Input
	params := domain.CreateGenerationBatchParams{PublicID: newRequestID(), UserID: user.ID, UsernameSnapshot: user.Username, RequestID: confirmation.RequestID, TemplateID: first.TemplateID, WorkflowID: first.PresetID, ModelName: first.ModelName, Mode: domain.GenerationBatchLoraVerification, MaxParallel: 1,
		LoraVerification: &domain.CreateLoraVerificationParams{TrainingJobID: source.ID, TrainingPublicID: source.PublicID, Family: source.Family, ProfileSHA256: confirmation.ProfileSHA256, Artifact: confirmation.Artifact, ModelID: first.ModelID, RequestSHA256: digest, PlanCipher: cipher, Members: members}}
	for i, candidate := range candidates {
		params.Jobs = append(params.Jobs, domain.CreateGenerationBatchJobParams{PublicID: newRequestID(), RequestID: generationBatchChildRequestID(params.RequestID, i+1), CorrelationID: correlationID(r), Position: i + 1, ExperimentValue: members[i].Weight,
			Prepared: domain.PreparedGenerationJob{InputNames: generationJobInputNames(candidate.Input), TemplateID: candidate.Input.TemplateID, WorkflowID: candidate.Input.PresetID, ModelName: candidate.Input.ModelName, Seed: candidate.Input.Seed, PayloadCipher: candidate.PayloadCipher, Dependencies: generationJobDependencies(candidate.Input, user), InputCount: generationJobInputCount(candidate.Input)}})
	}
	batch, created, err := a.store.CreateGenerationBatch(ctx, params)
	if err != nil {
		writeLoraVerificationError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"batch_id": batch.PublicID, "created": created})
}

func (a *App) handleLoraVerificationRead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Метод не поддерживается."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	user, err := a.loraVerificationUser(ctx, r)
	if err != nil {
		writeLoraVerificationError(w, err)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeLoraVerificationError(w, domain.ErrLoraVerification)
		return
	}
	for key, entries := range query {
		if len(entries) != 1 || key != "source" && key != "before" && key != "batch" && key != "request" {
			writeLoraVerificationError(w, domain.ErrLoraVerification)
			return
		}
	}
	batchID := query.Get("batch")
	if requestID := query.Get("request"); requestID != "" {
		if len(query) != 1 || !validGenerationRequestID(requestID) {
			writeLoraVerificationError(w, domain.ErrLoraVerification)
			return
		}
		batchID, err = a.store.LoraVerificationBatchByRequest(ctx, user.ID, requestID)
		if err != nil {
			writeLoraVerificationError(w, err)
			return
		}
	}
	if batchID != "" {
		if len(query) != 1 || !loraDatasetIDPattern.MatchString(batchID) {
			writeLoraVerificationError(w, domain.ErrLoraVerification)
			return
		}
		v, err := a.store.LoraVerificationByBatch(ctx, user.ID, batchID)
		if err != nil {
			writeLoraVerificationError(w, err)
			return
		}
		plain, err := a.contentCipher.Decrypt(v.PlanCipher)
		var plan domain.LoraVerificationPlan
		if err != nil || json.Unmarshal([]byte(plain), &plan) != nil {
			writeLoraVerificationError(w, domain.ErrLoraVerification)
			return
		}
		expected, err := plan.Members()
		if err != nil || len(expected) != len(v.Members) {
			writeLoraVerificationError(w, domain.ErrLoraVerification)
			return
		}
		for i, member := range v.Members {
			if member.LoraVerificationMemberSpec != expected[i] {
				writeLoraVerificationError(w, domain.ErrLoraVerification)
				return
			}
		}
		batch, err := a.store.GenerationBatchByPublicID(ctx, user.ID, batchID)
		if err != nil {
			writeLoraVerificationError(w, err)
			return
		}
		jobs, err := a.store.GenerationBatchJobs(ctx, user.ID, batch.ID)
		if err != nil {
			writeLoraVerificationError(w, err)
			return
		}
		views, err := a.generationJobViews(ctx, jobs, user.ID)
		if err != nil {
			writeLoraVerificationError(w, err)
			return
		}
		members := make([]map[string]any, 0, len(v.Members))
		for _, member := range v.Members {
			members = append(members, map[string]any{"position": member.Position, "case_index": member.CaseIndex, "variant_index": member.VariantIndex, "seed": strconv.FormatInt(member.Seed, 10), "weight": member.Weight, "job_id": member.JobPublicID, "state": member.State})
		}
		writeJSON(w, http.StatusOK, map[string]any{"batch": makeGenerationBatchView(batch, views), "plan": plan, "members": members, "source_id": v.TrainingPublicID, "source_name": v.TrainingName, "artifact": v.Artifact, "source_available": v.TrainingJobID != nil})
		return
	}
	source := query.Get("source")
	before, err := strconv.ParseInt(query.Get("before"), 10, 64)
	if query.Get("before") == "" {
		before, err = 0, nil
	}
	if err != nil || before < 0 || !loraDatasetIDPattern.MatchString(source) || len(query) > 2 {
		writeLoraVerificationError(w, domain.ErrLoraVerification)
		return
	}
	ids, err := a.store.ListLoraVerificationBatches(ctx, user.ID, source, before, 11)
	if err != nil {
		writeLoraVerificationError(w, err)
		return
	}
	more := len(ids) > 10
	if more {
		ids = ids[:10]
	}
	items := make([]map[string]any, 0, len(ids))
	var cursor int64
	for _, id := range ids {
		batch, err := a.store.GenerationBatchByPublicID(ctx, user.ID, id)
		if err != nil {
			writeLoraVerificationError(w, err)
			return
		}
		items = append(items, map[string]any{"batch_id": id, "created_at": batch.CreatedAt, "state": generationBatchState(batch), "total_count": batch.TotalCount, "completed_count": batch.CompletedCount, "model": batch.ModelName})
		cursor = batch.ID
	}
	if !more {
		cursor = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": strconv.FormatInt(cursor, 10), "source_id": source})
}
