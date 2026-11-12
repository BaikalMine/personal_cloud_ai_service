package gateway

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type taskView struct {
	domain.Task
	DetailURL  string           `json:"detail_url"`
	OpenURL    string           `json:"open_url,omitempty"`
	Preview    *taskPreviewView `json:"preview,omitempty"`
	NextAction string           `json:"next_action"`
}

type taskPreviewView struct {
	URL         string `json:"url"`
	Sensitive   bool   `json:"sensitive"`
	ExpiresUnix int64  `json:"expires_unix"`
	Label       string `json:"label"`
	SourceType  string `json:"source_type"`
}

func taskAccessScope(user *User) domain.TaskScope {
	if user == nil {
		return domain.TaskScope{}
	}
	return domain.TaskScope{UserID: user.ID, Generation: user.CanUseService("quick_generation"),
		Training: user.Role == "admin" || user.CanTrainImageLora}
}

func (a *App) handleTasksPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	scope := taskAccessScope(a.currentUser(r))
	if !scope.Generation && !scope.Training {
		a.forbiddenPage(w, r, "Для просмотра задач нужен доступ к генерации или обучению LoRA.")
		return
	}
	a.render(w, r, "tasks", map[string]any{"Title": "Задачи", "TaskGenerationAccess": scope.Generation, "TaskTrainingAccess": scope.Training})
}

func parseTaskQuery(values url.Values) (domain.TaskFilter, int, string, error) {
	f := domain.TaskFilter{}
	for key, values := range values {
		switch key {
		case "kind", "state", "search", "from", "before", "limit", "cursor":
		default:
			return f, 0, "", store.ErrTaskQuery
		}
		if len(values) != 1 {
			return f, 0, "", store.ErrTaskQuery
		}
	}
	f.Kind, f.State, f.Search = values.Get("kind"), values.Get("state"), values.Get("search")
	for key, dest := range map[string]**time.Time{"from": &f.From, "before": &f.Before} {
		if value := values.Get(key); value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return f, 0, "", store.ErrTaskQuery
			}
			*dest = &parsed
		}
	}
	limit := 40
	if value := values.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			return f, 0, "", store.ErrTaskQuery
		}
		limit = parsed
	}
	return f, limit, values.Get("cursor"), nil
}

func makeTaskView(task domain.Task) taskView {
	v := taskView{Task: task, NextAction: taskNextAction(task, time.Now())}
	if p := task.Preview; p != nil {
		preview := taskPreviewView{Sensitive: p.Sensitive, ExpiresUnix: p.ExpiresAt.UnixMilli(), SourceType: p.MediaType}
		preview.URL = taskThumbnailURL(task)
		preview.Label = "Результат генерации"
		if p.MediaType == "video" {
			preview.Label = "Первый кадр видео"
		}
		if task.Kind == "lora_caption" {
			preview.Label = "Исходный кадр описания"
		}
		if task.Kind == "lora_training" {
			preview.Label = "Кадр исходного набора обучения"
		}
		if preview.URL != "" {
			v.Preview = &preview
		}
	}
	switch task.Kind {
	case "generation":
		v.DetailURL = "/generate/jobs/detail?job_id=" + url.QueryEscape(task.ID)
		v.OpenURL = "/generate?job=" + url.QueryEscape(task.ID)
	case "lora_training":
		v.DetailURL = "/api/lora-training/jobs/" + url.PathEscape(task.ID)
	case "lora_caption":
		v.DetailURL = "/api/lora-training/caption/" + url.PathEscape(task.ID)
	case "lora_caption_series":
		v.DetailURL = "/api/tasks/detail?kind=lora_caption_series&id=" + url.QueryEscape(task.ID)
	}
	return v
}

func (a *App) handleTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
		return
	}
	scope := taskAccessScope(user)
	if !scope.Generation && !scope.Training {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "task_access_denied"})
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_task_query"})
		return
	}
	filter, limit, cursor, err := parseTaskQuery(values)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_task_query"})
		return
	}
	page, err := a.store.ListTasks(r.Context(), scope, filter, limit, cursor)
	if errors.Is(err, store.ErrTaskQuery) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_task_query"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "task_list_unavailable"})
		return
	}
	views := make([]taskView, 0, len(page.Tasks))
	for _, task := range page.Tasks {
		views = append(views, makeTaskView(task))
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 1, "tasks": views,
		"total": page.Total, "revision": page.Revision, "next_cursor": page.NextCursor,
		"access": map[string]bool{"generation": scope.Generation, "training": scope.Training}})
}
