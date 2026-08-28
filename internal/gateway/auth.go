package gateway

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		a.render(w, r, "login", map[string]any{
			"Title": "Вход",
			"Next":  safeNext(r.URL.Query().Get("next")),
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !validFormOrigin(r) {
		http.Error(w, "источник запроса запрещён", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	ip := a.clientIP(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "неверные данные формы", http.StatusBadRequest)
		return
	}
	identity := strings.TrimSpace(r.Form.Get("login"))
	if identity == "" {
		// Keep existing password-manager and direct-form clients working.
		identity = strings.TrimSpace(r.Form.Get("username"))
	}
	password := r.Form.Get("password")
	nextURL := safeNext(r.Form.Get("next"))
	limitKey := loginRateLimitKey(ip, identity)
	if !a.loginLimiter.Allow(limitKey) {
		a.loginFailures.Add(1)
		a.audit(r.Context(), nil, "user_login_failed", "user", nil, ip, r.UserAgent(), map[string]any{
			"identity": truncate(identity, 256), "reason": "rate_limited",
		})
		a.render(w, r, "login", map[string]any{"Title": "Вход", "Error": "Слишком много попыток входа. Попробуйте позже."})
		return
	}

	var user User
	var passwordHash string
	var err error
	if len(identity) <= 254 {
		user, passwordHash, err = a.store.FindUserWithPassword(r.Context(), identity)
	} else {
		err = sql.ErrNoRows
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	userFound := err == nil
	passwordValid := security.VerifyPassword(passwordHash, password)
	accountLocked := userFound && user.Role != "admin" && user.IsLocked(time.Now())
	if !userFound || user.Disabled || accountLocked || !passwordValid {
		a.loginLimiter.RecordFailure(limitKey)
		a.loginFailures.Add(1)
		reason := "invalid_credentials"
		if accountLocked {
			reason = "account_locked"
		} else if userFound && user.Role != "admin" && !user.Disabled && !passwordValid {
			lockedUntil, failureErr := a.store.RecordLoginFailure(
				r.Context(), identity, a.cfg.AccountLockThreshold, a.cfg.AccountLockDuration,
			)
			if failureErr != nil {
				log.Printf("record login failure: %v", failureErr)
			} else if lockedUntil.Valid {
				reason = "account_locked"
			}
		}
		a.audit(r.Context(), nil, "user_login_failed", "user", nil, ip, r.UserAgent(), map[string]any{"identity": truncate(identity, 256), "reason": reason})
		a.render(w, r, "login", map[string]any{"Title": "Вход", "Error": "Неверный логин или пароль.", "Next": nextURL})
		return
	}
	if security.PasswordHashNeedsUpgrade(passwordHash) {
		upgradedHash, hashErr := security.HashPassword(password)
		if hashErr != nil {
			http.Error(w, "не удалось обновить защиту пароля", http.StatusInternalServerError)
			return
		}
		if err := a.store.UpdatePassword(r.Context(), user.ID, upgradedHash); err != nil {
			http.Error(w, "ошибка обновления защиты пароля", http.StatusInternalServerError)
			return
		}
	}

	if err := a.store.RecordLoginSuccess(r.Context(), user.ID); err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	token, err := a.createSession(r.Context(), user.ID, ip, r.UserAgent())
	if err != nil {
		http.Error(w, "не удалось создать сессию", http.StatusInternalServerError)
		return
	}
	a.loginLimiter.Clear(limitKey)
	http.SetCookie(w, a.sessionCookie(r, token))
	a.audit(r.Context(), &user.ID, "user_login_success", "user", &user.ID, ip, r.UserAgent(), nil)
	if user.Role == "admin" {
		a.audit(r.Context(), &user.ID, "admin_login", "user", &user.ID, ip, r.UserAgent(), nil)
	}
	if nextURL == "" {
		nextURL = "/app"
	}
	http.Redirect(w, r, nextURL, http.StatusFound)
}

func loginRateLimitKey(ip, username string) string {
	return ip + "\x00" + strings.ToLower(truncate(strings.TrimSpace(username), 256))
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !validFormOrigin(r) {
		http.Error(w, "источник запроса запрещён", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}
	user := a.currentUser(r)
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = a.store.DeleteSessionByHash(r.Context(), security.HashToken(c.Value))
	}
	if user != nil {
		a.audit(r.Context(), &user.ID, "user_logout", "session", nil, a.clientIP(r), r.UserAgent(), nil)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cookieSecure(r),
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (a *App) handleInvite(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/invite/"))
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		access, err := a.store.AvailableInvite(r.Context(), security.HashToken(token))
		if err != nil {
			a.render(w, r, "invite", map[string]any{"Title": "Приглашение", "Invalid": true})
			return
		}
		a.render(w, r, "invite", map[string]any{"Title": "Создание аккаунта", "Token": token, "Access": access})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !validFormOrigin(r) {
		http.Error(w, "источник запроса запрещён", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "неверные данные формы", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.Form.Get("username"))
	email := strings.TrimSpace(r.Form.Get("email"))
	password := r.Form.Get("password")
	if validationError := validateCredentials(username, email, password); validationError != "" {
		a.render(w, r, "invite", map[string]any{
			"Title": "Создание аккаунта", "Token": token,
			"Error": validationError,
		})
		return
	}
	passwordHash, err := security.HashPassword(password)
	if err != nil {
		http.Error(w, "ошибка обработки пароля", http.StatusInternalServerError)
		return
	}
	userID, inviteID, err := a.store.RegisterFromInvite(r.Context(), store.RegisterFromInviteParams{
		TokenHash:    security.HashToken(token),
		Username:     username,
		Email:        email,
		PasswordHash: passwordHash,
		IP:           a.clientIP(r),
	})
	if errors.Is(err, store.ErrInviteUnavailable) {
		a.render(w, r, "invite", map[string]any{"Title": "Создание аккаунта", "Token": token, "Invalid": true})
		return
	}
	if errors.Is(err, store.ErrUsernameExists) {
		a.render(w, r, "invite", map[string]any{"Title": "Создание аккаунта", "Token": token, "Error": "Этот логин уже занят."})
		return
	}
	if errors.Is(err, store.ErrEmailExists) {
		a.render(w, r, "invite", map[string]any{"Title": "Создание аккаунта", "Token": token, "Error": "Этот email уже используется."})
		return
	}
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	a.audit(r.Context(), &userID, "invite_used", "invite", &inviteID, a.clientIP(r), r.UserAgent(), map[string]any{"username": username})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (a *App) currentUser(r *http.Request) *User {
	if v := r.Context().Value(userCtxKey); v != nil {
		if u, ok := v.(*User); ok {
			return u
		}
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	tokenHash := security.HashToken(c.Value)
	u, err := a.store.UserBySessionHash(r.Context(), tokenHash, a.cfg.SessionIdleTimeout)
	if err != nil {
		return nil
	}
	_ = a.store.TouchSession(r.Context(), tokenHash)
	return &u
}

func (a *App) createSession(ctx context.Context, userID int64, ip, userAgent string) (string, error) {
	token, err := security.RandomToken()
	if err != nil {
		return "", err
	}
	err = a.store.CreateSession(ctx, userID, security.HashToken(token), time.Now().Add(a.cfg.SessionTTL), truncate(userAgent, 500), ip)
	return token, err
}

func (a *App) sessionCookie(r *http.Request, token string) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(a.cfg.SessionTTL),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cookieSecure(r),
	}
}

func (a *App) cookieSecure(r *http.Request) bool {
	if !a.cfg.CookieSecure || a.cfg.PublicURL == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return strings.EqualFold(strings.TrimSuffix(host, "."), a.cfg.PublicURL.Hostname())
}

func validateCredentials(username, email, password string) string {
	if len(username) < 3 || len(username) > 48 {
		return "Логин должен содержать от 3 до 48 символов."
	}
	for _, r := range username {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return "Логин может содержать только латинские буквы, цифры, точку, подчёркивание и дефис."
		}
	}
	if email != "" {
		if len(email) > 254 {
			return "Адрес email слишком длинный."
		}
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email {
			return "Укажите корректный адрес email."
		}
	}
	if errors.Is(security.ValidatePassword(password), security.ErrPasswordTooShort) {
		return "Пароль должен содержать не менее 10 символов."
	}
	if errors.Is(security.ValidatePassword(password), security.ErrPasswordTooLong) {
		return "Пароль не должен превышать 256 символов."
	}
	return ""
}

func (a *App) csrfToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	return a.csrfSigner.Token(c.Value)
}

func (a *App) validCSRF(r *http.Request) bool {
	var err error
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		err = r.ParseMultipartForm(1 << 20)
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		return false
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return a.csrfSigner.Verify(cookie.Value, r.Form.Get("csrf"))
}
