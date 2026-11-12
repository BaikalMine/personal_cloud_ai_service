package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func (a *App) handleAccountProfile(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	tab, valid := accountProfileTab(r)
	if !valid {
		a.renderAccountProfilePage(w, r, 400, "profile", map[string]any{"Error": "Раздел настроек не найден."})
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		success := ""
		switch {
		case r.URL.Query().Get("updated") == "1":
			success = "Профиль обновлён."
		case r.URL.Query().Get("notifications_updated") == "1":
			success = "Настройки уведомлений обновлены."
			tab = "notifications"
		case r.URL.Query().Get("mining_updated") == "1":
			success = "Настройка майнинга сохранена."
			tab = "access"
		case r.URL.Query().Get("priority_updated") == "1":
			success = "Приоритет заданий сохранён."
			tab = "access"
		case r.URL.Query().Get("theme_updated") == "1":
			success = "Тема сохранена."
		}
		a.renderAccountProfilePage(w, r, 200, tab, map[string]any{"Success": success})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "метод не поддерживается", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !a.validCSRF(r) {
		a.renderAccountProfilePage(w, r, 403, "profile", map[string]any{"Error": "Не удалось подтвердить запрос. Обновите страницу.", "WriteUncertain": true})
		return
	}
	username := user.Username
	if user.Role == "admin" {
		username = strings.TrimSpace(r.Form.Get("username"))
	}
	email := strings.ToLower(strings.TrimSpace(r.Form.Get("email")))
	fail := func(status int, field, message string, uncertain bool) {
		a.renderAccountProfilePage(w, r, status, "profile", map[string]any{"ProfileUsername": truncate(username, 256), "ProfileEmail": truncate(email, 254), "Error": message, "ErrorField": field, "WriteUncertain": uncertain})
	}
	if user.Role == "admin" {
		if message := validateProfileUsername(username); message != "" {
			fail(200, "username", message, false)
			return
		}
	}
	if message := validateProfileEmail(email); message != "" {
		fail(200, "email", message, false)
		return
	}
	if err := a.store.UpdateOwnProfile(r.Context(), user.ID, username, email, user.Role == "admin"); err != nil {
		switch {
		case errors.Is(err, store.ErrEmailExists):
			fail(200, "email", "Этот e-mail уже используется другой учётной записью.", false)
		case errors.Is(err, store.ErrUsernameExists):
			fail(200, "username", "Этот логин уже используется другой учётной записью.", false)
		default:
			fail(503, "", "Результат сохранения не подтверждён. Обновите профиль перед повторной отправкой.", true)
		}
		return
	}
	a.audit(r.Context(), &user.ID, "user_profile_updated", "user", &user.ID, a.clientIP(r), r.UserAgent(), map[string]any{"username_changed": username != user.Username, "email_changed": email != user.Email.String})
	http.Redirect(w, r, "/account/profile?updated=1", http.StatusSeeOther)
}

func (a *App) handleAccountNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "метод не поддерживается", 405)
		return
	}
	user := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	jsonReply := strings.Contains(r.Header.Get("Accept"), "application/json")
	if !a.validCSRF(r) {
		if jsonReply {
			http.Error(w, "неверный защитный токен", 403)
		} else {
			a.renderAccountProfilePage(w, r, 403, "notifications", map[string]any{"Error": "Не удалось подтвердить запрос. Обновите страницу.", "WriteUncertain": true})
		}
		return
	}
	submitted := domain.UserNotificationPreferences{InAppEnabled: r.Form.Get("in_app_enabled") == "on", SuccessEnabled: r.Form.Get("success_enabled") == "on", BrowserEnabled: r.Form.Get("browser_enabled") == "on"}
	preferences, _, err := a.store.SetUserNotificationPreferences(r.Context(), user.ID, submitted)
	if err != nil {
		if jsonReply {
			writeGenerationError(w, 500, "не удалось сохранить настройки уведомлений")
		} else {
			a.renderAccountProfilePage(w, r, 503, "notifications", map[string]any{"NotificationPreferences": submitted, "Error": "Результат сохранения не подтверждён. Проверьте настройки перед повторной отправкой.", "WriteUncertain": true})
		}
		return
	}
	a.audit(r.Context(), &user.ID, "user_notification_preferences_updated", "user", &user.ID, a.clientIP(r), r.UserAgent(), map[string]any{"in_app_enabled": preferences.InAppEnabled, "success_enabled": preferences.SuccessEnabled, "browser_enabled": preferences.BrowserEnabled})
	if jsonReply {
		writeJSON(w, 200, notificationPreferencesFromSummary(domain.UserNotificationSummary{Preferences: preferences}))
		return
	}
	http.Redirect(w, r, "/account/profile?tab=notifications&notifications_updated=1#notification-settings", http.StatusSeeOther)
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
	render := func(status int, field, message, success string, uncertain bool) {
		a.renderAccountPage(w, r, status, "account_password", "password", map[string]any{"Title": "Смена пароля", "ErrorField": field, "Error": message, "Success": success, "WriteUncertain": uncertain})
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		success := ""
		if r.URL.Query().Get("updated") == "1" {
			success = "Пароль изменён. Остальные сессии завершены."
		}
		render(200, "", "", success, false)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "метод не поддерживается", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !a.validCSRF(r) {
		render(403, "", "Не удалось подтвердить запрос. Обновите страницу.", "", true)
		return
	}
	currentPassword, newPassword, confirmation := r.Form.Get("current_password"), r.Form.Get("new_password"), r.Form.Get("confirm_password")
	if message := validateNewPassword(newPassword); message != "" {
		render(200, "new_password", message, "", false)
		return
	}
	if newPassword != confirmation {
		render(200, "confirm_password", "Новый пароль и подтверждение не совпадают.", "", false)
		return
	}
	verifiedHash, err := a.store.PasswordHash(r.Context(), user.ID)
	if err != nil {
		render(503, "", "Не удалось проверить пароль. Повторите попытку позже.", "", false)
		return
	}
	release, acquired := acquireBoundedSlot(r.Context(), a.passwordWorkSlots, 3*time.Second)
	if !acquired {
		render(503, "", "Сервер занят проверкой паролей. Повторите попытку позже.", "", false)
		return
	}
	defer release()
	if !security.VerifyPassword(verifiedHash, currentPassword) {
		render(200, "current_password", "Текущий пароль указан неверно.", "", false)
		return
	}
	newHash, err := security.HashPassword(newPassword)
	if err != nil {
		render(503, "", "Не удалось защитить новый пароль. Повторите попытку позже.", "", false)
		return
	}
	currentHash := ""
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		currentHash = security.HashToken(cookie.Value)
	}
	changed, err := a.store.ChangeOwnPassword(r.Context(), user.ID, verifiedHash, newHash, currentHash)
	if err != nil {
		render(503, "", "Результат смены пароля не подтверждён. Проверьте вход с новым паролем перед повторной попыткой.", "", true)
		return
	}
	if !changed {
		render(409, "", "Пароль уже изменён в другом запросе. Войдите в аккаунт заново.", "", true)
		return
	}
	if currentHash == "" {
		a.closeUserWebSockets(user.ID)
	} else {
		a.closeOtherUserWebSockets(user.ID, currentHash)
	}
	a.audit(r.Context(), &user.ID, "user_password_changed", "user", &user.ID, a.clientIP(r), r.UserAgent(), nil)
	http.Redirect(w, r, "/account/password?updated=1", http.StatusSeeOther)
}

func (a *App) handleAccountQuickGenerationPriority(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "метод не поддерживается", 405)
		return
	}
	user := a.currentUser(r)
	if user == nil || user.Role != "admin" {
		a.forbiddenPage(w, r, "Эта настройка доступна только администратору.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", 403)
		return
	}
	enabled := r.Form.Get("enabled") == "on"
	accountReturn := len(r.PostForm["return_to"]) == 1 && r.PostForm.Get("return_to") == "account-access"
	updated, err := a.store.SetAdminQueuePriority(r.Context(), user.ID, enabled)
	if err != nil {
		if accountReturn {
			a.renderAccountProfilePage(w, r, 503, "access", map[string]any{"PriorityPreference": enabled, "WriteUncertain": true, "Error": "Результат сохранения приоритета не подтверждён. Обновите данные перед повторной попыткой."})
		} else {
			http.Error(w, "не удалось изменить режим приоритетной генерации", 500)
		}
		return
	}
	if !updated {
		a.forbiddenPage(w, r, "Эта настройка доступна только администратору.")
		return
	}
	a.audit(r.Context(), &user.ID, "admin_quick_generation_priority_updated", "user", &user.ID, a.clientIP(r), r.UserAgent(), map[string]any{"enabled": enabled})
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, 200, map[string]any{"enabled": enabled})
		return
	}
	if accountReturn {
		http.Redirect(w, r, "/account/profile?tab=access&priority_updated=1#generation-resources", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (a *App) handleAccountGenerationMining(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "метод не поддерживается", 405)
		return
	}
	user := a.currentUser(r)
	if user == nil || user.Role != "admin" {
		a.forbiddenPage(w, r, "Эта настройка доступна только администратору.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !a.validCSRF(r) {
		a.renderAccountProfilePage(w, r, 403, "access", map[string]any{"Error": "Не удалось подтвердить запрос. Обновите страницу.", "WriteUncertain": true})
		return
	}
	enabled := r.Form.Get("pause_mining_for_quick_generation") == "on"
	updated, err := a.store.SetAdminGenerationMiningPolicy(r.Context(), user.ID, enabled)
	if err != nil {
		a.renderAccountProfilePage(w, r, 503, "access", map[string]any{"MiningPreference": enabled, "WriteUncertain": true, "Error": "Результат сохранения настройки майнинга не подтверждён. Обновите данные перед повторной попыткой."})
		return
	}
	if !updated {
		a.forbiddenPage(w, r, "Эта настройка доступна только администратору.")
		return
	}
	a.audit(r.Context(), &user.ID, "admin_generation_mining_policy_updated", "user", &user.ID, a.clientIP(r), r.UserAgent(), map[string]any{"enabled": enabled})
	http.Redirect(w, r, "/account/profile?tab=access&mining_updated=1#generation-resources", http.StatusSeeOther)
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
		a.closeUserWebSockets(userID)
		return
	}
	currentHash := security.HashToken(cookie.Value)
	_, _ = a.store.RevokeOtherSessions(r.Context(), userID, currentHash)
	a.closeOtherUserWebSockets(userID, currentHash)
}
