package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func (a *App) handleAccountProfile(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	render := func(errorMessage string, successMessage string, username string, email string) {
		a.render(w, r, "account_profile", map[string]any{
			"Title": "Профиль", "ProfileUsername": username, "ProfileEmail": email,
			"CanChangeUsername": user.Role == "admin", "Error": errorMessage, "Success": successMessage,
		})
	}

	currentEmail := ""
	if user.Email.Valid {
		currentEmail = user.Email.String
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		success := ""
		if r.URL.Query().Get("updated") == "1" {
			success = "Профиль обновлён."
		}
		render("", success, user.Username, currentEmail)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}

	username := user.Username
	if user.Role == "admin" {
		username = strings.TrimSpace(r.Form.Get("username"))
		if usernameError := validateProfileUsername(username); usernameError != "" {
			render(usernameError, "", username, strings.TrimSpace(r.Form.Get("email")))
			return
		}
	}
	email := strings.ToLower(strings.TrimSpace(r.Form.Get("email")))
	if emailError := validateProfileEmail(email); emailError != "" {
		render(emailError, "", username, email)
		return
	}
	if err := a.store.UpdateOwnProfile(r.Context(), user.ID, username, email, user.Role == "admin"); err != nil {
		switch {
		case errors.Is(err, store.ErrEmailExists):
			render("Этот e-mail уже используется другой учётной записью.", "", username, email)
		case errors.Is(err, store.ErrUsernameExists):
			render("Этот логин уже используется другой учётной записью.", "", username, email)
		default:
			http.Error(w, "не удалось обновить профиль", http.StatusInternalServerError)
		}
		return
	}
	a.audit(r.Context(), &user.ID, "user_profile_updated", "user", &user.ID, a.clientIP(r), r.UserAgent(), map[string]any{
		"username_changed": username != user.Username,
		"email_changed":    email != currentEmail,
	})
	http.Redirect(w, r, "/account/profile?updated=1", http.StatusSeeOther)
}

func validateProfileUsername(username string) string {
	if len(username) < 3 || len(username) > 48 {
		return "Логин должен содержать от 3 до 48 символов."
	}
	for _, r := range username {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return "Логин может содержать только латинские буквы, цифры, точку, подчёркивание и дефис."
		}
	}
	return ""
}

func validateProfileEmail(email string) string {
	if email == "" {
		return ""
	}
	if len(email) > 254 {
		return "Адрес e-mail слишком длинный."
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "Укажите корректный адрес e-mail."
	}
	return ""
}

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

func (a *App) handleAccountQuickGenerationPriority(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	if user == nil || user.Role != "admin" {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}
	enabled := r.Form.Get("enabled") == "on"
	updated, err := a.store.SetAdminQuickGenerationMiningPriority(r.Context(), user.ID, enabled)
	if err != nil {
		http.Error(w, "не удалось изменить режим приоритетной генерации", http.StatusInternalServerError)
		return
	}
	if !updated {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return
	}
	a.audit(r.Context(), &user.ID, "admin_quick_generation_priority_updated", "user", &user.ID, a.clientIP(r), r.UserAgent(), map[string]any{"enabled": enabled})
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled})
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
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
