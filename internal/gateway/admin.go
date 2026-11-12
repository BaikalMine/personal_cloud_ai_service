package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

const (
	maxConcurrentAdminMediaResponses = 2
)

func (a *App) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" {
		http.NotFound(w, r)
		return
	}
	probeCtx, probeCancel := context.WithTimeout(r.Context(), 4*time.Second)
	a.refreshDependencyStatuses(probeCtx)
	probeCancel()
	system, err := a.systemOverview(r.Context())
	if err != nil {
		http.Error(w, "ошибка системной статистики", http.StatusInternalServerError)
		return
	}
	operations, err := a.loadAdminOperations(r.Context(), system)
	if err != nil {
		http.Error(w, "ошибка операционной сводки", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "admin_dashboard", map[string]any{
		"Title":      "Операции",
		"System":     system,
		"Operations": operations,
	})
}

func (a *App) handleAdminRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/")
	switch {
	case path == "models":
		a.handleModelLibrary(w, r)
	case path == "users":
		a.handleAdminUsers(w, r)
	case strings.HasPrefix(path, "users/"):
		a.handleAdminUserDetail(w, r, strings.TrimPrefix(path, "users/"))
	case path == "invites":
		a.handleAdminInvites(w, r)
	case path == "invite-templates":
		a.handleAdminInviteTemplates(w, r)
	case strings.HasPrefix(path, "invites/"):
		a.handleAdminInviteAction(w, r, strings.TrimPrefix(path, "invites/"))
	case path == "sessions":
		a.handleAdminSessions(w, r)
	case strings.HasPrefix(path, "sessions/"):
		a.handleAdminSessionAction(w, r, strings.TrimPrefix(path, "sessions/"))
	case path == "metrics":
		a.handleAdminMetricsPage(w, r)
	case path == "harness-ultra" || strings.HasPrefix(path, "harness-ultra/"):
		a.handleAdminUltra(w, r)
	case strings.HasPrefix(path, "jobs/"):
		a.handleAdminGenerationJobTrace(w, r, strings.TrimPrefix(path, "jobs/"))
	case path == "storage":
		a.handleAdminStorage(w, r)
	case path == "storage/training" || path == "storage/training/preview":
		a.handleAdminTrainingStorage(w, r)
	case path == "storage/comfy" || path == "storage/comfy/inspect":
		a.handleAdminComfyStorage(w, r)
	case path == "storage/comfy/files":
		a.handleAdminComfyFiles(w, r)
	case path == "storage/comfy/unresolved-inputs":
		a.handleAdminUnresolvedInputs(w, r)
	case path == "storage/training/operations" || strings.HasPrefix(path, "storage/training/operations/"):
		a.handleAdminTrainingStorageOperations(w, r)
	case path == "storage/comfy/operations" || strings.HasPrefix(path, "storage/comfy/operations/"):
		a.handleAdminComfyStorageOperations(w, r)
	case path == "system/overview":
		a.handleAdminSystemOverview(w, r)
	case path == "operations/overview":
		a.handleAdminOperationsOverview(w, r)
	case path == "resources/overview":
		a.handleAdminResourceOverview(w, r)
	case path == "services/comfyui/runtime":
		a.handleAdminComfyRuntime(w, r)
	case path == "mining":
		a.handleAdminMining(w, r)
	case path == "mining/script":
		a.handleAdminMiningScript(w, r)
	case path == "mining/favorite":
		a.handleAdminMiningFavorite(w, r)
	case path == "updates":
		a.handleAdminUpdates(w, r)
	case path == "release" || path == "release/status":
		a.handleAdminRelease(w, r)
	case path == "release/binding":
		a.handleAdminReleaseBinding(w, r)
	case path == "workflows":
		a.handleAdminWorkflows(w, r)
	case path == "lora-training" || path == "lora-training/overview":
		a.handleAdminLoraTraining(w, r)
	case path == "lora-training/gpu":
		a.handleAdminTrainingGPU(w, r)
	case strings.HasPrefix(path, "lora-training/"):
		a.handleAdminLoraTrainingAction(w, r, strings.TrimPrefix(path, "lora-training/"))
	case strings.HasPrefix(path, "content/media/"):
		a.handleAdminContentMedia(w, r, strings.TrimPrefix(path, "content/media/"))
	case path == "content/events":
		a.handleAdminContentEvents(w, r)
	case path == "content":
		a.handleAdminContent(w, r)
	case strings.HasPrefix(path, "services/"):
		a.handleAdminService(w, r, strings.TrimPrefix(path, "services/"))
	case path == "audit/export":
		a.handleAdminAuditExport(w, r)
	case path == "audit":
		a.handleAdminAudit(w, r)
	case strings.HasPrefix(path, "suggestions/"):
		a.handleAdminSuggestionAction(w, r, strings.TrimPrefix(path, "suggestions/"))
	case path == "suggestions":
		a.handleAdminSuggestions(w, r)
	default:
		a.notFoundPage(w, r)
	}
}

func (a *App) handleAdminSuggestionAction(w http.ResponseWriter, r *http.Request, rawPath string) {
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "json":
		a.handleAdminSuggestionDownload(w, r, id)
	case "retry":
		a.handleAdminSuggestionRetry(w, r, id)
	case "decision":
		a.handleAdminSuggestionDecision(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) handleAdminSuggestionDownload(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	suggestion, err := a.store.FeatureSuggestionJSONForAdmin(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	payload, err := a.contentCipher.DecryptBytes(suggestion.JSONCipher)
	if err != nil || int64(len(payload)) != suggestion.JSONSizeBytes || !json.Valid(payload) {
		http.Error(w, "не удалось подготовить JSON", http.StatusInternalServerError)
		return
	}
	filename := strings.ReplaceAll(filepath.Base(suggestion.JSONName), `"`, "")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) handleAdminSuggestions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	a.renderAdminSuggestionsPage(w, r, http.StatusOK, "", 0, "", false)
}

func (a *App) renderAdminSuggestionsPage(w http.ResponseWriter, r *http.Request, status int, message string, commentID int64, comment string, uncertain bool) {
	w.Header().Set("Cache-Control", "no-store")
	rows, err := a.store.ListFeatureSuggestions(r.Context(), 200)
	if err != nil {
		status = http.StatusServiceUnavailable
	}
	items := make([]featureSuggestionView, 0, len(rows))
	for _, row := range rows {
		item, decodeErr := a.featureSuggestionView(row)
		if decodeErr != nil {
			statusLabel, statusClass, statusHint := featureSuggestionStatus(row.Status)
			item = featureSuggestionView{FeatureSuggestionRow: row, Description: "[не удалось расшифровать данные]", KindLabel: featureSuggestionKindLabel(row.Kind), StatusLabel: statusLabel, StatusClass: statusClass, StatusHint: statusHint, ScanStatusLabel: featureSuggestionScanStatusLabel(row.ScanStatus)}
		}
		items = append(items, item)
	}
	if message == "" {
		message = adminSuggestionError(r.URL.Query().Get("error"))
	}
	a.renderStatus(w, r, status, "admin_suggestions", map[string]any{
		"Title": "Предложения пользователей", "Suggestions": items,
		"VirusTotalConfigured": a.virusTotal != nil && a.virusTotal.Configured(),
		"PublicIntakeEnabled":  a.cfg.FeatureSuggestionsEnabled,
		"Message":              adminSuggestionMessage(r.URL.Query().Get("message")),
		"Error":                message,
		"CommentID":            commentID,
		"Comment":              comment,
		"SuggestionLoadError":  err != nil,
		"SuggestionUncertain":  uncertain,
	})
}

func (a *App) handleAdminSuggestionRetry(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось прочитать форму", http.StatusBadRequest)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return
	}
	if a.virusTotal == nil || !a.virusTotal.Configured() {
		http.Redirect(w, r, "/admin/suggestions?error=vt_unavailable", http.StatusSeeOther)
		return
	}
	if err := a.store.RetryFeatureSuggestionScans(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrFeatureSuggestionStateConflict) || errors.Is(err, sql.ErrNoRows) {
			http.Redirect(w, r, "/admin/suggestions?error=state", http.StatusSeeOther)
			return
		}
		a.renderAdminSuggestionsPage(w, r, http.StatusServiceUnavailable, "Результат повторной проверки не подтверждён. Обновите очередь перед повторным действием.", 0, "", true)
		return
	}
	admin := a.currentUser(r)
	a.audit(r.Context(), &admin.ID, "feature_suggestion_scan_retried", "feature_suggestion", &id, a.clientIP(r), r.UserAgent(), nil)
	http.Redirect(w, r, "/admin/suggestions?message=retry", http.StatusSeeOther)
}

func (a *App) handleAdminSuggestionDecision(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось прочитать форму", http.StatusBadRequest)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return
	}
	if len(r.PostForm["decision"]) != 1 || len(r.PostForm["comment"]) > 1 {
		http.Error(w, "некорректное решение", http.StatusBadRequest)
		return
	}
	decision := strings.TrimSpace(r.PostForm.Get("decision"))
	comment := strings.TrimSpace(r.PostForm.Get("comment"))
	if decision != "accepted" && decision != "rejected" {
		http.Error(w, "неизвестное решение", http.StatusBadRequest)
		return
	}
	if len([]rune(comment)) > 1000 || (decision == "rejected" && len([]rune(comment)) < 3) {
		a.renderAdminSuggestionsPage(w, r, http.StatusBadRequest, adminSuggestionError("comment"), id, comment, false)
		return
	}
	commentCipher, err := a.contentCipher.Encrypt(comment)
	if err != nil {
		http.Error(w, "не удалось защитить комментарий", http.StatusInternalServerError)
		return
	}
	admin := a.currentUser(r)
	if err := a.store.SetFeatureSuggestionDecision(r.Context(), id, admin.ID, admin.Username, decision, commentCipher); err != nil {
		switch {
		case errors.Is(err, store.ErrFeatureSuggestionUnsafeDecision):
			http.Redirect(w, r, "/admin/suggestions?error=unsafe", http.StatusSeeOther)
		case errors.Is(err, store.ErrFeatureSuggestionStateConflict), errors.Is(err, sql.ErrNoRows):
			http.Redirect(w, r, "/admin/suggestions?error=state", http.StatusSeeOther)
		default:
			a.renderAdminSuggestionsPage(w, r, http.StatusServiceUnavailable, "Результат сохранения не подтверждён. Обновите очередь перед повторным действием.", id, comment, true)
		}
		return
	}
	a.audit(r.Context(), &admin.ID, "feature_suggestion_"+decision, "feature_suggestion", &id, a.clientIP(r), r.UserAgent(), map[string]any{"comment_provided": comment != ""})
	http.Redirect(w, r, "/admin/suggestions?message="+decision, http.StatusSeeOther)
}

func adminSuggestionMessage(code string) string {
	switch code {
	case "retry":
		return "Повторная проверка поставлена в очередь."
	case "accepted":
		return "Предложение принято в работу. Ничего не устанавливалось автоматически."
	case "rejected":
		return "Предложение отклонено, комментарий доступен пользователю."
	default:
		return ""
	}
}

func adminSuggestionError(code string) string {
	switch code {
	case "vt_unavailable":
		return "VirusTotal не настроен, повторную проверку пока нельзя запустить."
	case "unsafe":
		return "Нельзя принять предложение: не все вложения прошли проверку без замечаний."
	case "comment":
		return "Для отклонения укажите понятную причину до 1000 символов."
	case "state":
		return "Состояние предложения уже изменилось. Обновите очередь и повторите действие."
	default:
		return ""
	}
}

func (a *App) handleAdminSystemOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	probeCtx, probeCancel := context.WithTimeout(r.Context(), 4*time.Second)
	a.refreshDependencyStatuses(probeCtx)
	probeCancel()
	overview, err := a.systemOverview(r.Context())
	if err != nil {
		http.Error(w, "ошибка системной статистики", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(overview)
}

func (a *App) handleAdminUpdates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	status := updates.Status{}
	var commandErr error
	action, validation := "", ""
	selected := []string{}
	httpStatus := http.StatusOK
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if !a.validCSRF(r) {
			http.Error(w, "доступ запрещён", http.StatusForbidden)
			return
		}
		var request updates.Request
		action, request = updateRequestFromForm(r)
		if validUpdateRequest(request) {
			selected = request.Components
		}
		if action != "check" && action != "install" {
			validation, httpStatus = "Неизвестное действие. Обновите страницу и выберите команду.", http.StatusBadRequest
		} else if !validUpdateRequest(request) {
			validation, httpStatus = "Выберите хотя бы один компонент для проверки или обновления.", http.StatusBadRequest
		} else if action == "install" && r.FormValue("confirm_install") != "1" {
			validation, httpStatus = "Подтвердите установку и перезапуск компонентов.", http.StatusBadRequest
		} else if action == "install" {
			status, commandErr = a.updates.Install(r.Context(), request)
			a.audit(r.Context(), &actor.ID, "updates_install_requested", "system", nil, a.clientIP(r), r.UserAgent(), map[string]any{"components": request.Components, "response_confirmed": commandErr == nil})
			status.Message = a.appendComfyUpdateCompatibilitySummary(r.Context(), status.Message, request, commandErr)
		} else {
			status, commandErr = a.updates.Check(r.Context(), request)
		}
		if validation != "" {
			action = ""
			status, commandErr = a.updates.Status(r.Context())
		}
	} else {
		status, commandErr = a.updates.Status(r.Context())
	}
	a.renderStatus(w, r, httpStatus, "admin_updates", map[string]any{
		"Title": "Обновления", "UpdateWorkspace": newAdminUpdatesView(time.Now().UTC(), status, selected, action, commandErr), "Validation": validation,
		"RuntimeBuild": buildinfo.Current(),
	})
}

func (a *App) appendComfyUpdateCompatibilitySummary(ctx context.Context, message string, request updates.Request, installErr error) string {
	message = strings.TrimSpace(message)
	if installErr != nil || !hasString(request.Components, updates.ComponentComfyUI) {
		return message
	}
	report := a.currentWorkflowCompatibility(ctx, true)
	return strings.TrimSpace(message + " " + report.updateSummary())
}

// updateRequestFromForm keeps a direct card action isolated from the batch checkboxes.
func updateRequestFromForm(r *http.Request) (string, updates.Request) {
	action := r.FormValue("action")
	if target, directInstall := strings.CutPrefix(action, "install:"); directInstall {
		return "install", updates.Request{Components: []string{target}}
	}

	selected := r.Form["components"]
	if len(selected) == 0 {
		if component := r.FormValue("component"); component != "" {
			selected = []string{component}
		}
	}
	return action, updates.Request{Components: selected}
}

func validUpdateRequest(request updates.Request) bool {
	if len(request.Components) == 0 || len(request.Components) > 3 {
		return false
	}
	seen := make(map[string]struct{}, len(request.Components))
	for _, component := range request.Components {
		if !updates.ValidComponent(component) || component == updates.ComponentOpenWebUI {
			return false
		}
		if _, found := seen[component]; found {
			return false
		}
		seen[component] = struct{}{}
	}
	return true
}

func (a *App) handleAdminContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	username := strings.TrimSpace(r.URL.Query().Get("user"))
	if len(username) > 80 {
		username = username[:80]
	}
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	if service != "comfyui" && service != "openwebui" && service != "ollama" {
		service = ""
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		query = query[:200]
	}
	liveRefresh := r.URL.Query().Get("live") == "1"
	if !liveRefresh {
		a.classifyPendingSensitiveContent(r.Context())
		a.queueSensitiveMediaClassification()
		a.backfillComfyContentMedia(r.Context())
	}
	contentRevision, err := a.store.ContentRevision(r.Context())
	if err != nil {
		http.Error(w, "ошибка ревизии контента", http.StatusInternalServerError)
		return
	}
	rowLimit := 200
	if query != "" {
		rowLimit = 500
	}
	rows, err := a.store.ListContentEvents(r.Context(), rowLimit, username, "")
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	jobs := []domain.GenerationJob{}
	if service == "" || service == "comfyui" {
		jobs, err = a.store.ListAdminGenerationJobs(r.Context(), rowLimit, username, time.Now().Add(-a.retentionPolicy().AIContent))
		if err != nil {
			http.Error(w, "ошибка загрузки заданий", http.StatusInternalServerError)
			return
		}
	}
	eventIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		eventIDs = append(eventIDs, row.ID)
	}
	mediaByEvent, err := a.store.ListContentMediaSummaries(r.Context(), eventIDs)
	if err != nil {
		http.Error(w, "ошибка загрузки медиа", http.StatusInternalServerError)
		return
	}
	retentionStats, err := a.store.ContentRetentionStats(r.Context())
	if err != nil {
		http.Error(w, "ошибка отчёта хранения", http.StatusInternalServerError)
		return
	}
	events, overview := a.buildContentTaskViews(rows, jobs, mediaByEvent, service, query, 200)
	jobIDs := make([]int64, 0, len(events))
	for _, event := range events {
		if event.GenerationJobID != nil {
			jobIDs = append(jobIDs, *event.GenerationJobID)
		}
	}
	transitions, err := a.store.GenerationJobTransitionsForAdmin(r.Context(), jobIDs)
	if err != nil {
		http.Error(w, "ошибка загрузки этапов заданий", http.StatusInternalServerError)
		return
	}
	for index := range events {
		if events[index].GenerationJobID == nil {
			continue
		}
		events[index].Stages = contentStageViews(transitions[*events[index].GenerationJobID])
	}
	a.render(w, r, "admin_content", map[string]any{
		"Title": "AI-контент пользователей", "Events": events,
		"Username": username, "Service": service, "Query": query, "Overview": overview,
		"ContentRevision": contentRevision, "RetentionStats": retentionStats,
	})
}

func (a *App) handleAdminContentEvents(w http.ResponseWriter, r *http.Request) {
	a.serveLiveEvents(w, r, "content", writeContentRevisionEvent)
}

func writeContentRevisionEvent(w io.Writer, event string, revision int64) error {
	_, err := fmt.Fprintf(w, "event: %s\ndata: %d\n\n", event, revision)
	return err
}

func prettyContentMetadata(metadata string) string {
	var payload any
	if json.Unmarshal([]byte(metadata), &payload) != nil {
		return metadata
	}
	formatted, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return metadata
	}
	return string(formatted)
}

func contentAssistantFromMetadata(metadata string) *ContentAssistantView {
	var payload struct {
		PromptAssistant *struct {
			Requested      bool   `json:"requested"`
			Applied        bool   `json:"applied"`
			Decision       string `json:"decision"`
			Template       string `json:"template"`
			Think          bool   `json:"think"`
			OriginalPrompt string `json:"original_prompt"`
			Suggestion     string `json:"suggestion"`
		} `json:"prompt_assistant"`
	}
	if err := json.Unmarshal([]byte(metadata), &payload); err != nil || payload.PromptAssistant == nil || !payload.PromptAssistant.Requested {
		return nil
	}
	return &ContentAssistantView{
		Applied: payload.PromptAssistant.Applied, Decision: payload.PromptAssistant.Decision, Template: payload.PromptAssistant.Template, Think: payload.PromptAssistant.Think,
		OriginalPrompt: payload.PromptAssistant.OriginalPrompt, Suggestion: payload.PromptAssistant.Suggestion,
	}
}

func (a *App) backfillComfyContentMedia(ctx context.Context) (int64, error) {
	items, err := a.store.UnarchivedComfyOutputs(ctx, 12)
	if err != nil {
		return 0, err
	}
	var archived int64
	var archiveErrors []error
	for _, item := range items {
		if err := a.archiveGenerationOutputs(ctx, item.UserID, []generationOutput{{
			Filename: item.Filename, Subfolder: item.Subfolder, Type: item.StorageType, MediaType: item.MediaType,
		}}); err != nil {
			archiveErrors = append(archiveErrors, fmt.Errorf("archive %s: %w", item.Filename, err))
			continue
		}
		archived++
	}
	return archived, errors.Join(archiveErrors...)
}

func (a *App) handleAdminContentMedia(w http.ResponseWriter, r *http.Request, rawID string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.Trim(rawID, "/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	release := a.acquireAdminMediaSlot(r.Context())
	if release == nil {
		return
	}
	defer release()
	media, err := a.store.ContentMediaByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if wantsAdminMediaViewer(r) {
		a.render(w, r, "admin_media_viewer", map[string]any{
			"Title": "Просмотр результата", "MediaID": media.ID, "MediaType": media.MediaType,
			"Filename": media.OriginalName,
		})
		return
	}
	contentType, inline := safeAdminMediaType(media.MediaType, media.MIMEType)
	disposition := "attachment"
	if inline && r.URL.Query().Get("download") != "1" {
		disposition = "inline"
	}
	filename := filepath.Base(strings.ReplaceAll(media.OriginalName, "\\", "/"))
	if filename == "." || filename == "/" || filename == "" {
		filename = "media"
	}
	payload, err := a.materializeContentMedia(r.Context(), media)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errMediaMemoryBudget) {
			status = http.StatusTooManyRequests
		}
		http.Error(w, "медиа временно недоступно", status)
		return
	}
	defer payload.Close()
	dispositionHeader := mime.FormatMediaType(disposition, map[string]string{"filename": filename})
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", dispositionHeader)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	http.ServeContent(w, r, filename, time.Time{}, payload)
}

// acquireAdminMediaSlot queues thumbnail requests instead of rejecting the
// browser's normal parallel image loading with a random 429 response.
func (a *App) acquireAdminMediaSlot(ctx context.Context) func() {
	if a.adminMediaSlots == nil {
		return func() {}
	}
	select {
	case a.adminMediaSlots <- struct{}{}:
		return func() { <-a.adminMediaSlots }
	case <-ctx.Done():
		return nil
	}
}

func wantsAdminMediaViewer(r *http.Request) bool {
	if r.URL.Query().Get("raw") == "1" || r.URL.Query().Get("download") == "1" {
		return false
	}
	return r.Header.Get("Sec-Fetch-Dest") == "document" || strings.Contains(r.Header.Get("Accept"), "text/html")
}

func safeAdminMediaType(mediaType, rawMIME string) (string, bool) {
	parsed, _, err := mime.ParseMediaType(rawMIME)
	if err != nil {
		return "application/octet-stream", false
	}
	parsed = strings.ToLower(parsed)
	allowedImages := map[string]struct{}{
		"image/png": {}, "image/jpeg": {}, "image/gif": {}, "image/webp": {}, "image/avif": {},
	}
	allowedVideos := map[string]struct{}{
		"video/mp4": {}, "video/webm": {}, "video/ogg": {}, "video/quicktime": {},
	}
	if mediaType == "image" {
		_, ok := allowedImages[parsed]
		return chooseMediaType(parsed, ok)
	}
	if mediaType == "video" {
		_, ok := allowedVideos[parsed]
		return chooseMediaType(parsed, ok)
	}
	return "application/octet-stream", false
}

func chooseMediaType(contentType string, allowed bool) (string, bool) {
	if !allowed {
		return "application/octet-stream", false
	}
	return contentType, true
}

func (a *App) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	filter, err := ParseAccessListFilter(r.URL.Query(), false)
	if err != nil {
		http.Error(w, "Проверьте фильтры списка", http.StatusBadRequest)
		return
	}
	page, err := a.store.ListUsersPage(r.Context(), filter)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "admin_users", map[string]any{
		"Title": "Пользователи", "AccessList": BuildUserAccessList(page),
		"DeleteStatus": r.URL.Query().Get("deleted"),
	})
}

func (a *App) handleAdminUserDetail(w http.ResponseWriter, r *http.Request, rest string) {
	w.Header().Set("Cache-Control", "private, no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		http.Error(w, "Доступ запрещён", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 1 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	query, err := ParseAdminUserProfileQuery(r.URL.Query())
	if err != nil {
		http.Error(w, "Проверьте вкладку и фильтры профиля.", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if !a.validCSRF(r) {
			http.Error(w, "неверный защитный токен", http.StatusForbidden)
			return
		}
		target, err := a.store.UserByID(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "Не удалось загрузить пользователя.", http.StatusInternalServerError)
			return
		}
		if target.Role == "admin" {
			switch r.Form.Get("action") {
			case "disable", "enable", "delete", "update_access", "review_access", "update_generation_policy", "update_assistant_model":
				http.Error(w, "Права администратора нельзя изменить в этой форме.", http.StatusForbidden)
				return
			}
		}
		switch r.Form.Get("action") {
		case "update_generation_resources":
			if target.Role != "admin" {
				http.Error(w, "Эта настройка предназначена для администратора.", http.StatusBadRequest)
				return
			}
			for _, key := range []string{"queue_priority", "pause_mining_for_quick_generation"} {
				values := r.PostForm[key]
				if len(values) > 1 || len(values) == 1 && values[0] != "on" {
					http.Error(w, "Проверьте настройки ресурсов.", http.StatusBadRequest)
					return
				}
			}
			priority := r.PostForm.Get("queue_priority") == "on"
			pauseMining := r.PostForm.Get("pause_mining_for_quick_generation") == "on"
			updated, err := a.store.SetAdminGenerationResources(r.Context(), id, priority, pauseMining)
			if err != nil {
				http.Error(w, "Не удалось сохранить настройки ресурсов. Обновите данные перед повторной попыткой.", http.StatusServiceUnavailable)
				return
			}
			if !updated {
				http.Error(w, "Учётная запись изменилась. Обновите страницу.", http.StatusConflict)
				return
			}
			a.audit(r.Context(), &actor.ID, "admin_generation_resources_updated", "user", &id, a.clientIP(r), r.UserAgent(), map[string]any{"queue_priority": priority, "pause_mining_for_quick_generation": pauseMining})
			http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?tab=access&access=changed", id), http.StatusSeeOther)
			return
		case "update_assistant_model":
			a.handleAdminAssistantModelGrant(w, r, actor.ID, id)
			return
		case "disable":
			updated, revoked, err := a.store.SetDisabled(r.Context(), id, true)
			if err != nil {
				http.Error(w, "не удалось отключить пользователя", http.StatusInternalServerError)
				return
			}
			if updated {
				a.closeUserWebSockets(id)
				a.audit(r.Context(), &actor.ID, "user_disabled", "user", &id, a.clientIP(r), r.UserAgent(), map[string]any{"sessions_revoked": revoked})
			}
		case "enable":
			updated, _, err := a.store.SetDisabled(r.Context(), id, false)
			if err != nil {
				http.Error(w, "не удалось включить пользователя", http.StatusInternalServerError)
				return
			}
			if updated {
				a.audit(r.Context(), &actor.ID, "user_enabled", "user", &id, a.clientIP(r), r.UserAgent(), nil)
			}
		case "delete":
			confirmation := strings.TrimSpace(r.Form.Get("confirm_username"))
			if confirmation == "" {
				http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?delete=confirmation_required", id), http.StatusFound)
				return
			}
			deleted, err := a.store.DeleteUser(r.Context(), id, confirmation)
			if err != nil {
				http.Error(w, "не удалось удалить пользователя", http.StatusInternalServerError)
				return
			}
			if !deleted {
				http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?delete=confirmation_invalid", id), http.StatusFound)
				return
			}
			a.closeUserWebSockets(id)
			a.audit(r.Context(), &actor.ID, "user_deleted", "user", &id, a.clientIP(r), r.UserAgent(), map[string]any{"username": confirmation})
			http.Redirect(w, r, "/admin/users?deleted=1", http.StatusFound)
			return
		case "revoke_sessions":
			revoked, err := a.store.RevokeSessions(r.Context(), id)
			if err != nil {
				http.Error(w, "Не удалось завершить сессии. Попробуйте снова.", http.StatusInternalServerError)
				return
			}
			a.closeUserWebSockets(id)
			a.audit(r.Context(), &actor.ID, "sessions_revoked", "user", &id, a.clientIP(r), r.UserAgent(), map[string]any{"count": revoked})
			redirectAdminProfile(w, r, id, "sessions", "security", "sessions_revoked")
			return
		case "revoke_session":
			sessionID, err := strconv.ParseInt(r.Form.Get("session_id"), 10, 64)
			if err != nil || sessionID <= 0 {
				http.Error(w, "Неверная сессия", http.StatusBadRequest)
				return
			}
			hash, revoked, err := a.store.RevokeOwnedSessionWithHash(r.Context(), sessionID, id, adminProfileSessionHash(r))
			if err != nil {
				http.Error(w, "Не удалось завершить сессию. Попробуйте снова.", http.StatusInternalServerError)
				return
			}
			status := "session_unavailable"
			if revoked {
				a.closeSessionWebSockets(hash)
				a.audit(r.Context(), &actor.ID, "admin_user_session_revoked", "user", &id, a.clientIP(r), r.UserAgent(), map[string]any{"session_id": sessionID})
				status = "session_revoked"
			}
			redirectAdminProfile(w, r, id, "sessions", "security", status)
			return
		case "unlock":
			unlocked, err := a.store.UnlockUser(r.Context(), id)
			if err != nil {
				http.Error(w, "не удалось снять блокировку", http.StatusInternalServerError)
				return
			}
			if unlocked {
				a.audit(r.Context(), &actor.ID, "user_unlocked", "user", &id, a.clientIP(r), r.UserAgent(), nil)
			}
			http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?security=unlocked", id), http.StatusFound)
			return
		case "review_access", "update_access":
			settings, accessErr := parseAccessSettings(r, false)
			if accessErr != nil {
				query.Tab = "access"
				a.renderAdminUserProfile(w, r, id, query, map[string]any{"AccessError": accessErr.Error(), "AccessForm": r.PostForm})
				return
			}
			if r.Form.Get("action") == "review_access" {
				query.Tab = "access"
				a.renderAdminUserProfile(w, r, id, query, map[string]any{"AccessReviewed": true, "AccessReview": settings, "AccessForm": r.PostForm})
				return
			}
			params := store.SetServiceAccessParams{ManageMining: r.Form.Get("can_manage_mining") == "on"}
			params.ApplyAccess(settings)
			updated, err := a.store.SetServiceAccess(r.Context(), id, params)
			if err != nil {
				http.Error(w, "не удалось обновить права доступа", http.StatusInternalServerError)
				return
			}
			if updated {
				a.closeUserWebSockets(id)
				details := accessAuditDetails(settings)
				details["manage_mining"] = params.ManageMining
				a.audit(r.Context(), &actor.ID, "user_service_access_updated", "user", &id, a.clientIP(r), r.UserAgent(), details)
			}
			http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?access=changed", id), http.StatusFound)
			return
		case "update_generation_policy":
			if target, err := a.store.UserByID(r.Context(), id); err != nil || target.Role == "admin" {
				http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?policy=not_available", id), http.StatusFound)
				return
			}
			policy := domain.GenerationAccessPolicy{UserID: id, PresetIDs: r.Form["preset_id"], ModelIDs: r.Form["model_id"], KreaLoraGroups: r.Form["krea_lora_group"], FluxLoraGroups: r.Form["flux_lora_group"]}
			if err := a.store.SaveGenerationAccessPolicy(r.Context(), policy); err != nil {
				http.Error(w, "не удалось сохранить правила быстрой генерации", http.StatusInternalServerError)
				return
			}
			a.audit(r.Context(), &actor.ID, "user_generation_policy_updated", "user", &id, a.clientIP(r), r.UserAgent(), map[string]any{"presets": policy.PresetIDs, "models": policy.ModelIDs, "krea_lora_groups": policy.KreaLoraGroups, "flux_lora_groups": policy.FluxLoraGroups})
			http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?policy=changed", id), http.StatusFound)
			return
		case "change_password":
			newPassword := r.Form.Get("new_password")
			confirmPassword := r.Form.Get("confirm_password")
			if validateNewPassword(newPassword) != "" || newPassword != confirmPassword {
				http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?password=invalid", id), http.StatusFound)
				return
			}
			if err := a.updateUserPassword(r.Context(), id, newPassword); err != nil {
				http.Error(w, "не удалось изменить пароль", http.StatusInternalServerError)
				return
			}
			if id == actor.ID {
				a.revokeOtherSessions(id, r)
			} else {
				_, _ = a.store.RevokeSessions(r.Context(), id)
				a.closeUserWebSockets(id)
			}
			a.audit(r.Context(), &actor.ID, "admin_user_password_changed", "user", &id, a.clientIP(r), r.UserAgent(), nil)
			http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?password=changed", id), http.StatusFound)
			return
		default:
			http.Error(w, "Неверное действие", http.StatusBadRequest)
			return
		}
		redirectAdminProfile(w, r, id, "sessions", "security", "status_changed")
		return
	}
	a.renderAdminUserProfile(w, r, id, query, nil)
}

func (a *App) handleAdminInvites(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if _, err := ParseAccessListFilter(r.URL.Query(), true); err != nil {
		http.Error(w, "Проверьте фильтры списка", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if !a.validCSRF(r) {
			http.Error(w, "неверный защитный токен", http.StatusForbidden)
			return
		}
		actor := a.currentUser(r)
		maxUses, _ := strconv.Atoi(r.Form.Get("max_uses"))
		if maxUses <= 0 {
			maxUses = 1
		}
		if maxUses > 10000 {
			maxUses = 10000
		}
		expiresAt, expiryErr := parseInviteExpiry(r)
		if expiryErr != nil {
			a.renderAdminInvites(w, r, map[string]any{"Title": "Приглашения", "Error": expiryErr.Error()})
			return
		}
		accountLifetimeSeconds, lifetimeErr := parseInviteAccountLifetime(r)
		if lifetimeErr != nil {
			a.renderAdminInvites(w, r, map[string]any{"Title": "Приглашения", "Error": lifetimeErr.Error()})
			return
		}
		settings, accessErr := parseAccessSettings(r, true)
		if accessErr != nil {
			a.renderAdminInvites(w, r, map[string]any{"Title": "Приглашения", "Error": accessErr.Error()})
			return
		}
		if r.Form.Get("limit_form_version") == "2" {
			review, reviewErr := InvitationSettingsFromForm(r.PostForm)
			if reviewErr != nil {
				a.renderAdminInvites(w, r, map[string]any{"Title": "Приглашения", "Error": "Проверьте сроки ссылки, аккаунта и число активаций."})
				return
			}
			digest := inviteReviewDigest(review)
			if r.Form.Get("action") != "create" || r.Form.Get("invite_review") != digest {
				a.renderAdminInvites(w, r, map[string]any{"Title": "Приглашения", "InviteReviewed": true, "InviteReview": review, "ReviewDigest": digest})
				return
			}
		}
		token, err := security.RandomToken()
		if err != nil {
			http.Error(w, "не удалось создать защищённый токен", http.StatusInternalServerError)
			return
		}
		params := store.CreateInviteParams{
			TokenHash:              security.HashToken(token),
			CreatedByUserID:        actor.ID,
			MaxUses:                maxUses,
			ExpiresAt:              expiresAt,
			AccountLifetimeSeconds: accountLifetimeSeconds,
		}
		params.ApplyAccess(settings)
		id, err := a.store.CreateInvite(r.Context(), params)
		if err != nil {
			http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
			return
		}
		details := accessAuditDetails(settings)
		details["max_uses"], details["expires_at"], details["account_lifetime_seconds"] = maxUses, expiresAt, accountLifetimeSeconds
		details["grant_comfyui"], details["grant_openwebui"], details["grant_quick_generation"] = settings.ComfyUI, settings.OpenWebUI, settings.QuickGeneration
		a.audit(r.Context(), &actor.ID, "invite_created", "invite", &id, a.clientIP(r), r.UserAgent(), details)
		a.renderAdminInvites(w, r, map[string]any{
			"Title":      "Приглашения",
			"InviteLink": strings.TrimRight(a.cfg.PublicBaseURL, "/") + "/invite/" + token,
		})
		return
	}
	a.renderAdminInvites(w, r, map[string]any{"Title": "Приглашения"})
}

func parseQueuePriority(r *http.Request, allowed, legacyPriority bool) bool {
	if !allowed {
		return false
	}
	// Forms opened before the split still submit the former combined flag.
	if r.Form.Get("queue_policy_version") != "2" {
		return legacyPriority
	}
	return r.Form.Get("queue_priority") == "on"
}

func parseGenerationLimits(r *http.Request, prefix, label string) (int, int64, error) {
	rawDaily := strings.TrimSpace(r.Form.Get(prefix + "_daily_limit"))
	if r.Form.Get("limit_form_version") == "2" {
		if r.Form.Get(prefix+"_daily_unlimited") == "on" {
			rawDaily = "0"
		} else if value, err := strconv.Atoi(rawDaily); err != nil || value <= 0 {
			return 0, 0, fmt.Errorf("укажите суточный лимит %s или выберите «Без ограничения»", label)
		}
	}
	if rawDaily == "" {
		rawDaily = "0"
	}
	dailyLimit, err := strconv.Atoi(rawDaily)
	if err != nil || dailyLimit < 0 || dailyLimit > 100000 {
		return 0, 0, fmt.Errorf("суточный лимит %s должен быть числом от 0 до 100000", label)
	}
	rawTotal := strings.TrimSpace(r.Form.Get(prefix + "_total_limit"))
	if r.Form.Get("limit_form_version") == "2" {
		if r.Form.Get(prefix+"_total_unlimited") == "on" {
			rawTotal = "0"
		} else if value, err := strconv.ParseInt(rawTotal, 10, 64); err != nil || value <= 0 {
			return 0, 0, fmt.Errorf("укажите общий лимит %s или выберите «Без ограничения»", label)
		}
	}
	if rawTotal == "" {
		rawTotal = "0"
	}
	totalLimit, err := strconv.ParseInt(rawTotal, 10, 64)
	if err != nil || totalLimit < 0 || totalLimit > 10000000 {
		return 0, 0, fmt.Errorf("общий лимит %s должен быть числом от 0 до 10000000", label)
	}
	return dailyLimit, totalLimit, nil
}

func parseMaxVideoGenerationQuality(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.Form.Get("max_video_generation_quality"))
	if raw == "" {
		raw = "720"
	}
	quality, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("выберите максимальное качество видео")
	}
	switch quality {
	case 480, 720, 1080, 1440:
		return quality, nil
	default:
		return 0, fmt.Errorf("максимальное качество видео должно быть 480p, 720p, 1080p или 1440p")
	}
}

func parseInviteExpiry(r *http.Request) (time.Time, error) {
	if rawHours := strings.TrimSpace(r.Form.Get("invite_ttl_hours")); rawHours != "" {
		hours, err := strconv.Atoi(rawHours)
		if err != nil || hours < 1 || hours > 24*90 {
			return time.Time{}, fmt.Errorf("срок действия ссылки должен быть от 1 часа до 90 дней")
		}
		return time.Now().Add(time.Duration(hours) * time.Hour).UTC(), nil
	}

	// Compatibility with the previous datetime-based form.
	expiresAt := time.Now().Add(24 * time.Hour)
	if raw := strings.TrimSpace(r.Form.Get("expires_at")); raw != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", raw, time.Local)
		if err != nil {
			return time.Time{}, fmt.Errorf("укажите корректный срок действия ссылки")
		}
		expiresAt = t.UTC()
	}
	if expiresAt.Before(time.Now().Add(5 * time.Minute)) {
		return time.Time{}, fmt.Errorf("ссылка должна действовать ещё минимум 5 минут")
	}
	if expiresAt.After(time.Now().Add(90 * 24 * time.Hour)) {
		return time.Time{}, fmt.Errorf("срок действия ссылки не может быть больше 90 дней")
	}
	return expiresAt, nil
}

func parseInviteAccountLifetime(r *http.Request) (int64, error) {
	accountType := strings.TrimSpace(r.Form.Get("account_type"))
	if accountType == "" || accountType == "permanent" {
		return 0, nil
	}
	if accountType != "temporary" {
		return 0, fmt.Errorf("выберите тип учётной записи")
	}
	if rawHours := strings.TrimSpace(r.Form.Get("temporary_account_hours")); rawHours != "" {
		hours, err := strconv.Atoi(rawHours)
		if err != nil || hours < 1 || hours > 24*365 {
			return 0, fmt.Errorf("срок временной учётной записи должен быть от 1 часа до 365 дней")
		}
		return int64(hours) * int64(time.Hour.Seconds()), nil
	}
	// Keep invitation forms submitted before the hour selector was introduced valid.
	days, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("temporary_account_days")))
	if err != nil || days < 1 || days > 365 {
		return 0, fmt.Errorf("срок временной учётной записи должен быть от 1 часа до 365 дней")
	}
	return int64(days) * int64((24 * time.Hour).Seconds()), nil
}

func (a *App) handleAdminInviteAction(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}
	filter, err := ParseAccessListFilter(r.URL.Query(), true)
	if err != nil {
		http.Error(w, "Проверьте фильтры списка", http.StatusBadRequest)
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	actor := a.currentUser(r)
	switch parts[1] {
	case "delete":
		deleted, err := a.store.DeleteInvite(r.Context(), id)
		if err != nil {
			http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
			return
		}
		if !deleted {
			http.NotFound(w, r)
			return
		}
		a.audit(r.Context(), &actor.ID, "invite_deleted", "invite", &id, a.clientIP(r), r.UserAgent(), nil)
	case "revoke":
		if _, err := a.store.SetInviteRevoked(r.Context(), id, true); err != nil {
			http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
			return
		}
		a.audit(r.Context(), &actor.ID, "invite_revoked", "invite", &id, a.clientIP(r), r.UserAgent(), nil)
	case "unrevoke":
		if _, err := a.store.SetInviteRevoked(r.Context(), id, false); err != nil {
			http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
			return
		}
		a.audit(r.Context(), &actor.ID, "invite_unrevoked", "invite", &id, a.clientIP(r), r.UserAgent(), nil)
	default:
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, accessListURL(filter, true, filter.Offset)+"#access-results", http.StatusFound)
}

func (a *App) handleAdminService(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	service := strings.Trim(strings.ToLower(rest), "/")
	if service != "comfyui" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	view, err := a.loadAdminResource(ctx, service, false)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "admin_service", map[string]any{
		"Title": serviceDisplayName(service), "Resource": view,
	})
}
