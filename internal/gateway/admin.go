package gateway

import (
	"bytes"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
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
	a.render(w, r, "admin_dashboard", map[string]any{
		"Title":    "Администрирование",
		"Stats":    stats,
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
	case path == "mining":
		a.handleAdminMining(w, r)
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
	if service != "comfyui" && service != "openwebui" {
		service = ""
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		query = query[:200]
	}
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
			Response: response, Metadata: metadata, MediaCount: row.MediaCount,
			Media:     mediaByEvent[row.ID],
			CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		})
		overview.Total++
		switch row.Service {
		case "comfyui":
			overview.ComfyUI++
		case "openwebui":
			overview.OpenWebUI++
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
	select {
	case a.adminMediaSlots <- struct{}{}:
		defer func() { <-a.adminMediaSlots }()
	default:
		http.Error(w, "слишком много одновременных запросов медиа", http.StatusTooManyRequests)
		return
	}
	media, err := a.store.ContentMediaByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	payload, err := a.contentCipher.DecryptBytes(media.PayloadCipher)
	if err != nil {
		http.Error(w, "ошибка расшифровки медиа", http.StatusInternalServerError)
		return
	}
	contentType, inline := safeAdminMediaType(media.MediaType, media.MIMEType)
	disposition := "attachment"
	if inline {
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
	a.render(w, r, "admin_users", map[string]any{"Title": "Пользователи", "Users": users, "Query": search})
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
			updated, err := a.store.SetServiceAccess(r.Context(), id, comfyUI, openWebUI)
			if err != nil {
				http.Error(w, "не удалось обновить права доступа", http.StatusInternalServerError)
				return
			}
			if updated {
				a.audit(r.Context(), &actor.ID, "user_service_access_updated", "user", &id, a.clientIP(r), r.UserAgent(), map[string]any{"comfyui": comfyUI, "openwebui": openWebUI})
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
		if !grantComfyUI && !grantOpenWebUI {
			invites, _ := a.store.ListInvites(r.Context(), 200)
			a.render(w, r, "admin_invites", map[string]any{"Title": "Приглашения", "Invites": invites, "Error": "Выберите хотя бы один сервис."})
			return
		}
		token, err := security.RandomToken()
		if err != nil {
			http.Error(w, "не удалось создать защищённый токен", http.StatusInternalServerError)
			return
		}
		id, err := a.store.CreateInvite(r.Context(), store.CreateInviteParams{
			TokenHash:       security.HashToken(token),
			CreatedByUserID: actor.ID,
			MaxUses:         maxUses,
			ExpiresAt:       expiresAt,
			GrantComfyUI:    grantComfyUI,
			GrantOpenWebUI:  grantOpenWebUI,
		})
		if err != nil {
			http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
			return
		}
		a.audit(r.Context(), &actor.ID, "invite_created", "invite", &id, a.clientIP(r), r.UserAgent(), map[string]any{"max_uses": maxUses, "expires_at": expiresAt, "grant_comfyui": grantComfyUI, "grant_openwebui": grantOpenWebUI})
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
