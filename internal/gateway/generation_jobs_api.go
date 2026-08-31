package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const (
	generationJobEventPollInterval      = 750 * time.Millisecond
	generationJobEventHeartbeatInterval = 15 * time.Second
)

type generationJobView struct {
	JobID           string                `json:"job_id"`
	RequestID       string                `json:"request_id"`
	CorrelationID   string                `json:"correlation_id"`
	PromptID        string                `json:"prompt_id,omitempty"`
	State           string                `json:"state"`
	JobState        string                `json:"job_state"`
	Message         string                `json:"message"`
	ErrorCode       string                `json:"error_code,omitempty"`
	TemplateID      string                `json:"template_id"`
	WorkflowID      string                `json:"workflow_id"`
	ModelName       string                `json:"model_name"`
	Seed            int64                 `json:"seed"`
	Attempt         int                   `json:"attempt"`
	InputCount      int                   `json:"input_count"`
	Prompt          string                `json:"prompt,omitempty"`
	Cancellable     bool                  `json:"cancellable"`
	Retryable       bool                  `json:"retryable"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
	FinishedAt      *time.Time            `json:"finished_at,omitempty"`
	ExpiresAt       *time.Time            `json:"expires_at,omitempty"`
	DurationSeconds int64                 `json:"duration_seconds"`
	Media           []generationMediaView `json:"media"`
}

type generationJobTransitionView struct {
	State         string    `json:"state"`
	Message       string    `json:"message"`
	ErrorCode     string    `json:"error_code,omitempty"`
	Attempt       int       `json:"attempt"`
	DurationMS    int64     `json:"duration_ms"`
	CorrelationID string    `json:"correlation_id"`
	CreatedAt     time.Time `json:"created_at"`
}

func (a *App) generationJobView(job domain.GenerationJob) generationJobView {
	// This adapter is kept pure enough for rendering tests; database-backed
	// media is attached by generationJobViews below.
	now := time.Now()
	end := now
	if job.FinishedAt != nil {
		end = *job.FinishedAt
	}
	duration := int64(end.Sub(job.CreatedAt).Seconds())
	if duration < 0 {
		duration = 0
	}
	view := generationJobView{
		JobID: job.PublicID, RequestID: job.RequestID, CorrelationID: job.CorrelationID, PromptID: job.PromptID,
		State: generationJobClientState(job), JobState: string(job.State), Message: job.StatusMessage,
		ErrorCode: job.ErrorCode, TemplateID: job.TemplateID, WorkflowID: job.WorkflowID,
		ModelName: job.ModelName, Seed: job.Seed, Attempt: job.Attempt, InputCount: job.InputCount,
		Cancellable: job.State.Cancellable() && job.CancellationRequestedAt == nil,
		Retryable:   job.State.Terminal() && len(job.PayloadCipher) > 0,
		CreatedAt:   job.CreatedAt, UpdatedAt: job.UpdatedAt, FinishedAt: job.FinishedAt,
		DurationSeconds: duration, Media: []generationMediaView{},
	}
	if job.State.Terminal() {
		boundary := job.CreatedAt
		if job.FinishedAt != nil {
			boundary = *job.FinishedAt
		}
		expires := boundary.Add(a.retentionPolicy().GenerationHistory)
		view.ExpiresAt = &expires
	}
	if len(job.PayloadCipher) > 0 {
		if payload, err := a.decodeGenerationSavedPayload(job.PayloadCipher); err == nil {
			view.Prompt = strings.TrimSpace(payload.Values["positive_prompt"])
		} else {
			view.Retryable = false
		}
	}
	return view
}

func (a *App) generationJobViews(ctx context.Context, jobs []domain.GenerationJob, userID int64) ([]generationJobView, error) {
	views := make([]generationJobView, 0, len(jobs))
	for _, job := range jobs {
		view := a.generationJobView(job)
		if job.PromptID != "" {
			media, err := a.store.ListGenerationVariantMedia(ctx, userID, job.PromptID)
			if err != nil {
				return nil, err
			}
			for _, item := range media {
				view.Media = append(view.Media, generationMediaView{
					ID: item.ID, URL: "/generate/library/" + strconv.FormatInt(item.ID, 10),
					Filename: item.Filename, MediaType: item.MediaType, ExpiresUnix: item.ExpiresAt.UnixMilli(),
					Sensitive: item.Sensitive || item.VisualPending,
				})
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func (a *App) loadGenerationJobViews(r *http.Request) ([]generationJobView, int64, error) {
	user := a.currentUser(r)
	jobs, err := a.store.ListGenerationJobs(r.Context(), user.ID, 60, time.Now().Add(-a.retentionPolicy().GenerationHistory))
	if err != nil {
		return nil, 0, err
	}
	views, err := a.generationJobViews(r.Context(), jobs, user.ID)
	if err != nil {
		return nil, 0, err
	}
	revision, err := a.store.GenerationJobRevision(r.Context())
	return views, revision, err
}

func (a *App) handleGenerationJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	views, revision, err := a.loadGenerationJobViews(r)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить задания генерации")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": views, "revision": revision})
}

func (a *App) handleGenerationJobDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	job, err := a.store.GenerationJobByPublicID(r.Context(), user.ID, strings.TrimSpace(r.URL.Query().Get("job_id")))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить задание")
		return
	}
	transitions, err := a.store.GenerationJobTransitions(r.Context(), job.ID, user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить этапы задания")
		return
	}
	items := make([]generationJobTransitionView, 0, len(transitions))
	for _, transition := range transitions {
		items = append(items, generationJobTransitionView{
			State: string(transition.ToState), Message: transition.Message, ErrorCode: transition.ErrorCode,
			Attempt: transition.Attempt, DurationMS: transition.DurationMS, CorrelationID: transition.CorrelationID, CreatedAt: transition.CreatedAt,
		})
	}
	views, err := a.generationJobViews(r.Context(), []domain.GenerationJob{job}, user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить результат задания")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": views[0], "transitions": items})
}

func (a *App) handleGenerationJobEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "потоковые обновления не поддерживаются", http.StatusInternalServerError)
		return
	}
	lastRevision, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("since")), 10, 64)
	revision, err := a.store.GenerationJobRevision(r.Context())
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось открыть поток заданий")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = io.WriteString(w, "retry: 2000\n\n")
	event := "ready"
	if revision != lastRevision {
		event = "jobs"
	}
	if err := writeGenerationJobRevisionEvent(w, event, revision); err != nil {
		return
	}
	flusher.Flush()
	lastRevision = revision

	poll := time.NewTicker(generationJobEventPollInterval)
	heartbeat := time.NewTicker(generationJobEventHeartbeatInterval)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
			revision, err = a.store.GenerationJobRevision(r.Context())
			if err != nil {
				log.Printf("generation job event stream: %v", err)
				return
			}
			if revision == lastRevision {
				continue
			}
			if err := writeGenerationJobRevisionEvent(w, "jobs", revision); err != nil {
				return
			}
			flusher.Flush()
			lastRevision = revision
		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ": keepalive %d\n\n", time.Now().Unix()); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeGenerationJobRevisionEvent(w io.Writer, event string, revision int64) error {
	_, err := fmt.Fprintf(w, "event: %s\ndata: %d\n\n", event, revision)
	return err
}

func (a *App) handleGenerationJobCancel(w http.ResponseWriter, r *http.Request) {
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
	user := a.currentUser(r)
	job, err := a.store.GenerationJobByPublicID(r.Context(), user.ID, strings.TrimSpace(r.Form.Get("job_id")))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить задание")
		return
	}
	jobCtx := generationJobTraceContext(r.Context(), job)
	if job.State.Terminal() {
		writeJSON(w, http.StatusOK, map[string]any{"job": a.generationJobView(job), "cancelled": job.State == domain.GenerationJobCancelled})
		return
	}
	job, _, err = a.store.RequestGenerationJobCancellation(jobCtx, job.ID, user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusConflict, "это задание уже нельзя отменить")
		return
	}
	job, cancelled, err := a.continueGenerationJobCancellation(jobCtx, job)
	if err != nil {
		if errors.Is(err, errGenerationCancellationNotSent) {
			if cleared, _, clearErr := a.store.ClearGenerationJobCancellation(jobCtx, job.ID, user.ID, "Генерация продолжается"); clearErr == nil {
				job = cleared
			}
		}
		writeGenerationError(w, http.StatusBadGateway, "не удалось отменить задание: "+err.Error())
		return
	}
	if cancelled {
		a.audit(jobCtx, &user.ID, "quick_generation_cancelled", "comfyui", nil, a.clientIP(r), r.UserAgent(), map[string]any{"prompt_id": job.PromptID, "job_id": job.PublicID})
	}
	status := http.StatusAccepted
	if job.State.Terminal() {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"job": a.generationJobView(job), "cancelled": cancelled})
}

func (a *App) handleGenerationJobRetry(w http.ResponseWriter, r *http.Request) {
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
	user := a.currentUser(r)
	job, err := a.store.GenerationJobByPublicID(r.Context(), user.ID, strings.TrimSpace(r.Form.Get("job_id")))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить задание")
		return
	}
	if !job.State.Terminal() {
		writeGenerationError(w, http.StatusConflict, "сначала дождитесь завершения или отмените это задание")
		return
	}
	if len(job.PayloadCipher) == 0 {
		writeGenerationError(w, http.StatusGone, "параметры этого задания больше недоступны")
		return
	}
	payload, err := a.decodeGenerationSavedPayload(job.PayloadCipher)
	if err != nil {
		writeGenerationError(w, http.StatusGone, "не удалось восстановить параметры этого задания")
		return
	}
	delete(payload.Values, "client_request_id")
	delete(payload.Values, "csrf")
	writeJSON(w, http.StatusOK, map[string]any{
		"parent_job_id": job.PublicID, "values": payload.Values,
		"requires_inputs": job.InputCount > 0,
	})
}
