package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"ai-access-gateway/internal/promptassistant"
)

const maxLoraCaptionRequestBytes = maxLoraTrainingImageBytes + (512 << 10)

const (
	maxQueuedLoraCaptionJobsPerUser = 4
	loraCaptionJobRetention         = 20 * time.Minute
	loraCaptionWorkerOverhead       = 2 * time.Minute
)

var (
	errLoraCaptionCSRF     = errors.New("проверка безопасности не пройдена")
	errLoraCaptionTooLarge = errors.New("изображение должно быть не больше 24 МБ")
)

type loraCaptionSubmission struct {
	TriggerWord string
	ConceptType string
	Filename    string
	MIMEType    string
	Image       []byte
}

type loraCaptionJobState string

const (
	loraCaptionQueued    loraCaptionJobState = "queued"
	loraCaptionRunning   loraCaptionJobState = "running"
	loraCaptionCompleted loraCaptionJobState = "completed"
	loraCaptionFailed    loraCaptionJobState = "failed"
)

type loraCaptionJob struct {
	ID        string
	UserID    int64
	State     loraCaptionJobState
	Status    string
	Caption   string
	Model     string
	Warning   string
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
}

type loraCaptionJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*loraCaptionJob
}

func newLoraCaptionJobRegistry() *loraCaptionJobRegistry {
	return &loraCaptionJobRegistry{jobs: make(map[string]*loraCaptionJob)}
}

func (a *App) loraCaptionJobRegistry() *loraCaptionJobRegistry {
	a.loraCaptionMu.Lock()
	defer a.loraCaptionMu.Unlock()
	if a.loraCaptionJobs == nil {
		a.loraCaptionJobs = newLoraCaptionJobRegistry()
	}
	return a.loraCaptionJobs
}

func (registry *loraCaptionJobRegistry) enqueue(userID int64) (loraCaptionJob, bool) {
	now := time.Now().UTC()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for id, job := range registry.jobs {
		if job.ExpiresAt.Before(now) {
			delete(registry.jobs, id)
		}
	}
	pending := 0
	for _, job := range registry.jobs {
		if job.UserID == userID && (job.State == loraCaptionQueued || job.State == loraCaptionRunning) {
			pending++
		}
	}
	if pending >= maxQueuedLoraCaptionJobsPerUser {
		return loraCaptionJob{}, false
	}
	job := &loraCaptionJob{
		ID:        newRequestID(),
		UserID:    userID,
		State:     loraCaptionQueued,
		Status:    "Описание поставлено в очередь ассистента",
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: now.Add(loraCaptionJobRetention),
	}
	registry.jobs[job.ID] = job
	return *job, true
}

func (registry *loraCaptionJobRegistry) get(userID int64, id string) (loraCaptionJob, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	job, ok := registry.jobs[id]
	if !ok || job.UserID != userID || job.ExpiresAt.Before(time.Now().UTC()) {
		return loraCaptionJob{}, false
	}
	return *job, true
}

func (registry *loraCaptionJobRegistry) update(id string, state loraCaptionJobState, status string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if job := registry.jobs[id]; job != nil {
		job.State = state
		job.Status = status
		job.UpdatedAt = time.Now().UTC()
	}
}

func (registry *loraCaptionJobRegistry) complete(id, caption, model, warning string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if job := registry.jobs[id]; job != nil {
		now := time.Now().UTC()
		job.State = loraCaptionCompleted
		job.Status = "Описание готово"
		job.Caption = caption
		job.Model = model
		job.Warning = warning
		job.Error = ""
		job.UpdatedAt = now
		job.ExpiresAt = now.Add(loraCaptionJobRetention)
	}
}

func (registry *loraCaptionJobRegistry) fail(id, message string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if job := registry.jobs[id]; job != nil {
		now := time.Now().UTC()
		job.State = loraCaptionFailed
		job.Status = "Описание не удалось подготовить"
		job.Error = message
		job.UpdatedAt = now
		job.ExpiresAt = now.Add(loraCaptionJobRetention)
	}
}

func (a *App) handleLoraTrainingCaption(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeGenerationError(w, http.StatusMethodNotAllowed, "метод не поддерживается")
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeGenerationError(w, http.StatusUnauthorized, "требуется вход")
		return
	}
	if a.promptAssistant == nil || !a.promptAssistant.VisionConfigured() {
		writeGenerationError(w, http.StatusServiceUnavailable, "локальная vision-модель не настроена")
		return
	}
	submission, err := a.readLoraCaptionSubmission(w, r)
	if err != nil {
		clear(submission.Image)
		submission.Image = nil
		status := http.StatusBadRequest
		if errors.Is(err, errLoraCaptionCSRF) {
			status = http.StatusForbidden
		} else if errors.Is(err, errLoraCaptionTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeGenerationError(w, status, err.Error())
		return
	}
	imageBytes := len(submission.Image)
	if err := validateLoraTriggerWord(submission.TriggerWord); err != nil {
		clear(submission.Image)
		submission.Image = nil
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validLoraConceptType(submission.ConceptType) {
		clear(submission.Image)
		submission.Image = nil
		writeGenerationError(w, http.StatusBadRequest, "выберите тип LoRA")
		return
	}
	job, queued := a.loraCaptionJobRegistry().enqueue(user.ID)
	if !queued {
		clear(submission.Image)
		submission.Image = nil
		writeGenerationError(w, http.StatusTooManyRequests, "уже ожидают обработки несколько описаний; дождитесь завершения или повторите позже")
		return
	}
	userCopy := *user
	go a.runLoraCaptionJob(job.ID, userCopy, submission, imageBytes, a.clientIP(r), r.UserAgent())
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":  job.ID,
		"state":   job.State,
		"status":  job.Status,
		"expires": job.ExpiresAt.Unix(),
	})
}

func (a *App) runLoraCaptionJob(jobID string, user User, submission loraCaptionSubmission, imageBytes int, clientIP, userAgent string) {
	defer func() {
		clear(submission.Image)
		submission.Image = nil
		if imageBytes >= 8<<20 {
			debug.FreeOSMemory()
		}
	}()
	policy := a.promptAssistant.PolicyForRequest(promptassistant.ModeImageToImage, promptassistant.ProfileWorkflowDefault, false, true)
	ctx, cancel := context.WithTimeout(context.Background(), policy.Timeout+loraCaptionWorkerOverhead)
	defer cancel()
	registry := a.loraCaptionJobRegistry()
	registry.update(jobID, loraCaptionRunning, "Ожидаем доступ к vision-модели")
	releaseAssistant, acquired := acquireBoundedSlot(ctx, a.promptAssistantSlots, policy.Timeout+loraCaptionWorkerOverhead)
	if !acquired {
		registry.fail(jobID, "ассистент не освободился вовремя; повторите описание")
		return
	}
	defer releaseAssistant()
	releaseMedia, acquired := a.mediaByteLimiter().tryAcquire(promptAssistantMemoryReservation)
	if !acquired {
		registry.fail(jobID, "обработка изображений уже заняла доступную память; повторите позже")
		return
	}
	defer releaseMedia()

	registry.update(jobID, loraCaptionRunning, "Подготавливаем vision-модель")
	miningLease, miningWarning, err := a.pauseMiningForQuickGeneration(ctx, &user, 0)
	if err != nil {
		registry.fail(jobID, "не удалось освободить ресурсы для ассистента")
		log.Printf("LoRA caption job %s could not pause mining: %v", jobID, err)
		return
	}
	if miningLease != nil {
		defer func() {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer releaseCancel()
			a.releaseMiningPause(releaseCtx, miningLease.ID)
		}()
	}

	registry.update(jobID, loraCaptionRunning, "Ассистент анализирует кадр")
	started := time.Now()
	result, err := a.promptAssistant.CaptionImage(ctx, submission.TriggerWord, submission.ConceptType, submission.Image, submission.MIMEType)
	a.observeServiceCall(ctx, dependencyOllama, "caption_lora_image", started, err, false, "assistant_request_failed", "")
	if err != nil {
		registry.fail(jobID, "локальная модель не смогла подготовить описание; повторите позже")
		log.Printf("LoRA caption job %s failed: %v", jobID, err)
		return
	}
	caption := truncateLoraText(ensureLoraCaptionTrigger(submission.TriggerWord, result.Caption), promptassistant.MaxLoraCaptionCharacters)
	if caption == "" {
		registry.fail(jobID, "локальная модель вернула пустое описание")
		return
	}
	a.audit(ctx, &user.ID, "lora_training_caption_generated", "lora_training_dataset", nil, clientIP, userAgent, map[string]any{
		"concept_type":  submission.ConceptType,
		"model":         result.Model,
		"filename":      submission.Filename,
		"image_bytes":   imageBytes,
		"caption_chars": utf8.RuneCountInString(caption),
		"usage":         result.Usage,
		"policy":        result.Policy,
	})
	registry.complete(jobID, caption, result.Model, miningWarning)
}

func (a *App) handleLoraTrainingCaptionStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeGenerationError(w, http.StatusMethodNotAllowed, "метод не поддерживается")
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeGenerationError(w, http.StatusUnauthorized, "требуется вход")
		return
	}
	jobID := strings.TrimPrefix(r.URL.Path, "/api/lora-training/caption/")
	if jobID == "" || strings.Contains(jobID, "/") {
		writeGenerationError(w, http.StatusNotFound, "описание не найдено")
		return
	}
	job, ok := a.loraCaptionJobRegistry().get(user.ID, jobID)
	if !ok {
		writeGenerationError(w, http.StatusNotFound, "описание не найдено или срок ожидания истёк")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":  job.ID,
		"state":   job.State,
		"status":  job.Status,
		"caption": job.Caption,
		"model":   job.Model,
		"warning": job.Warning,
		"error":   job.Error,
		"expires": job.ExpiresAt.Unix(),
	})
}

func (a *App) readLoraCaptionSubmission(w http.ResponseWriter, r *http.Request) (loraCaptionSubmission, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoraCaptionRequestBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		return loraCaptionSubmission{}, errors.New("не удалось прочитать запрос автописания")
	}
	result := loraCaptionSubmission{}
	csrfVerified := false
	imageCount := 0
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			if strings.Contains(strings.ToLower(partErr.Error()), "too large") {
				return result, errLoraCaptionTooLarge
			}
			return result, errors.New("не удалось прочитать запрос автописания")
		}
		name := part.FormName()
		if part.FileName() == "" {
			value, readErr := io.ReadAll(io.LimitReader(part, (16<<10)+1))
			part.Close()
			if readErr != nil || len(value) > 16<<10 {
				return result, errors.New("поле запроса автописания слишком длинное")
			}
			switch name {
			case "csrf":
				csrfVerified = a.validCSRFValue(r, strings.TrimSpace(string(value)))
				if !csrfVerified {
					return result, errLoraCaptionCSRF
				}
			case "trigger_word":
				result.TriggerWord = strings.TrimSpace(string(value))
			case "concept_type":
				result.ConceptType = strings.TrimSpace(string(value))
			}
			continue
		}
		if name != "image" || !csrfVerified {
			part.Close()
			if !csrfVerified {
				return result, errLoraCaptionCSRF
			}
			return result, errors.New("за один запрос можно отправить только одно изображение")
		}
		imageCount++
		if imageCount > 1 {
			part.Close()
			return result, errors.New("за один запрос можно отправить только одно изображение")
		}
		payload, readErr := io.ReadAll(io.LimitReader(part, maxLoraTrainingImageBytes+1))
		filename := filepath.Base(part.FileName())
		part.Close()
		if readErr != nil || len(payload) == 0 || len(payload) > maxLoraTrainingImageBytes {
			clear(payload)
			return result, errLoraCaptionTooLarge
		}
		result.Image = payload
		result.Filename = filename
	}
	if !csrfVerified {
		return result, errLoraCaptionCSRF
	}
	if imageCount != 1 || len(result.Image) == 0 {
		return result, errors.New("добавьте одно изображение для описания")
	}
	headerLength := min(len(result.Image), 512)
	mimeType := http.DetectContentType(result.Image[:headerLength])
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return result, fmt.Errorf("%s: поддерживаются только PNG, JPG и WebP", result.Filename)
	}
	prepared, preparedMIME, _, err := prepareVisionReference(result.Image, mimeType)
	if err != nil {
		return result, fmt.Errorf("%s: не удалось подготовить изображение", result.Filename)
	}
	if &prepared[0] != &result.Image[0] {
		clear(result.Image)
	}
	result.Image = prepared
	result.MIMEType = preparedMIME
	return result, nil
}

func ensureLoraCaptionTrigger(trigger, caption string) string {
	trigger = strings.TrimSpace(trigger)
	caption = strings.TrimSpace(caption)
	if trigger == "" {
		return caption
	}
	for {
		rest, matched := trimLoraTriggerPrefix(caption, trigger)
		if !matched {
			break
		}
		caption = rest
	}
	if caption == "" {
		return trigger
	}
	return trigger + ", " + caption
}

func trimLoraTriggerPrefix(caption, trigger string) (string, bool) {
	captionRunes := []rune(strings.TrimSpace(caption))
	triggerRunes := []rune(strings.TrimSpace(trigger))
	if len(triggerRunes) == 0 || len(captionRunes) < len(triggerRunes) || !strings.EqualFold(string(captionRunes[:len(triggerRunes)]), string(triggerRunes)) {
		return caption, false
	}
	if len(captionRunes) > len(triggerRunes) {
		next := captionRunes[len(triggerRunes)]
		if !unicode.IsSpace(next) && !unicode.IsPunct(next) {
			return caption, false
		}
	}
	rest := strings.TrimLeftFunc(string(captionRunes[len(triggerRunes):]), func(value rune) bool {
		return unicode.IsSpace(value) || unicode.IsPunct(value)
	})
	return rest, true
}
