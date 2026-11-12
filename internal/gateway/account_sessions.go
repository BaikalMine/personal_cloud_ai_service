package gateway

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type accountSessionRow struct {
	domain.AccountSession
	IdleExpiresAt, EffectiveExpiresAt time.Time
}

func BuildAccountSessionRows(items []domain.AccountSession, idle time.Duration, accountExpiry time.Time) []accountSessionRow {
	rows := make([]accountSessionRow, 0, len(items))
	for _, item := range items {
		row := accountSessionRow{AccountSession: item, IdleExpiresAt: item.LastSeenAt.Add(idle), EffectiveExpiresAt: item.ExpiresAt}
		if row.IdleExpiresAt.Before(row.EffectiveExpiresAt) {
			row.EffectiveExpiresAt = row.IdleExpiresAt
		}
		if !accountExpiry.IsZero() && accountExpiry.Before(row.EffectiveExpiresAt) {
			row.EffectiveExpiresAt = accountExpiry
		}
		rows = append(rows, row)
	}
	return rows
}

func (a *App) handleAccountSessions(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	currentHash := a.currentSessionHash(r)
	if currentHash == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	filter, err := ParseAccountSessionFilter(r.URL.Query())
	if err != nil {
		a.renderAccountSessions(w, r, user.ID, currentHash, 400, map[string]any{"Error": "Параметры списка не распознаны. Установите фильтры заново."})
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		a.renderAccountSessions(w, r, user.ID, currentHash, 200, nil)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "метод не поддерживается", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !a.validCSRF(r) {
		a.renderAccountSessions(w, r, user.ID, currentHash, 403, map[string]any{"Error": "Не удалось подтвердить запрос. Обновите список.", "WriteUncertain": true})
		return
	}
	if len(r.PostForm["confirm_revoke"]) != 1 || r.PostForm.Get("confirm_revoke") != "1" {
		a.renderAccountSessions(w, r, user.ID, currentHash, 400, map[string]any{"Error": "Подтвердите завершение сессии."})
		return
	}
	fail := func() {
		a.renderAccountSessions(w, r, user.ID, currentHash, 503, map[string]any{"Error": "Результат завершения не подтверждён. Обновите список перед повторной попыткой.", "WriteUncertain": true})
	}
	status := ""
	if len(r.PostForm["action"]) != 1 {
		http.Error(w, "неверное действие", 400)
		return
	}
	switch r.PostForm.Get("action") {
	case "revoke_others":
		revoked, err := a.store.RevokeOtherSessions(r.Context(), user.ID, currentHash)
		if err != nil {
			fail()
			return
		}
		a.closeOtherUserWebSockets(user.ID, currentHash)
		a.audit(r.Context(), &user.ID, "user_sessions_revoked", "session", nil, a.clientIP(r), r.UserAgent(), map[string]any{"count": revoked})
		status = "others_revoked"
	case "revoke":
		id, err := strconv.ParseInt(r.PostForm.Get("session_id"), 10, 64)
		if err != nil || id <= 0 || len(r.PostForm["session_id"]) != 1 {
			a.renderAccountSessions(w, r, user.ID, currentHash, 400, map[string]any{"Error": "Сессия не распознана. Обновите список."})
			return
		}
		hash, revoked, err := a.store.RevokeOwnedSessionWithHash(r.Context(), id, user.ID, currentHash)
		if err != nil {
			fail()
			return
		}
		if !revoked {
			a.renderAccountSessions(w, r, user.ID, currentHash, 409, map[string]any{"Error": "Эта сессия уже завершена, недоступна или является текущей. Обновите список."})
			return
		}
		a.closeSessionWebSockets(hash)
		a.audit(r.Context(), &user.ID, "user_session_revoked", "session", &id, a.clientIP(r), r.UserAgent(), nil)
		status = "revoked"
	default:
		a.renderAccountSessions(w, r, user.ID, currentHash, 400, map[string]any{"Error": "Действие не распознано. Обновите список."})
		return
	}
	target, _ := url.Parse(AccountSessionURL(filter, filter.Offset))
	query := target.Query()
	query.Set("status", status)
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (a *App) renderAccountSessions(w http.ResponseWriter, r *http.Request, userID int64, currentHash string, status int, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	filter, err := ParseAccountSessionFilter(r.URL.Query())
	if err != nil {
		filter, _ = (store.UserProfileFilter{}).Normalize(true)
	}
	page, err := a.store.ListUserSessionsPage(r.Context(), userID, currentHash, a.cfg.SessionIdleTimeout, filter)
	data["Title"], data["Filter"] = "Активные сессии", filter
	if err != nil {
		data["SessionLoadError"] = true
		if status == 200 {
			status = 503
		}
	} else {
		expiry := time.Time{}
		if user := a.currentUser(r); user != nil && user.AccountExpiresAt.Valid {
			expiry = user.AccountExpiresAt.Time
		}
		data["Sessions"], data["Filter"] = BuildAccountSessionRows(page.Items, a.cfg.SessionIdleTimeout, expiry), page.Filter
		data["Pagination"], data["SnapshotAt"] = BuildAccountSessionPagination(page.UserProfilePage, len(page.Items)), page.Now
	}
	if data["Error"] == nil {
		switch r.URL.Query().Get("status") {
		case "revoked":
			data["Success"] = "Сессия завершена."
		case "others_revoked":
			data["Success"] = "Все остальные сессии завершены."
		}
	}
	a.renderAccountPage(w, r, status, "account_sessions", "sessions", data)
}
