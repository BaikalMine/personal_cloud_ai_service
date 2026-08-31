package gateway

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestAdminUsersRendersAccountLifetime(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Date(2026, 8, 31, 18, 45, 0, 0, time.UTC)
	users := []domain.UserRow{
		{ID: 1, Username: "permanent", Role: "user"},
		{ID: 2, Username: "temporary", Role: "user", AccountExpiresAt: sql.NullTime{Time: expiresAt, Valid: true}},
	}
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "admin_users", map[string]any{"Title": "Пользователи", "Users": users}); err != nil {
		t.Fatal(err)
	}
	page := rendered.String()
	for _, expected := range []string{"Постоянная", "Временная", "Без автоматического удаления", "Удаление:", `data-account-expiry="1788201900000"`} {
		if !strings.Contains(page, expected) {
			t.Fatalf("users page does not contain %q", expected)
		}
	}
}

func TestAdminUserDetailRendersTemporaryAccountDeadline(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Date(2026, 8, 31, 18, 45, 0, 0, time.UTC)
	profile := domain.User{ID: 2, Username: "temporary", Role: "user", AccountExpiresAt: sql.NullTime{Time: expiresAt, Valid: true}}
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "admin_user_detail", map[string]any{"Title": "Пользователь", "Profile": profile, "Stats": domain.UserStats{}}); err != nil {
		t.Fatal(err)
	}
	page := rendered.String()
	for _, expected := range []string{"Временная", "Тип аккаунта", "Временный", "Автоудаление", `data-account-expiry="1788201900000"`} {
		if !strings.Contains(page, expected) {
			t.Fatalf("user detail does not contain %q", expected)
		}
	}
}
