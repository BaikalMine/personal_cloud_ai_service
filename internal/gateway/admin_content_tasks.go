package gateway

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

type decodedContentEvent struct {
	row      domain.ContentEventRow
	prompt   string
	response string
	metadata string
}

type contentTaskGroup struct {
	key    string
	job    *domain.GenerationJob
	events []decodedContentEvent
}

func (a *App) buildContentTaskViews(rows []domain.ContentEventRow, jobs []domain.GenerationJob, mediaByEvent map[int64][]domain.ContentMediaSummary, service, query string, limit int) ([]ContentEventView, ContentOverview) {
	groups := make(map[string]*contentTaskGroup, len(rows)+len(jobs))
	jobKeyByID := make(map[int64]string, len(jobs))
	jobKeyByCorrelation := make(map[string]string, len(jobs))
	for index := range jobs {
		job := &jobs[index]
		key := "job-" + job.PublicID
		groups[key] = &contentTaskGroup{key: key, job: job}
		jobKeyByID[job.ID] = key
		if job.CorrelationID != "" {
			jobKeyByCorrelation[job.CorrelationID] = key
		}
	}
	assistantCorrelations := make(map[string]struct{})
	for _, row := range rows {
		if row.Kind == "prompt_assistant" && row.CorrelationID != "" {
			assistantCorrelations[row.CorrelationID] = struct{}{}
		}
	}

	for _, row := range rows {
		prompt, promptErr := a.contentCipher.Decrypt(row.PromptCipher)
		response, responseErr := a.contentCipher.Decrypt(row.ResponseCipher)
		metadata, metadataErr := a.contentCipher.Decrypt(row.MetadataCipher)
		if promptErr != nil || responseErr != nil || metadataErr != nil {
			prompt, response, metadata = "[ошибка расшифровки]", "", ""
		}
		key := "event-" + strconv.FormatInt(row.ID, 10)
		if row.GenerationJobID != nil {
			if jobKey := jobKeyByID[*row.GenerationJobID]; jobKey != "" {
				key = jobKey
			} else {
				key = "job-id-" + strconv.FormatInt(*row.GenerationJobID, 10)
			}
		} else if _, hasAssistant := assistantCorrelations[row.CorrelationID]; row.CorrelationID != "" && hasAssistant {
			if jobKey := jobKeyByCorrelation[row.CorrelationID]; jobKey != "" {
				key = jobKey
			} else {
				key = "trace-" + row.CorrelationID
			}
		}
		group := groups[key]
		if group == nil {
			group = &contentTaskGroup{key: key}
			groups[key] = group
		}
		group.events = append(group.events, decodedContentEvent{row: row, prompt: prompt, response: response, metadata: metadata})
	}

	views := make([]ContentEventView, 0, len(groups))
	queryLower := strings.ToLower(strings.TrimSpace(query))
	for _, group := range groups {
		view := a.contentTaskView(group, mediaByEvent)
		if service != "" && view.Service != service {
			continue
		}
		if queryLower != "" && !strings.Contains(strings.ToLower(contentTaskSearchText(view)), queryLower) {
			continue
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].CreatedAt.Equal(views[j].CreatedAt) {
			return views[i].Key > views[j].Key
		}
		return views[i].CreatedAt.After(views[j].CreatedAt)
	})

	overview := ContentOverview{}
	for _, view := range views {
		overview.Total++
		switch view.Service {
		case "comfyui":
			overview.ComfyUI++
		case "openwebui":
			overview.OpenWebUI++
		}
		if view.MediaCount > 0 {
			overview.WithMedia++
		}
	}
	if limit > 0 && len(views) > limit {
		views = views[:limit]
	}
	return views, overview
}

func (a *App) contentTaskView(group *contentTaskGroup, mediaByEvent map[int64][]domain.ContentMediaSummary) ContentEventView {
	primary := primaryContentEvent(group.events)
	view := ContentEventView{Key: group.key, Service: "comfyui", Kind: "generation_task"}
	if primary != nil {
		view.ID = primary.row.ID
		view.UserID = primary.row.UserID
		view.Username = primary.row.Username
		view.AuthorDeleted = primary.row.AuthorDeleted
		view.GenerationJobID = primary.row.GenerationJobID
		view.CorrelationID = primary.row.CorrelationID
		view.Service = primary.row.Service
		view.Kind = primary.row.Kind
		view.ExternalID = primary.row.ExternalID
		view.Model = primary.row.Model
		view.Prompt = primary.prompt
		view.Response = primary.response
		view.Metadata = prettyContentMetadata(primary.metadata)
		view.Assistant = contentAssistantFromMetadata(primary.metadata)
		view.GenerationState = primary.row.GenerationState
		view.Sensitive = primary.row.Sensitive
		view.GeneratedMediaCount = primary.row.GeneratedMediaCount
		view.MediaExpiresAt = primary.row.MediaExpiresAt
		view.CreatedAt = primary.row.CreatedAt
		view.UpdatedAt = primary.row.UpdatedAt
		view.ExpiresAt = primary.row.ExpiresAt
	}

	for _, event := range group.events {
		if view.CreatedAt.IsZero() || event.row.CreatedAt.Before(view.CreatedAt) {
			view.CreatedAt = event.row.CreatedAt
		}
		if event.row.UpdatedAt.After(view.UpdatedAt) {
			view.UpdatedAt = event.row.UpdatedAt
		}
		if event.row.ExpiresAt.After(view.ExpiresAt) {
			view.ExpiresAt = event.row.ExpiresAt
		}
		if event.row.MediaExpiresAt.After(view.MediaExpiresAt) {
			view.MediaExpiresAt = event.row.MediaExpiresAt
		}
		if event.row.GeneratedMediaCount > view.GeneratedMediaCount {
			view.GeneratedMediaCount = event.row.GeneratedMediaCount
		}
		view.Sensitive = view.Sensitive || event.row.Sensitive
		for _, media := range mediaByEvent[event.row.ID] {
			view.Media = append(view.Media, media)
			if media.VisualPending {
				view.VisualPending = true
			}
			if media.UpdatedAt.After(view.UpdatedAt) {
				view.UpdatedAt = media.UpdatedAt
			}
		}
		if event.row.Kind == "prompt_assistant" {
			assistant := contentAssistantFromMetadata(event.metadata)
			if assistant == nil {
				assistant = &ContentAssistantView{OriginalPrompt: event.prompt, Suggestion: event.response}
			}
			assistant.Model = event.row.Model
			if view.Assistant == nil {
				view.Assistant = assistant
			} else if view.Assistant.Model == "" {
				view.Assistant.Model = assistant.Model
			}
		}
	}

	if group.job != nil {
		job := group.job
		view.Service = "comfyui"
		view.Kind = "generation_task"
		jobID := job.ID
		view.GenerationJobID = &jobID
		view.JobID = job.PublicID
		view.RequestID = job.RequestID
		view.CorrelationID = job.CorrelationID
		view.JobState = string(job.State)
		view.StatusMessage = job.StatusMessage
		view.ErrorCode = job.ErrorCode
		view.ErrorMessage = job.ErrorMessage
		view.GenerationState = contentGenerationState(job.State)
		if job.ModelName != "" {
			view.Model = job.ModelName
		}
		if job.UserID == nil {
			view.UserID = 0
			view.AuthorDeleted = true
		} else {
			view.UserID = *job.UserID
		}
		if job.UsernameSnapshot != "" {
			view.Username = job.UsernameSnapshot
		}
		if view.CreatedAt.IsZero() || job.CreatedAt.Before(view.CreatedAt) {
			view.CreatedAt = job.CreatedAt
		}
		if job.UpdatedAt.After(view.UpdatedAt) {
			view.UpdatedAt = job.UpdatedAt
		}
		if view.ExpiresAt.IsZero() {
			view.ExpiresAt = job.CreatedAt.Add(a.retentionPolicy().AIContent)
		}
		if len(job.PayloadCipher) > 0 {
			if payload, err := a.decodeGenerationSavedPayload(job.PayloadCipher); err == nil {
				if view.Prompt == "" {
					view.Prompt = strings.TrimSpace(payload.Values["positive_prompt"])
				}
				if view.Response == "" {
					view.Response = strings.TrimSpace(payload.Values["negative_prompt"])
				}
				if view.Assistant == nil {
					view.Assistant = contentAssistantFromValues(payload.Values)
				}
				if view.Metadata == "" {
					view.Metadata = prettyGenerationJobValues(payload.Values)
				}
			}
		}
	}

	if view.Username == "" {
		view.Username = "Удалённый пользователь"
		view.AuthorDeleted = true
	}
	if view.UpdatedAt.IsZero() {
		view.UpdatedAt = view.CreatedAt
	}
	view.MediaCount = int64(len(view.Media))
	view.Sensitive = view.Sensitive || view.VisualPending
	view.MediaExpired = view.GeneratedMediaCount > 0 && view.MediaCount == 0 && !view.MediaExpiresAt.IsZero() && view.MediaExpiresAt.Before(time.Now())
	view.StateLabel, view.StateClass = contentStatePresentation(view.JobState, view.GenerationState, view.Kind)
	view.Version = fmt.Sprintf("%d-%s-%d-%t-%t", view.UpdatedAt.UnixNano(), view.GenerationState, view.MediaCount, view.Sensitive, view.MediaExpired)
	return view
}

func primaryContentEvent(events []decodedContentEvent) *decodedContentEvent {
	for index := range events {
		if events[index].row.Kind == "comfyui_prompt" {
			return &events[index]
		}
	}
	for index := range events {
		if events[index].row.Kind == "openwebui_chat" {
			return &events[index]
		}
	}
	if len(events) > 0 {
		return &events[0]
	}
	return nil
}

func contentAssistantFromValues(values map[string]string) *ContentAssistantView {
	if values["assistant_requested"] != "true" && strings.TrimSpace(values["assistant_original_prompt"]) == "" {
		return nil
	}
	return &ContentAssistantView{
		Applied: values["assistant_applied"] == "true", Decision: values["assistant_action"], Template: values["assistant_template_used"],
		Think: values["assistant_think_used"] == "true", OriginalPrompt: values["assistant_original_prompt"],
		Suggestion: values["assistant_suggestion"],
	}
}

func prettyGenerationJobValues(values map[string]string) string {
	filtered := make(map[string]string, len(values))
	for key, value := range values {
		if strings.HasPrefix(key, "assistant_") || key == "positive_prompt" || key == "negative_prompt" || strings.HasPrefix(key, "input_") {
			continue
		}
		filtered[key] = value
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return ""
	}
	return prettyContentMetadata(string(encoded))
}

func contentTaskSearchText(view ContentEventView) string {
	parts := []string{view.Username, view.Service, view.Kind, view.ExternalID, view.Model, view.Prompt, view.Response,
		view.Metadata, view.JobID, view.RequestID, view.CorrelationID, view.StatusMessage, view.ErrorCode, view.ErrorMessage}
	if view.Assistant != nil {
		parts = append(parts, view.Assistant.OriginalPrompt, view.Assistant.Suggestion, view.Assistant.Template, view.Assistant.Model)
	}
	return strings.Join(parts, "\n")
}

func contentGenerationState(state domain.GenerationJobState) string {
	switch state {
	case domain.GenerationJobCompleted:
		return "completed"
	case domain.GenerationJobFailed:
		return "error"
	case domain.GenerationJobCancelled, domain.GenerationJobExpired:
		return "cancelled"
	case domain.GenerationJobRunning, domain.GenerationJobPostprocessing, domain.GenerationJobArchiving:
		return "running"
	default:
		return "queued"
	}
}

func contentStatePresentation(jobState, generationState, kind string) (string, string) {
	labels := map[string]string{
		"draft": "Создано", "preparing": "Подготовка", "uploading": "Референсы", "waiting_for_resources": "Ожидает ресурсы",
		"queued": "В очереди", "running": "Генерация", "postprocessing": "Обработка", "archiving": "Сохранение",
		"completed": "Готово", "failed": "Ошибка", "cancelled": "Отменено", "expired": "Истекло",
	}
	if label := labels[jobState]; label != "" {
		switch jobState {
		case "completed":
			return label, "is-complete"
		case "failed":
			return label, "is-error"
		case "cancelled", "expired":
			return label, "is-cancelled"
		default:
			return label, "is-pending"
		}
	}
	if kind == "prompt_assistant" {
		return "Черновик", "is-assistant"
	}
	switch generationState {
	case "completed":
		return "Готово", "is-complete"
	case "error":
		return "Ошибка", "is-error"
	case "cancelled":
		return "Отменено", "is-cancelled"
	case "queued":
		return "В очереди", "is-pending"
	case "running":
		return "В работе", "is-pending"
	default:
		return "Сохранено", ""
	}
}

func contentStageViews(items []domain.GenerationJobTransition) []ContentStageView {
	views := make([]ContentStageView, 0, len(items))
	for _, item := range items {
		label, tone := contentStatePresentation(string(item.ToState), contentGenerationState(item.ToState), "")
		views = append(views, ContentStageView{
			State: string(item.ToState), Label: label, Message: item.Message, ErrorMessage: item.ErrorMessage,
			Tone: tone, DurationMS: item.DurationMS, CreatedAt: item.CreatedAt,
		})
	}
	return views
}
