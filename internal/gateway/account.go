package gateway

import (
	"context"
	"errors"
	"net/http"

	"ai-access-gateway/internal/security"
)

func (a *App) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		a.render(w, r, "account_password", map[string]any{"Title": "Смена пароля"})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}

	currentPassword := r.Form.Get("current_password")
	newPassword := r.Form.Get("new_password")
	confirmPassword := r.Form.Get("confirm_password")
	if passwordError := validateNewPassword(newPassword); passwordError != "" {
		a.render(w, r, "account_password", map[string]any{"Title": "Смена пароля", "Error": passwordError})
		return
	}
	if newPassword != confirmPassword {
		a.render(w, r, "account_password", map[string]any{"Title": "Смена пароля", "Error": "Новый пароль и подтверждение не совпадают."})
		return
	}

	passwordHash, err := a.store.PasswordHash(r.Context(), user.ID)
	if err != nil || !security.VerifyPassword(passwordHash, currentPassword) {
		a.render(w, r, "account_password", map[string]any{"Title": "Смена пароля", "Error": "Текущий пароль указан неверно."})
		return
	}
	if err := a.updateUserPassword(r.Context(), user.ID, newPassword); err != nil {
		http.Error(w, "не удалось изменить пароль", http.StatusInternalServerError)
		return
	}
	a.revokeOtherSessions(user.ID, r)
	a.audit(r.Context(), &user.ID, "user_password_changed", "user", &user.ID, a.clientIP(r), r.UserAgent(), nil)
	a.render(w, r, "account_password", map[string]any{"Title": "Смена пароля", "Success": "Пароль изменён."})
}

func (a *App) updateUserPassword(ctx context.Context, userID int64, password string) error {
	passwordHash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	return a.store.UpdatePassword(ctx, userID, passwordHash)
}

func validateNewPassword(password string) string {
	if errors.Is(security.ValidatePassword(password), security.ErrPasswordTooShort) {
		return "Новый пароль должен содержать не менее 10 символов."
	}
	if errors.Is(security.ValidatePassword(password), security.ErrPasswordTooLong) {
		return "Новый пароль не должен превышать 256 символов."
	}
	return ""
}

func (a *App) revokeOtherSessions(userID int64, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		_, _ = a.store.RevokeSessions(r.Context(), userID)
		return
	}
	_, _ = a.store.RevokeOtherSessions(r.Context(), userID, security.HashToken(cookie.Value))
}
