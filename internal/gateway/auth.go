package gateway

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"strings"
	"time"

	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		a.renderAuthPage(w, r, http.StatusOK, "login", AuthPageView{Next: r.URL.Query().Get("next")})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !validFormOrigin(r) {
		a.renderAuthPage(w, r, http.StatusForbidden, "login", AuthPageView{Error: "Источник запроса не подтверждён. Откройте форму входа заново."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	ip := a.clientIP(r)
	if err := r.ParseForm(); err != nil {
		a.renderAuthPage(w, r, http.StatusBadRequest, "login", AuthPageView{Error: "Не удалось прочитать форму. Введите данные снова."})
		return
	}
	identity := strings.TrimSpace(r.Form.Get("login"))
	if identity == "" {
		// Keep existing password-manager and direct-form clients working.
		identity = strings.TrimSpace(r.Form.Get("username"))
	}
	password := r.Form.Get("password")
	nextURL := safeNext(r.Form.Get("next"))
	view := AuthPageView{Login: identity, Next: nextURL}
	fail := func(status int, field, message string) {
		view.ErrorField, view.Error = field, message
		a.renderAuthPage(w, r, status, "login", view)
	}
	limitKey := loginRateLimitKey(ip, identity)
	limitIP := rateLimitIP(ip)
	if !limiterAllows(a.loginIPLimiter, limitIP) || !limiterAllows(a.loginLimiter, limitKey) {
		a.loginFailures.Add(1)
		if limiterAllows(a.loginAuditLimiter, limitIP) {
			a.audit(r.Context(), nil, "user_login_failed", "user", nil, ip, r.UserAgent(), map[string]any{
				"identity": truncate(identity, 256), "reason": "rate_limited",
			})
			recordLimiterFailure(a.loginAuditLimiter, limitIP)
		}
		fail(http.StatusOK, "", "Слишком много попыток входа. Попробуйте позже.")
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
		fail(http.StatusServiceUnavailable, "", "Вход временно недоступен. Повторите попытку позже.")
		return
	}
	userFound := err == nil
	releasePasswordWork, acquired := acquireBoundedSlot(r.Context(), a.passwordWorkSlots, 3*time.Second)
	if !acquired {
		fail(http.StatusOK, "", "Сервер занят проверкой входа. Повторите попытку через несколько секунд.")
		return
	}
	passwordValid := security.VerifyPassword(passwordHash, password)
	accountLocked := userFound && user.Role != "admin" && user.IsLocked(time.Now())
	var upgradedHash string
	var hashErr error
	if userFound && !user.Disabled && !accountLocked && passwordValid && security.PasswordHashNeedsUpgrade(passwordHash) {
		upgradedHash, hashErr = security.HashPassword(password)
	}
	releasePasswordWork()
	if !userFound || user.Disabled || accountLocked || !passwordValid {
		recordLimiterFailure(a.loginLimiter, limitKey)
		recordLimiterFailure(a.loginIPLimiter, limitIP)
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
		fail(http.StatusOK, "login", "Неверный логин или пароль.")
		return
	}
	if hashErr != nil {
		fail(http.StatusServiceUnavailable, "", "Не удалось завершить вход. Повторите попытку позже.")
		return
	}
	if upgradedHash != "" {
		if err := a.store.UpdatePassword(r.Context(), user.ID, upgradedHash); err != nil {
			fail(http.StatusServiceUnavailable, "", "Не удалось завершить вход. Повторите попытку позже.")
			return
		}
	}

	if err := a.store.RecordLoginSuccess(r.Context(), user.ID); err != nil {
		fail(http.StatusServiceUnavailable, "", "Не удалось завершить вход. Повторите попытку позже.")
		return
	}
	token, err := a.createSession(r.Context(), user.ID, ip, r.UserAgent())
	if err != nil {
		fail(http.StatusServiceUnavailable, "", "Не удалось создать сессию. Повторите вход.")
		return
	}
	if a.loginLimiter != nil {
		a.loginLimiter.Clear(limitKey)
	}
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
	return rateLimitIP(ip) + "\x00" + strings.ToLower(truncate(strings.TrimSpace(username), 256))
}

func rateLimitIP(raw string) string {
	address, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	address = address.Unmap()
	if address.Is6() {
		return netip.PrefixFrom(address, 64).Masked().String()
	}
	return address.String()
}

func limiterAllows(limiter *security.LoginLimiter, key string) bool {
	return limiter == nil || limiter.Allow(key)
}

func recordLimiterFailure(limiter *security.LoginLimiter, key string) {
	if limiter != nil {
		limiter.RecordFailure(key)
	}
}

func acquireBoundedSlot(ctx context.Context, slots chan struct{}, wait time.Duration) (func(), bool) {
	if slots == nil {
		return func() {}, true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	case <-ctx.Done():
		return nil, false
	case <-timer.C:
		return nil, false
	}
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
	sessionHash := ""
	if c, err := r.Cookie(sessionCookieName); err == nil {
		sessionHash = security.HashToken(c.Value)
		_ = a.store.DeleteSessionByHash(r.Context(), sessionHash)
	}
	if sessionHash != "" {
		a.closeSessionWebSockets(sessionHash)
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
		a.notFoundPage(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	view := AuthPageView{State: "active"}
	fail := func(status int, state, field, message string) {
		view.State, view.ErrorField, view.Error = state, field, message
		a.renderAuthPage(w, r, status, "invite", view)
	}
	if r.Method == http.MethodPost {
		if !validFormOrigin(r) {
			fail(http.StatusForbidden, "unavailable", "", "Источник запроса не подтверждён. Откройте приглашение заново.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest, "unavailable", "", "Не удалось прочитать форму. Введите данные снова.")
			return
		}
		view.Username = strings.TrimSpace(r.Form.Get("username"))
		view.Email = strings.TrimSpace(r.Form.Get("email"))
		if !limiterAllows(a.inviteLimiter, rateLimitIP(a.clientIP(r))) {
			fail(http.StatusOK, "active", "", "Слишком много попыток регистрации. Попробуйте позже.")
			return
		}
	}
	access, err := a.store.AvailableInvite(r.Context(), security.HashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrInviteUnavailable) {
			if r.Method == http.MethodPost {
				recordLimiterFailure(a.inviteLimiter, rateLimitIP(a.clientIP(r)))
			}
			fail(http.StatusOK, "invalid", "", "Приглашение недействительно, просрочено, отозвано или уже использовано.")
		} else {
			fail(http.StatusServiceUnavailable, "unavailable", "", "Не удалось проверить приглашение. Повторите проверку позже.")
		}
		return
	}
	view.Access = &access
	if r.Method != http.MethodPost {
		a.renderAuthPage(w, r, http.StatusOK, "invite", view)
		return
	}
	password := r.Form.Get("password")
	if field, message := credentialValidation(view.Username, view.Email, password); message != "" {
		fail(http.StatusOK, "active", field, message)
		return
	}
	ip := a.clientIP(r)
	limitIP := rateLimitIP(ip)
	recordLimiterFailure(a.inviteLimiter, limitIP)
	releasePasswordWork, acquired := acquireBoundedSlot(r.Context(), a.passwordWorkSlots, 3*time.Second)
	if !acquired {
		fail(http.StatusOK, "active", "", "Сервер занят обработкой регистраций. Повторите попытку через несколько секунд.")
		return
	}
	passwordHash, err := security.HashPassword(password)
	releasePasswordWork()
	if err != nil {
		fail(http.StatusServiceUnavailable, "active", "", "Не удалось защитить пароль. Повторите попытку позже.")
		return
	}
	userID, inviteID, err := a.store.RegisterFromInvite(r.Context(), store.RegisterFromInviteParams{
		TokenHash: security.HashToken(token), Username: view.Username, Email: view.Email,
		PasswordHash: passwordHash, IP: ip,
	})
	switch {
	case errors.Is(err, store.ErrInviteUnavailable):
		view.Access = nil
		fail(http.StatusOK, "invalid", "", "Приглашение больше недоступно. Запросите новое у администратора.")
		return
	case errors.Is(err, store.ErrUsernameExists):
		fail(http.StatusOK, "active", "username", "Этот логин уже занят.")
		return
	case errors.Is(err, store.ErrEmailExists):
		fail(http.StatusOK, "active", "email", "Этот email уже используется.")
		return
	case err != nil:
		fail(http.StatusServiceUnavailable, "uncertain", "", "Результат регистрации не подтверждён. Попробуйте войти с указанными данными, прежде чем регистрироваться повторно.")
		return
	}
	if a.inviteLimiter != nil {
		a.inviteLimiter.Clear(limitIP)
	}
	a.audit(r.Context(), &userID, "invite_used", "invite", &inviteID, ip, r.UserAgent(), map[string]any{"username": view.Username})
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
	_, message := credentialValidation(username, email, password)
	return message
}

func credentialValidation(username, email, password string) (string, string) {
	if len(username) < 3 || len(username) > 48 {
		return "username", "Логин должен содержать от 3 до 48 символов."
	}
	for _, r := range username {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return "username", "Логин может содержать только латинские буквы, цифры, точку, подчёркивание и дефис."
		}
	}
	if email != "" {
		if len(email) > 254 {
			return "email", "Адрес email слишком длинный."
		}
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email {
			return "email", "Укажите корректный адрес email."
		}
	}
	if errors.Is(security.ValidatePassword(password), security.ErrPasswordTooShort) {
		return "password", "Пароль должен содержать не менее 10 символов."
	}
	if errors.Is(security.ValidatePassword(password), security.ErrPasswordTooLong) {
		return "password", "Пароль не должен превышать 256 символов."
	}
	return "", ""
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
		err = r.ParseMultipartForm(maxSuggestionRequest)
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
