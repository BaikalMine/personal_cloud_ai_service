package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func securityListOffset(values url.Values, keys ...string) (int, error) {
	for _, key := range append(keys, "offset") {
		if len(values[key]) > 1 {
			return 0, store.ErrAdminSecurityFilter
		}
	}
	if raw := values.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || strconv.Itoa(n) != raw {
			return 0, store.ErrAdminSecurityFilter
		}
		return n, nil
	}
	return 0, nil
}

func ParseAdminAuditFilter(values url.Values, now time.Time) (store.AdminAuditFilter, error) {
	f := store.AdminAuditFilter{Search: values.Get("q"), Action: values.Get("action"), Target: values.Get("target"), Sort: values.Get("sort"), Before: now.UTC()}
	var err error
	if f.Offset, err = securityListOffset(values, "q", "action", "target", "sort", "from", "to", "before"); err != nil {
		return f, err
	}
	if raw := values.Get("before"); raw != "" {
		if f.Before, err = time.Parse(time.RFC3339Nano, raw); err != nil || f.Before.After(now.Add(time.Minute)) {
			return f, store.ErrAdminSecurityFilter
		}
		f.Before = f.Before.UTC()
	}
	for key, dest := range map[string]**time.Time{"from": &f.From, "to": &f.Until} {
		if raw := values.Get(key); raw != "" {
			v, err := time.Parse(time.DateOnly, raw)
			if err != nil {
				return f, store.ErrAdminSecurityFilter
			}
			if key == "to" {
				v = v.AddDate(0, 0, 1)
			}
			*dest = &v
		}
	}
	return f.Normalize()
}

func ParseAdminSessionFilter(values url.Values) (store.AdminSessionFilter, error) {
	f := store.AdminSessionFilter{Search: values.Get("q"), State: values.Get("state"), Sort: values.Get("sort")}
	var err error
	if f.Offset, err = securityListOffset(values, "q", "state", "sort"); err != nil {
		return f, err
	}
	return f.Normalize()
}

func adminAuditListValues(f store.AdminAuditFilter) url.Values {
	v := url.Values{"before": {f.Before.UTC().Format(time.RFC3339Nano)}}
	for key, value := range map[string]string{"q": f.Search, "action": f.Action, "target": f.Target, "sort": f.Sort} {
		if value != "" {
			v.Set(key, value)
		}
	}
	if f.From != nil {
		v.Set("from", f.From.UTC().Format(time.DateOnly))
	}
	if f.Until != nil {
		v.Set("to", f.Until.UTC().AddDate(0, 0, -1).Format(time.DateOnly))
	}
	return v
}

func adminSessionListValues(f store.AdminSessionFilter) url.Values {
	v := url.Values{}
	for key, value := range map[string]string{"q": f.Search, "state": f.State, "sort": f.Sort} {
		if value != "" {
			v.Set(key, value)
		}
	}
	return v
}

type securityListPageView struct {
	store.AdminSecurityPage
	First, Last, PageNumber, PageCount                                        int64
	CurrentURL, RefreshURL, ResetURL, PreviousURL, NextURL, FirstURL, LastURL string
}

func buildSecurityListPage(p store.AdminSecurityPage, path string, values url.Values, size int) securityListPageView {
	v := securityListPageView{AdminSecurityPage: p, ResetURL: path}
	link := func(offset int) string {
		q := values.Encode()
		params, _ := url.ParseQuery(q)
		if offset > 0 {
			params.Set("offset", strconv.Itoa(offset))
		} else {
			params.Del("offset")
		}
		return path + "?" + params.Encode()
	}
	v.CurrentURL = link(p.Offset)
	v.RefreshURL = v.CurrentURL
	if path == "/admin/audit" {
		fresh, _ := url.Parse(v.CurrentURL)
		q := fresh.Query()
		q.Del("before")
		q.Del("offset")
		fresh.RawQuery = q.Encode()
		v.RefreshURL = fresh.String()
	}
	if p.Total > 0 {
		v.First, v.Last = int64(p.Offset)+1, int64(p.Offset+size)
		v.PageNumber, v.PageCount = int64(p.Offset/store.AdminSecurityPageSize)+1, (p.Total+store.AdminSecurityPageSize-1)/store.AdminSecurityPageSize
	}
	if p.Offset > 0 {
		v.PreviousURL = link(max(0, p.Offset-store.AdminSecurityPageSize)) + "#security-results"
		v.FirstURL = link(0) + "#security-results"
	}
	if v.Last < p.Total {
		v.NextURL = link(p.Offset+store.AdminSecurityPageSize) + "#security-results"
		v.LastURL = link(int((p.Total-1)/store.AdminSecurityPageSize)*store.AdminSecurityPageSize) + "#security-results"
	}
	return v
}

type securityChoice struct{ Value, Label string }
type adminAuditRowView struct {
	store.AdminAuditRow
	MetadataJSON string
}
type adminAuditListView struct {
	securityListPageView
	Filter                      store.AdminAuditFilter
	FromText, ToText, ExportURL string
	Items                       []adminAuditRowView
	Actions, Targets            []securityChoice
}

func BuildAdminAuditList(p store.AdminAuditPage) adminAuditListView {
	v := adminAuditListView{securityListPageView: buildSecurityListPage(p.AdminSecurityPage, "/admin/audit", adminAuditListValues(p.Filter), len(p.Items)), Filter: p.Filter}
	if p.Filter.From != nil {
		v.FromText = p.Filter.From.UTC().Format(time.DateOnly)
	}
	if p.Filter.Until != nil {
		v.ToText = p.Filter.Until.UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	}
	v.ExportURL = "/admin/audit/export?" + url.Values{"before": {p.Filter.Before.UTC().Format(time.RFC3339Nano)}}.Encode()
	choices := func(items []string, selected string, label func(string) string) []securityChoice {
		seen := map[string]bool{}
		out := []securityChoice{}
		for _, value := range append(append([]string{}, items...), selected) {
			if value != "" && !seen[value] {
				out = append(out, securityChoice{value, label(value)})
				seen[value] = true
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
		return out
	}
	v.Actions, v.Targets = choices(p.Actions, p.Filter.Action, auditActionLabel), choices(p.Targets, p.Filter.Target, auditTargetLabel)
	for _, row := range p.Items {
		var pretty bytes.Buffer
		metadata := row.Metadata
		if json.Indent(&pretty, []byte(metadata), "", "  ") == nil {
			metadata = pretty.String()
		}
		v.Items = append(v.Items, adminAuditRowView{row, metadata})
	}
	return v
}

type adminSessionRowView struct {
	store.AdminSessionRow
	StateLabel, RevokeURL, Confirmation string
	IdleExpiresAt                       time.Time
}
type adminSessionListView struct {
	securityListPageView
	Filter      store.AdminSessionFilter
	Items       []adminSessionRowView
	IdleTimeout time.Duration
}

func BuildAdminSessionList(p store.AdminSessionPage, idle time.Duration) adminSessionListView {
	v := adminSessionListView{securityListPageView: buildSecurityListPage(p.AdminSecurityPage, "/admin/sessions", adminSessionListValues(p.Filter), len(p.Items)), Filter: p.Filter, IdleTimeout: idle}
	for _, row := range p.Items {
		item := adminSessionRowView{AdminSessionRow: row}
		item.IdleExpiresAt = row.LastSeenAt.Add(idle)
		item.StateLabel = map[string]string{"valid": "Действует", "expired": "Срок сессии истёк", "idle": "Истёк срок бездействия", "disabled": "Аккаунт отключён", "account_expired": "Срок аккаунта истёк"}[row.State]
		if item.StateLabel == "" {
			item.StateLabel = "Состояние не подтверждено"
		}
		link, _ := url.Parse(v.CurrentURL)
		link.Path = fmt.Sprintf("/admin/sessions/%d/revoke", row.ID)
		item.RevokeURL = link.String()
		item.Confirmation = fmt.Sprintf("Завершить сессию #%d пользователя %s (%s)? Связанные подключения будут закрыты.", row.ID, row.Username, row.IP)
		if row.Current {
			item.Confirmation += " Это ваша текущая сессия. Потребуется войти заново."
		}
		v.Items = append(v.Items, item)
	}
	return v
}

func (a *App) adminSecurityRead(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if user := a.currentUser(r); user == nil || user.Role != "admin" {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func (a *App) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	if !a.adminSecurityRead(w, r) {
		return
	}
	filter, err := ParseAdminAuditFilter(r.URL.Query(), time.Now())
	if err != nil {
		filter, _ = ParseAdminAuditFilter(nil, time.Now())
		a.renderStatus(w, r, http.StatusBadRequest, "admin_audit", map[string]any{"Title": "Журнал", "Audit": BuildAdminAuditList(store.AdminAuditPage{Filter: filter}), "SecurityError": "Проверьте даты, параметры и длину поиска (до 120 символов)."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	page, err := a.store.ListAdminAuditPage(ctx, filter)
	status, message := http.StatusOK, ""
	if err != nil {
		page = store.AdminAuditPage{Filter: filter}
		status, message = http.StatusServiceUnavailable, "Не удалось загрузить журнал. Повторите запрос."
	}
	a.renderStatus(w, r, status, "admin_audit", map[string]any{"Title": "Журнал", "Audit": BuildAdminAuditList(page), "SecurityError": message})
}

func (a *App) currentSessionHash(r *http.Request) string {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		return security.HashToken(cookie.Value)
	}
	return ""
}

func (a *App) renderAdminSessionList(w http.ResponseWriter, r *http.Request, filter store.AdminSessionFilter, status int, message string) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	page, err := a.store.ListAdminSessionsPage(ctx, filter, a.currentSessionHash(r), a.cfg.SessionIdleTimeout)
	loadError := ""
	if err != nil {
		page = store.AdminSessionPage{Filter: filter}
		status, loadError = http.StatusServiceUnavailable, "Не удалось загрузить сессии. Повторите запрос."
	}
	a.renderStatus(w, r, status, "admin_sessions", map[string]any{"Title": "Сессии", "SessionList": BuildAdminSessionList(page, a.cfg.SessionIdleTimeout), "SecurityError": loadError, "SecurityMessage": message, "SecurityValidation": status == http.StatusBadRequest, "SecurityUncertain": status == http.StatusServiceUnavailable && message != ""})
}

func (a *App) handleAdminSessions(w http.ResponseWriter, r *http.Request) {
	if !a.adminSecurityRead(w, r) {
		return
	}
	filter, err := ParseAdminSessionFilter(r.URL.Query())
	if err != nil {
		filter, _ = ParseAdminSessionFilter(nil)
		a.renderStatus(w, r, http.StatusBadRequest, "admin_sessions", map[string]any{"Title": "Сессии", "SessionList": BuildAdminSessionList(store.AdminSessionPage{Filter: filter}, a.cfg.SessionIdleTimeout), "SecurityError": "Проверьте параметры и длину поиска (до 120 символов)."})
		return
	}
	message := ""
	switch r.URL.Query().Get("status") {
	case "revoked":
		message = "Сессия завершена, связанные подключения закрыты."
	case "missing":
		message = "Сессия уже завершена или отсутствует."
	}
	a.renderAdminSessionList(w, r, filter, http.StatusOK, message)
}

func (a *App) handleAdminSessionAction(w http.ResponseWriter, r *http.Request, rest string) {
	w.Header().Set("Cache-Control", "no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "revoke" {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[0] {
		http.NotFound(w, r)
		return
	}
	filter, err := ParseAdminSessionFilter(r.URL.Query())
	if err != nil {
		http.Error(w, "неверные параметры возврата", http.StatusBadRequest)
		return
	}
	if len(r.PostForm["confirm_revoke"]) != 1 || r.PostForm.Get("confirm_revoke") != "1" {
		a.renderAdminSessionList(w, r, filter, http.StatusBadRequest, "Подтвердите завершение выбранной сессии.")
		return
	}
	hash, revoked, err := a.store.RevokeAdminSession(r.Context(), id)
	if err != nil {
		a.renderAdminSessionList(w, r, filter, http.StatusServiceUnavailable, "Результат отзыва не подтверждён. Обновите список перед повторной попыткой.")
		return
	}
	if revoked {
		a.closeSessionWebSockets(hash)
		a.audit(r.Context(), &actor.ID, "session_revoked", "session", &id, a.clientIP(r), r.UserAgent(), nil)
		if hash == a.currentSessionHash(r) {
			http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.cookieSecure(r)})
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
	}
	back, _ := url.Parse(buildSecurityListPage(store.AdminSecurityPage{Offset: filter.Offset}, "/admin/sessions", adminSessionListValues(filter), 0).CurrentURL)
	q := back.Query()
	q.Set("status", "missing")
	if revoked {
		q.Set("status", "revoked")
	}
	back.RawQuery = q.Encode()
	http.Redirect(w, r, back.String()+"#security-results", http.StatusSeeOther)
}
