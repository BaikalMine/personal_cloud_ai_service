package gateway

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

type userNotificationView struct {
	ID              int64     `json:"id"`
	GenerationJobID string    `json:"generation_job_id"`
	SourceKind      string    `json:"source_kind"`
	SourceID        string    `json:"source_id"`
	Kind            string    `json:"kind"`
	Title           string    `json:"title"`
	Message         string    `json:"message"`
	Href            string    `json:"href"`
	Read            bool      `json:"read"`
	CreatedAt       time.Time `json:"created_at"`
}

type userNotificationPreferencesView struct {
	InAppEnabled   bool `json:"in_app_enabled"`
	SuccessEnabled bool `json:"success_enabled"`
	BrowserEnabled bool `json:"browser_enabled"`
}

type userNotificationSummaryView struct {
	Revision    int64 `json:"revision"`
	UnreadCount int   `json:"unread_count"`
	ActiveCount int   `json:"active_count"`
}

func (a *App) registerNotificationRoutes(mux *http.ServeMux) {
	mux.Handle("/notifications", a.requireAuth(http.HandlerFunc(a.handleUserNotifications)))
	mux.Handle("/notifications/events", a.requireAuth(http.HandlerFunc(a.handleUserNotificationEvents)))
	mux.Handle("/notifications/read", a.requireAuth(http.HandlerFunc(a.handleUserNotificationRead)))
	mux.Handle("/account/notifications", a.requireAuth(http.HandlerFunc(a.handleAccountNotifications)))
}

func (a *App) handleUserNotifications(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	// Capture the revision before the list, never acknowledge a newer unseen list.
	summary, err := a.store.UserNotificationSummary(r.Context(), user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить состояние уведомлений")
		return
	}
	notifications, err := a.store.ListUserNotifications(r.Context(), user.ID, limit)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить уведомления")
		return
	}
	items := make([]userNotificationView, 0, len(notifications))
	for _, notification := range notifications {
		items = append(items, userNotificationView{
			ID: notification.ID, GenerationJobID: notification.GenerationJobPublicID,
			SourceKind: notification.SourceKind, SourceID: notification.SourceID,
			Kind: string(notification.Kind), Title: notification.Title, Message: notification.Message,
			Href: a.publicNotificationHref(notification.Href), Read: notification.ReadAt != nil, CreatedAt: notification.CreatedAt,
		})
	}
	payload := map[string]any{
		"notifications": items,
		"summary": userNotificationSummaryView{
			Revision: summary.Revision, UnreadCount: summary.UnreadCount, ActiveCount: summary.ActiveCount,
		},
		"preferences": userNotificationPreferencesView{
			InAppEnabled: summary.Preferences.InAppEnabled, SuccessEnabled: summary.Preferences.SuccessEnabled,
			BrowserEnabled: summary.Preferences.BrowserEnabled,
		},
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) handleUserNotificationRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}
	user := a.currentUser(r)
	if r.Form.Get("all") == "true" {
		changed, err := a.store.MarkAllUserNotificationsRead(r.Context(), user.ID)
		if err != nil {
			writeGenerationError(w, http.StatusInternalServerError, "не удалось отметить уведомления")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"changed": changed})
		return
	}
	notificationID, err := strconv.ParseInt(strings.TrimSpace(r.Form.Get("notification_id")), 10, 64)
	if err != nil || notificationID <= 0 {
		writeGenerationError(w, http.StatusBadRequest, "укажите уведомление")
		return
	}
	changed, err := a.store.MarkUserNotificationRead(r.Context(), user.ID, notificationID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось отметить уведомление")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed})
}

func (a *App) handleUserNotificationEvents(w http.ResponseWriter, r *http.Request) {
	a.serveLiveEvents(w, r, "notifications", writeUserNotificationRevisionEvent)
}

func writeUserNotificationRevisionEvent(w io.Writer, event string, revision int64) error {
	_, err := fmt.Fprintf(w, "event: %s\ndata: {\"revision\":%d}\n\n", event, revision)
	return err
}

func (a *App) publicNotificationHref(href string) string {
	href = strings.TrimSpace(href)
	base := strings.TrimRight(strings.TrimSpace(a.cfg.PublicBaseURL), "/")
	if base == "" || !strings.HasPrefix(href, "/") {
		return href
	}
	return base + href
}

func notificationPreferencesFromSummary(summary domain.UserNotificationSummary) userNotificationPreferencesView {
	return userNotificationPreferencesView{
		InAppEnabled: summary.Preferences.InAppEnabled, SuccessEnabled: summary.Preferences.SuccessEnabled,
		BrowserEnabled: summary.Preferences.BrowserEnabled,
	}
}
