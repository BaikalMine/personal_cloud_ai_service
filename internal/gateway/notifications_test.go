package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestAccountProfileRendersNotificationCenterAndPreferences(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	err = templates.ExecuteTemplate(&rendered, "account_profile", map[string]any{
		"Title": "Профиль", "AssetVersion": templates.AssetVersion, "CSRF": "test-csrf",
		"CurrentUser":             &domain.User{ID: 7, Username: "alice"},
		"NotificationSummary":     domain.UserNotificationSummary{Revision: 9, UnreadCount: 2, ActiveCount: 1},
		"NotificationPreferences": domain.UserNotificationPreferences{InAppEnabled: true, SuccessEnabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"data-notification-center", "Задачи и уведомления", "data-notification-active-count", "data-notification-unread-count",
		"Krea2, Flux2 и MiniMax H3", "data-notification-settings", "В интерфейсе", "Успешные генерации", "В браузере",
		"/static/notifications.css", "/static/notifications.js",
	} {
		if !strings.Contains(rendered.String(), expected) {
			t.Fatalf("profile does not contain %q", expected)
		}
	}
}

func TestNotificationRevisionEvent(t *testing.T) {
	var event bytes.Buffer
	if err := writeUserNotificationRevisionEvent(&event, "notifications", 42); err != nil {
		t.Fatal(err)
	}
	if event.String() != "event: notifications\ndata: {\"revision\":42}\n\n" {
		t.Fatalf("notification event = %q", event.String())
	}
}

func TestPublicNotificationHref(t *testing.T) {
	app := &App{cfg: Config{PublicBaseURL: "https://ai.example.test/"}}
	if href := app.publicNotificationHref("/generate?job=job-1"); href != "https://ai.example.test/generate?job=job-1" {
		t.Fatalf("public notification href = %q", href)
	}
	if href := app.publicNotificationHref("https://external.example/result"); href != "https://external.example/result" {
		t.Fatalf("absolute notification href = %q", href)
	}
}

func TestNotificationStylesAreServed(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/static/notifications.css", nil)
	(&App{}).handleStaticCSS(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("notification CSS response = status:%d headers:%v", response.Code, response.Header())
	}
	if !strings.Contains(response.Body.String(), ".notification-panel") {
		t.Fatal("notification CSS is missing panel styles")
	}
}
