package gateway

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/store"
)

type loraRepeatConfirmation struct {
	Purpose      string                               `json:"purpose"`
	UserID       int64                                `json:"user"`
	RequestID    string                               `json:"request"`
	SourceID     string                               `json:"source"`
	SourceSHA256 string                               `json:"source_sha256"`
	Constraint   *domain.LoraTrainingRepeatConstraint `json:"constraint"`
	ExpiresAt    int64                                `json:"expires"`
}

func (a *App) loraRepeatUser(r *http.Request) (*User, error) {
	user := a.currentUser(r)
	if user == nil {
		return nil, store.ErrLoraTrainingForbidden
	}
	fresh, err := a.store.UserByID(r.Context(), user.ID)
	if err != nil {
		return nil, err
	}
	if fresh.Disabled || fresh.AccountExpiresAt.Valid && !fresh.AccountExpiresAt.Time.After(time.Now()) || !taskAccessScope(&fresh).Training {
		return nil, store.ErrLoraTrainingForbidden
	}
	return &fresh, nil
}

func writeLoraRepeatError(w http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "Не удалось подготовить повтор обучения. Повторите запрос."
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status, message = http.StatusNotFound, "Исходное обучение или сохранённая версия набора недоступны."
	case errors.Is(err, store.ErrLoraTrainingForbidden):
		status, message = http.StatusForbidden, "Доступ к обучению закрыт."
	case errors.Is(err, store.ErrLoraTrainingAlreadyActive):
		status, message = http.StatusConflict, "У вас уже есть активное обучение. Дождитесь завершения или отмените его в задачах."
	case errors.Is(err, domain.ErrLoraTrainingRepeat):
		status, message = http.StatusBadRequest, "Не удалось восстановить точные параметры исходного обучения."
	case errors.Is(err, store.ErrLoraTrainingRepeatSource):
		status, message = http.StatusConflict, "Исходный набор или профиль обучения изменился либо недоступен. Подготовьте повтор заново."
	case errors.Is(err, store.ErrLoraTrainingRepeatConflict):
		status, message = http.StatusConflict, "Подтверждение устарело или относится к другому запуску. Подготовьте повтор заново."
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func writeLoraRepeatReceipt(w http.ResponseWriter, receipt domain.LoraTrainingRepeatReceipt, created bool) {
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"job_id": receipt.JobPublicID, "source_id": receipt.SourcePublicID,
		"source_name": receipt.SourceName, "snapshot_id": receipt.DatasetSnapshotID, "deleted": receipt.JobID == nil, "created": created})
}

func (a *App) matchingLoraRepeatProfile(ctx context.Context, source domain.LoraTrainingJob) (loratraining.Profile, error) {
	profiles, _ := a.loraTrainingProfiles(ctx)
	for _, p := range profiles {
		if p.ID == source.ProfileID && p.Family == source.Family && p.BaseModel == source.BaseModel && p.Ready && p.RepeatProtocol == 1 {
			return p, nil
		}
	}
	return loratraining.Profile{}, store.ErrLoraTrainingRepeatSource
}

func (a *App) prepareLoraRepeat(ctx context.Context, userID int64, sourceID string) (domain.LoraTrainingJob, domain.LoraDatasetSnapshot, *domain.LoraTrainingRepeatConstraint, error) {
	var snapshot domain.LoraDatasetSnapshot
	source, err := a.store.LoraTrainingJobByPublicID(ctx, sourceID, userID, false)
	if err != nil {
		return source, snapshot, nil, err
	}
	active, err := a.store.UserHasActiveLoraTrainingJob(ctx, userID)
	if err != nil {
		return source, snapshot, nil, err
	}
	if active {
		return source, snapshot, nil, store.ErrLoraTrainingAlreadyActive
	}
	profile, err := a.matchingLoraRepeatProfile(ctx, source)
	if err != nil {
		return source, snapshot, nil, err
	}
	c, err := domain.NewLoraTrainingRepeatConstraint(source, profile.ProfileSHA256)
	if err != nil {
		return source, snapshot, nil, err
	}
	snapshot, err = a.store.LoraDatasetSnapshot(ctx, userID, source.DatasetSnapshotID)
	if err == nil && snapshot.Hash != source.DatasetSnapshotHash {
		err = store.ErrLoraTrainingRepeatSource
	}
	return source, snapshot, c, err
}

func (a *App) encodeLoraRepeatConfirmation(value loraRepeatConfirmation) (string, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	cipher, err := a.contentCipher.Encrypt(string(plain))
	return base64.RawURLEncoding.EncodeToString(cipher), err
}

func (a *App) decodeLoraRepeatConfirmation(token string) (loraRepeatConfirmation, error) {
	var value loraRepeatConfirmation
	if len(token) > 32768 {
		return value, store.ErrLoraTrainingRepeatConflict
	}
	cipher, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return value, store.ErrLoraTrainingRepeatConflict
	}
	plain, err := a.contentCipher.Decrypt(cipher)
	if err != nil || json.Unmarshal([]byte(plain), &value) != nil || value.Purpose != "lora-repeat-v1" {
		return value, store.ErrLoraTrainingRepeatConflict
	}
	if _, err := domain.LoraTrainingRepeatRequestHash(value.SourceSHA256, value.Constraint); err != nil || value.Constraint.SourceJobID != value.SourceID {
		return value, store.ErrLoraTrainingRepeatConflict
	}
	return value, nil
}

func (a *App) handleLoraTrainingRepeat(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, err := a.loraRepeatUser(r)
	if err != nil {
		writeLoraRepeatError(w, err)
		return
	}
	if r.URL.Path == "/api/lora-training/repeats" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		requestID := r.URL.Query().Get("request")
		if !validGenerationRequestID(requestID) {
			writeLoraRepeatError(w, domain.ErrLoraTrainingRepeat)
			return
		}
		receipt, err := a.store.LoraTrainingRepeatByRequest(r.Context(), user.ID, requestID)
		if err != nil {
			writeLoraRepeatError(w, err)
			return
		}
		writeLoraRepeatReceipt(w, receipt, false)
		return
	}
	quote := r.URL.Path == "/api/lora-training/repeats/quote"
	if !quote && r.URL.Path != "/api/lora-training/repeats/enqueue" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 48<<10)
	if r.ParseForm() != nil {
		writeLoraRepeatError(w, domain.ErrLoraTrainingRepeat)
		return
	}
	if !a.validCSRF(r) {
		writeLoraRepeatError(w, store.ErrLoraTrainingForbidden)
		return
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			writeLoraRepeatError(w, domain.ErrLoraTrainingRepeat)
			return
		}
	}
	sourceID, requestID := r.PostForm.Get("source_id"), r.PostForm.Get("client_request_id")
	if !loraDatasetIDPattern.MatchString(sourceID) || !validGenerationRequestID(requestID) {
		writeLoraRepeatError(w, domain.ErrLoraTrainingRepeat)
		return
	}
	var confirmed loraRepeatConfirmation
	if !quote {
		confirmed, err = a.decodeLoraRepeatConfirmation(r.PostForm.Get("confirmation"))
		if err != nil || confirmed.UserID != user.ID || confirmed.RequestID != requestID || confirmed.SourceID != sourceID {
			writeLoraRepeatError(w, store.ErrLoraTrainingRepeatConflict)
			return
		}
	}
	existing, err := a.store.LoraTrainingRepeatByRequest(r.Context(), user.ID, requestID)
	if err == nil {
		if existing.SourcePublicID != sourceID {
			writeLoraRepeatError(w, store.ErrLoraTrainingRepeatConflict)
			return
		}
		if !quote {
			digest, _ := domain.LoraTrainingRepeatRequestHash(confirmed.SourceSHA256, confirmed.Constraint)
			if existing.RequestSHA256 != digest {
				writeLoraRepeatError(w, store.ErrLoraTrainingRepeatConflict)
				return
			}
		}
		writeLoraRepeatReceipt(w, existing, false)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeLoraRepeatError(w, err)
		return
	}
	if !quote && confirmed.ExpiresAt <= time.Now().Unix() {
		writeLoraRepeatError(w, store.ErrLoraTrainingRepeatConflict)
		return
	}
	source, snapshot, constraint, err := a.prepareLoraRepeat(r.Context(), user.ID, sourceID)
	if err != nil {
		writeLoraRepeatError(w, err)
		return
	}
	sourceHash := domain.LoraTrainingRepeatSourceHash(source)
	if !quote {
		want, _ := domain.LoraTrainingRepeatRequestHash(sourceHash, constraint)
		got, _ := domain.LoraTrainingRepeatRequestHash(confirmed.SourceSHA256, confirmed.Constraint)
		if want != got {
			writeLoraRepeatError(w, store.ErrLoraTrainingRepeatSource)
			return
		}
	}
	publicID := newRequestID()
	workspace := filepath.Join(a.mediaSpoolDir(), "lora-training", publicID)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		writeLoraRepeatError(w, err)
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(workspace)
		}
	}()
	archive := filepath.Join(workspace, "dataset.zip")
	count, err := a.writeLoraDatasetTrainingArchive(r.Context(), snapshot, archive)
	if err != nil || count != constraint.Parameters.SampleCount {
		writeLoraRepeatError(w, store.ErrLoraTrainingRepeatSource)
		return
	}
	info, err := os.Stat(archive)
	if err != nil {
		writeLoraRepeatError(w, err)
		return
	}
	if quote {
		value := loraRepeatConfirmation{Purpose: "lora-repeat-v1", UserID: user.ID, RequestID: requestID, SourceID: sourceID,
			SourceSHA256: sourceHash, Constraint: constraint, ExpiresAt: time.Now().Add(10 * time.Minute).Unix()}
		token, err := a.encodeLoraRepeatConfirmation(value)
		if err != nil {
			writeLoraRepeatError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"source_id": sourceID, "source_name": source.Name, "snapshot": snapshot,
			"parameters": constraint.Parameters, "base_model": source.BaseModel, "profile_id": source.ProfileID,
			"recorded_components": constraint.Tuner != nil, "confirmation": token, "expires_at": value.ExpiresAt, "trigger_word": source.TriggerWord})
		return
	}
	job := domain.LoraTrainingRepeatJob(source, constraint)
	job.PublicID, job.UserID, job.UsernameSnapshot, job.RequestID = publicID, user.ID, user.Username, requestID
	job.DatasetPath, job.DatasetBytes = archive, info.Size()
	receipt, created, err := a.store.CreateLoraTrainingRepeat(r.Context(), domain.CreateLoraTrainingRepeatParams{Job: job, SourceJobID: sourceID, SourceSHA256: sourceHash, Constraint: constraint})
	// A commit error can mean its acknowledgement was lost. Retain the archive
	// whenever the transaction reached commit so recovery cannot find a broken job.
	keep = created
	if err != nil {
		writeLoraRepeatError(w, err)
		return
	}
	if created {
		a.audit(r.Context(), &user.ID, "lora_training_repeated", "lora_training_job", receipt.JobID, a.clientIP(r), r.UserAgent(), map[string]any{"public_id": receipt.JobPublicID, "source_id": sourceID, "snapshot_id": snapshot.ID})
	}
	writeLoraRepeatReceipt(w, receipt, created)
}
