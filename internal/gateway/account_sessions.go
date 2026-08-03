package gateway

import (
	"net/http"
	"strconv"

	"ai-access-gateway/internal/security"
)

func (a *App) handleAccountSessions(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	currentHash := security.HashToken(cookie.Value)

	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		a.renderAccountSessions(w, r, user.ID, currentHash, "")
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

	switch r.Form.Get("action") {
	case "revoke_others":
		revoked, err := a.store.RevokeOtherSessions(r.Context(), user.ID, currentHash)
		if err != nil {
			http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
			return
		}
		a.audit(r.Context(), &user.ID, "user_sessions_revoked", "session", nil, a.clientIP(r), r.UserAgent(), map[string]any{"count": revoked})
		http.Redirect(w, r, "/account/sessions?status=others_revoked", http.StatusFound)
	case "revoke":
		id, err := strconv.ParseInt(r.Form.Get("session_id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "неверная сессия", http.StatusBadRequest)
			return
		}
		revoked, err := a.store.RevokeOwnedSession(r.Context(), id, user.ID, currentHash)
		if err != nil {
			http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
			return
		}
		if !revoked {
			a.renderAccountSessions(w, r, user.ID, currentHash, "Текущую сессию нельзя завершить на этой странице.")
			return
		}
		auditID := id
		a.audit(r.Context(), &user.ID, "user_session_revoked", "session", &auditID, a.clientIP(r), r.UserAgent(), nil)
		http.Redirect(w, r, "/account/sessions?status=revoked", http.StatusFound)
	default:
		http.Error(w, "неверное действие", http.StatusBadRequest)
	}
}

func (a *App) renderAccountSessions(w http.ResponseWriter, r *http.Request, userID int64, currentHash, message string) {
	sessions, err := a.store.ListAccountSessions(r.Context(), userID, currentHash, a.cfg.SessionIdleTimeout)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}

	status := r.URL.Query().Get("status")
	if status == "revoked" {
		message = "Сессия завершена."
	}
	if status == "others_revoked" {
		message = "Все остальные сессии завершены."
	}
	a.render(w, r, "account_sessions", map[string]any{
		"Title":    "Активные сессии",
		"Sessions": sessions,
		"Message":  message,
	})
}
