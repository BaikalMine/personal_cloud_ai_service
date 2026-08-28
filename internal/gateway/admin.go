package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

const maxConcurrentAdminMediaResponses = 2

func (a *App) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" {
		http.NotFound(w, r)
		return
	}
	stats, err := a.store.AdminStats(r.Context())
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	system, err := a.systemOverview(r.Context())
	if err != nil {
		http.Error(w, "ошибка системной статистики", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "admin_dashboard", map[string]any{
		"Title":    "Администрирование",
		"Stats":    stats,
		"System":   system,
		"Services": a.serviceStatuses(r.Context()),
	})
}

func (a *App) handleAdminRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/")
	switch {
	case path == "users":
		a.handleAdminUsers(w, r)
	case strings.HasPrefix(path, "users/"):
		a.handleAdminUserDetail(w, r, strings.TrimPrefix(path, "users/"))
	case path == "invites":
		a.handleAdminInvites(w, r)
	case strings.HasPrefix(path, "invites/"):
		a.handleAdminInviteAction(w, r, strings.TrimPrefix(path, "invites/"))
	case path == "sessions":
		a.handleAdminSessions(w, r)
	case strings.HasPrefix(path, "sessions/"):
		a.handleAdminSessionAction(w, r, strings.TrimPrefix(path, "sessions/"))
	case path == "metrics":
		a.handleAdminMetricsPage(w, r)
	case path == "system/overview":
		a.handleAdminSystemOverview(w, r)
	case path == "mining":
		a.handleAdminMining(w, r)
	case path == "updates":
		a.handleAdminUpdates(w, r)
	case strings.HasPrefix(path, "content/media/"):
		a.handleAdminContentMedia(w, r, strings.TrimPrefix(path, "content/media/"))
	case path == "content":
		a.handleAdminContent(w, r)
	case strings.HasPrefix(path, "services/"):
		a.handleAdminService(w, r, strings.TrimPrefix(path, "services/"))
	case path == "audit":
		a.handleAdminAudit(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) handleAdminSystemOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	status := updates.Status{}
	message := ""
	if r.Method == http.MethodPost {
		if !a.validCSRF(r) {
			http.Error(w, "доступ запрещён", http.StatusForbidden)
			return
		}
		action, request := updateRequestFromForm(r)
		if !validUpdateRequest(request) {
			message = "Выберите хотя бы один компонент для проверки или обновления."
		} else if action == "install" {
			status, _ = a.updates.Install(r.Context(), request)
			message = status.Message
			actor := a.currentUser(r)
			a.audit(r.Context(), &actor.ID, "updates_install_requested", "system", nil, a.clientIP(r), r.UserAgent(), map[string]any{"components": request.Components})
			if message == "" {
				message = "Команда обновления принята. Если обновлялся сам Gateway, страница может кратко перезагрузиться."
			}
		} else {
			status, _ = a.updates.Check(r.Context(), request)
			message = status.Message
		}
	} else {
		status, _ = a.updates.Status(r.Context())
		message = status.Message
	}
	overview := UpdateOverview{}
	for _, component := range status.Components {
		switch {
		case component.UpdateAvailable:
			overview.Available++
		case component.Message != "":
			overview.Blocked++
		case component.Configured:
			overview.Current++
		}
	}
	a.render(w, r, "admin_updates", map[string]any{
		"Title": "Обновления", "Updates": status, "Overview": overview, "Message": message,
	})
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
		if !updates.ValidComponent(component) {
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
	a.backfillComfyContentMedia(r.Context())
	rowLimit := 200
	if query != "" {
		rowLimit = 500
	}
	rows, err := a.store.ListContentEvents(r.Context(), rowLimit, username, service)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
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
	events := make([]ContentEventView, 0, min(len(rows), 200))
	overview := ContentOverview{}
	queryLower := strings.ToLower(query)
	for _, row := range rows {
		prompt, promptErr := a.contentCipher.Decrypt(row.PromptCipher)
		response, responseErr := a.contentCipher.Decrypt(row.ResponseCipher)
		metadata, metadataErr := a.contentCipher.Decrypt(row.MetadataCipher)
		if promptErr != nil || responseErr != nil || metadataErr != nil {
			prompt, response, metadata = "[ошибка расшифровки]", "", ""
		}
		if queryLower != "" {
			haystack := strings.ToLower(strings.Join([]string{prompt, response, metadata, row.Model, row.ExternalID}, "\n"))
			if !strings.Contains(haystack, queryLower) {
				continue
			}
		}
		events = append(events, ContentEventView{
			ID: row.ID, UserID: row.UserID, Username: row.Username, Service: row.Service,
			Kind: row.Kind, ExternalID: row.ExternalID, Model: row.Model, Prompt: prompt,
			Response: response, Metadata: metadata, Assistant: contentAssistantFromMetadata(metadata), MediaCount: row.MediaCount,
			Media:     mediaByEvent[row.ID],
			CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		})
		overview.Total++
		switch row.Service {
		case "comfyui":
			overview.ComfyUI++
		case "openwebui":
			overview.OpenWebUI++
		case "ollama":
			overview.Ollama++
		}
		if row.MediaCount > 0 {
			overview.WithMedia++
		}
		if len(events) == 200 {
			break
		}
	}
	a.render(w, r, "admin_content", map[string]any{
		"Title": "AI-контент пользователей", "Events": events,
		"Username": username, "Service": service, "Query": query, "Overview": overview,
	})
}

func contentAssistantFromMetadata(metadata string) *ContentAssistantView {
	var payload struct {
		PromptAssistant *struct {
			Requested      bool   `json:"requested"`
			Applied        bool   `json:"applied"`
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
		Applied: payload.PromptAssistant.Applied, Template: payload.PromptAssistant.Template, Think: payload.PromptAssistant.Think,
		OriginalPrompt: payload.PromptAssistant.OriginalPrompt, Suggestion: payload.PromptAssistant.Suggestion,
	}
}

func (a *App) backfillComfyContentMedia(ctx context.Context) {
	items, err := a.store.UnarchivedComfyOutputs(ctx, 12)
	if err != nil {
		log.Printf("find unarchived ComfyUI outputs: %v", err)
		return
	}
	for _, item := range items {
		a.archiveGenerationOutputs(ctx, item.UserID, []generationOutput{{
			Filename: item.Filename, Subfolder: item.Subfolder, Type: item.StorageType, MediaType: item.MediaType,
		}})
	}
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
	payload, err := a.contentCipher.DecryptBytes(media.PayloadCipher)
	if err != nil {
		http.Error(w, "ошибка расшифровки медиа", http.StatusInternalServerError)
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
	dispositionHeader := mime.FormatMediaType(disposition, map[string]string{"filename": filename})
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", dispositionHeader)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	http.ServeContent(w, r, filename, time.Time{}, bytes.NewReader(payload))
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
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(search) > 80 {
		search = search[:80]
	}
	users, err := a.store.ListUsers(r.Context(), search)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "admin_users", map[string]any{
		"Title": "Пользователи", "Users": users, "Query": search,
		"DeleteStatus": r.URL.Query().Get("deleted"),
	})
}

func (a *App) handleAdminUserDetail(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 1 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if !a.validCSRF(r) {
			http.Error(w, "неверный защитный токен", http.StatusForbidden)
			return
		}
		actor := a.currentUser(r)
		switch r.Form.Get("action") {
		case "disable":
			updated, revoked, err := a.store.SetDisabled(r.Context(), id, true)
			if err != nil {
				http.Error(w, "не удалось отключить пользователя", http.StatusInternalServerError)
				return
			}
			if updated {
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
			a.audit(r.Context(), &actor.ID, "user_deleted", "user", &id, a.clientIP(r), r.UserAgent(), map[string]any{"username": confirmation})
			http.Redirect(w, r, "/admin/users?deleted=1", http.StatusFound)
			return
		case "revoke_sessions":
			_, _ = a.store.RevokeSessions(r.Context(), id)
			a.audit(r.Context(), &actor.ID, "sessions_revoked", "user", &id, a.clientIP(r), r.UserAgent(), nil)
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
		case "update_access":
			comfyUI := r.Form.Get("can_use_comfyui") == "on"
			openWebUI := r.Form.Get("can_use_openwebui") == "on"
			quickGeneration := r.Form.Get("can_use_quick_generation") == "on"
			textToImage := quickGeneration && r.Form.Get("can_generate_text_to_image") == "on"
			imageToImage := quickGeneration && r.Form.Get("can_generate_image_to_image") == "on"
			video := quickGeneration && r.Form.Get("can_generate_video") == "on"
			quickGeneration = quickGeneration && (textToImage || imageToImage || video)
			manageMining := r.Form.Get("can_manage_mining") == "on"
			pauseMiningForQuickGeneration := quickGeneration && r.Form.Get("pause_mining_for_quick_generation") == "on"
			dailyLimit, totalLimit, limitErr := parseGenerationLimits(r)
			if limitErr != nil {
				http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?access=invalid_limits", id), http.StatusFound)
				return
			}
			updated, err := a.store.SetServiceAccess(r.Context(), id, comfyUI, openWebUI, quickGeneration, textToImage, imageToImage, video, manageMining, pauseMiningForQuickGeneration, dailyLimit, totalLimit)
			if err != nil {
				http.Error(w, "не удалось обновить права доступа", http.StatusInternalServerError)
				return
			}
			if updated {
				a.audit(r.Context(), &actor.ID, "user_service_access_updated", "user", &id, a.clientIP(r), r.UserAgent(), map[string]any{"comfyui": comfyUI, "openwebui": openWebUI, "quick_generation": quickGeneration, "text_to_image": textToImage, "image_to_image": imageToImage, "video": video, "manage_mining": manageMining, "pause_mining_for_quick_generation": pauseMiningForQuickGeneration, "generation_daily_limit": dailyLimit, "generation_total_limit": totalLimit})
			}
			http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?access=changed", id), http.StatusFound)
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
			}
			a.audit(r.Context(), &actor.ID, "admin_user_password_changed", "user", &id, a.clientIP(r), r.UserAgent(), nil)
			http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?password=changed", id), http.StatusFound)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/users/%d", id), http.StatusFound)
		return
	}
	user, err := a.store.UserByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	stats, err := a.store.UserStats(r.Context(), id, 30)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	activities, err := a.store.LatestActivity(r.Context(), id, 100)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	activities = prepareUserActivities(activities, 20)
	a.render(w, r, "admin_user_detail", map[string]any{
		"Title":          "Пользователь",
		"Profile":        user,
		"Stats":          stats,
		"Activities":     activities,
		"PasswordStatus": r.URL.Query().Get("password"),
		"AccessStatus":   r.URL.Query().Get("access"),
		"SecurityStatus": r.URL.Query().Get("security"),
		"DeleteStatus":   r.URL.Query().Get("delete"),
		"AccountLocked":  user.IsLocked(time.Now()),
	})
}

func (a *App) handleAdminInvites(w http.ResponseWriter, r *http.Request) {
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
		expiresAt := time.Now().Add(24 * time.Hour)
		if raw := r.Form.Get("expires_at"); raw != "" {
			if t, err := time.ParseInLocation("2006-01-02T15:04", raw, time.Local); err == nil {
				expiresAt = t.UTC()
			}
		}
		if expiresAt.Before(time.Now().Add(5 * time.Minute)) {
			expiresAt = time.Now().Add(5 * time.Minute)
		}
		if expiresAt.After(time.Now().Add(90 * 24 * time.Hour)) {
			expiresAt = time.Now().Add(90 * 24 * time.Hour)
		}
		grantComfyUI := r.Form.Get("grant_comfyui") == "on"
		grantOpenWebUI := r.Form.Get("grant_openwebui") == "on"
		grantQuickGeneration := r.Form.Get("grant_quick_generation") == "on"
		grantTextToImage := grantQuickGeneration && r.Form.Get("grant_text_to_image") == "on"
		grantImageToImage := grantQuickGeneration && r.Form.Get("grant_image_to_image") == "on"
		grantVideo := grantQuickGeneration && r.Form.Get("grant_video") == "on"
		if grantQuickGeneration && !grantTextToImage && !grantImageToImage && !grantVideo {
			invites, _ := a.store.ListInvites(r.Context(), 200)
			a.render(w, r, "admin_invites", map[string]any{"Title": "Приглашения", "Invites": invites, "Error": "Для быстрой генерации выберите хотя бы один сценарий."})
			return
		}
		dailyLimit, totalLimit, limitErr := parseGenerationLimits(r)
		if limitErr != nil {
			invites, _ := a.store.ListInvites(r.Context(), 200)
			a.render(w, r, "admin_invites", map[string]any{"Title": "Приглашения", "Invites": invites, "Error": limitErr.Error()})
			return
		}
		if !grantComfyUI && !grantOpenWebUI && !grantQuickGeneration {
			invites, _ := a.store.ListInvites(r.Context(), 200)
			a.render(w, r, "admin_invites", map[string]any{"Title": "Приглашения", "Invites": invites, "Error": "Выберите хотя бы один тип доступа."})
			return
		}
		token, err := security.RandomToken()
		if err != nil {
			http.Error(w, "не удалось создать защищённый токен", http.StatusInternalServerError)
			return
		}
		id, err := a.store.CreateInvite(r.Context(), store.CreateInviteParams{
			TokenHash:            security.HashToken(token),
			CreatedByUserID:      actor.ID,
			MaxUses:              maxUses,
			ExpiresAt:            expiresAt,
			GrantComfyUI:         grantComfyUI,
			GrantOpenWebUI:       grantOpenWebUI,
			GrantQuickGeneration: grantQuickGeneration,
			GrantTextToImage:     grantTextToImage,
			GrantImageToImage:    grantImageToImage,
			GrantVideo:           grantVideo,
			GenerationDailyLimit: dailyLimit,
			GenerationTotalLimit: totalLimit,
		})
		if err != nil {
			http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
			return
		}
		a.audit(r.Context(), &actor.ID, "invite_created", "invite", &id, a.clientIP(r), r.UserAgent(), map[string]any{"max_uses": maxUses, "expires_at": expiresAt, "grant_comfyui": grantComfyUI, "grant_openwebui": grantOpenWebUI, "grant_quick_generation": grantQuickGeneration, "text_to_image": grantTextToImage, "image_to_image": grantImageToImage, "video": grantVideo, "generation_daily_limit": dailyLimit, "generation_total_limit": totalLimit})
		invites, _ := a.store.ListInvites(r.Context(), 200)
		a.render(w, r, "admin_invites", map[string]any{
			"Title":      "Приглашения",
			"Invites":    invites,
			"InviteLink": strings.TrimRight(a.cfg.PublicBaseURL, "/") + "/invite/" + token,
		})
		return
	}
	invites, err := a.store.ListInvites(r.Context(), 200)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "admin_invites", map[string]any{"Title": "Приглашения", "Invites": invites})
}

func parseGenerationLimits(r *http.Request) (int, int64, error) {
	dailyLimit, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("generation_daily_limit")))
	if err != nil || dailyLimit < 0 || dailyLimit > 100000 {
		return 0, 0, fmt.Errorf("суточный лимит должен быть числом от 0 до 100000")
	}
	totalLimit, err := strconv.ParseInt(strings.TrimSpace(r.Form.Get("generation_total_limit")), 10, 64)
	if err != nil || totalLimit < 0 || totalLimit > 10000000 {
		return 0, 0, fmt.Errorf("общий лимит должен быть числом от 0 до 10000000")
	}
	return dailyLimit, totalLimit, nil
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
	http.Redirect(w, r, "/admin/invites", http.StatusFound)
}

func (a *App) handleAdminSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := a.store.ListAdminSessions(r.Context(), 200, a.cfg.SessionIdleTimeout)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "admin_sessions", map[string]any{"Title": "Активные сессии", "Sessions": sessions})
}

func (a *App) handleAdminSessionAction(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodPost || !a.validCSRF(r) {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "revoke" {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	actor := a.currentUser(r)
	revoked, err := a.store.RevokeSession(r.Context(), id)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	if revoked {
		a.audit(r.Context(), &actor.ID, "session_revoked", "session", &id, a.clientIP(r), r.UserAgent(), nil)
	}
	http.Redirect(w, r, "/admin/sessions", http.StatusFound)
}

func (a *App) handleAdminMetricsPage(w http.ResponseWriter, r *http.Request) {
	stats, err := a.store.AdminStats(r.Context())
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	serviceStats, err := a.store.ServiceStats(r.Context())
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "admin_metrics", map[string]any{
		"Title":        "Метрики",
		"Stats":        stats,
		"ServiceStats": serviceStats,
	})
}

func (a *App) handleAdminService(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	service := strings.Trim(strings.ToLower(rest), "/")
	if service != "comfyui" && service != "openwebui" {
		http.NotFound(w, r)
		return
	}
	analytics, err := a.store.ServiceAnalytics(r.Context(), service, 30)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	analytics.DisplayName = serviceDisplayName(service)
	a.render(w, r, "admin_service", map[string]any{
		"Title":     analytics.DisplayName,
		"Analytics": analytics,
	})
}

func (a *App) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	audits, err := a.store.ListAudit(r.Context(), 250)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "admin_audit", map[string]any{"Title": "Журнал аудита", "Audits": audits})
}
