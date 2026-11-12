package main

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
	"ai-access-gateway/internal/store"
)

func registerSecurityListsPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, now time.Time) {
	now = now.UTC()
	auditRows := func(q, scenario string) []store.AdminAuditRow {
		n := 3
		if strings.Contains(q, "bulk") {
			n = 267
		}
		rows := make([]store.AdminAuditRow, 0, n)
		for i := 0; i < n; i++ {
			action, actor, target := "user_service_access_updated", "admin", "user"
			if i%3 == 1 {
				action, actor, target = "session_revoked", "operator", "session"
			}
			if i%3 == 2 {
				action, actor, target = "temporary_users_cleanup", "", "system"
			}
			if n > 3 {
				actor = "bulk-operator"
			}
			row := store.AdminAuditRow{AuditRow: domain.AuditRow{ID: int64(400 - i), Actor: actor, Action: action, TargetType: target, TargetID: sql.NullInt64{Int64: 2, Valid: true}, IP: "192.168.1.86", UserAgent: "Chrome 139 / Windows 11", CreatedAt: now.Add(-time.Duration(i+1) * time.Minute), Metadata: "{\"enabled\":true,\"job_id\":9007199254740993}"}, RequestID: fmt.Sprintf("request-%03d", i), CorrelationID: "trace_preview_security"}
			if scenario == "long" {
				row.UserAgent = strings.Repeat("LongDeviceWithoutSpaces", 35)
				row.Metadata = "{\"detail\":\"<script>never()</script> " + strings.Repeat("diagnostic", 35) + "\"}"
				row.Actor = strings.Repeat("ОченьДлинноеИмя", 12)
			}
			rows = append(rows, row)
		}
		return rows
	}
	audit := func(w http.ResponseWriter, r *http.Request) {
		f, err := gateway.ParseAdminAuditFilter(r.URL.Query(), now)
		message := ""
		if err != nil {
			f, _ = gateway.ParseAdminAuditFilter(nil, now)
			message = "Проверьте даты, параметры и длину поиска (до 120 символов)."
		}
		scenario := r.URL.Query().Get("scenario")
		rows := auditRows(f.Search, scenario)
		p := store.AdminAuditPage{Filter: f, Actions: []string{"user_service_access_updated", "session_revoked", "temporary_users_cleanup"}, Targets: []string{"user", "session", "system"}}
		for _, row := range rows {
			match := strings.ToLower(row.Actor + " " + row.Action + " " + row.TargetType + " " + row.IP + " " + row.UserAgent + " " + row.Metadata)
			if !row.CreatedAt.Before(f.Before) || (f.From != nil && row.CreatedAt.Before(*f.From)) || (f.Until != nil && !row.CreatedAt.Before(*f.Until)) || !strings.Contains(match, strings.ToLower(f.Search)) || (f.Action != "" && row.Action != f.Action) || (f.Target != "" && row.TargetType != f.Target) || scenario == "empty" {
				continue
			}
			p.Items = append(p.Items, row)
		}
		if f.Sort == "oldest" {
			sort.Slice(p.Items, func(i, j int) bool { return p.Items[i].ID < p.Items[j].ID })
		}
		p.AdminSecurityPage = previewSecurityPage(len(p.Items), f.Offset, now)
		p.Filter.Offset = p.Offset
		p.Items = p.Items[p.Offset:min(len(p.Items), p.Offset+store.AdminSecurityPageSize)]
		if scenario == "error" {
			message = "Не удалось загрузить журнал. Повторите запрос."
		}
		render("admin_audit", "Журнал аудита", map[string]any{"Audit": gateway.BuildAdminAuditList(p), "SecurityError": message})(w, r)
	}
	sessions := func(w http.ResponseWriter, r *http.Request) {
		f, err := gateway.ParseAdminSessionFilter(r.URL.Query())
		message, loadError := "", ""
		if err != nil {
			f, _ = gateway.ParseAdminSessionFilter(nil)
			loadError = "Проверьте параметры поиска."
		}
		scenario := r.URL.Query().Get("scenario")
		if r.URL.Query().Get("status") == "revoked" {
			message = "Сессия завершена, связанные подключения закрыты."
		}
		p := store.AdminSessionPage{Filter: f}
		n := 5
		if strings.Contains(f.Search, "bulk") {
			n = 237
		}
		for i := 0; i < n; i++ {
			name, state := "admin", "valid"
			if i == 1 {
				name = "rayka"
			}
			if i == 2 {
				name = "studio"
			}
			if i == 3 {
				name, state = "former-user", "account_expired"
			}
			if i == 4 {
				name, state = "disabled-user", "disabled"
			}
			if n > 5 {
				name = "bulk-user"
				state = "valid"
			}
			row := store.AdminSessionRow{SessionRow: domain.SessionRow{ID: int64(71 + i), Username: name, IP: fmt.Sprintf("192.168.1.%d", 24+i), UserAgent: "Chrome Mobile / Android 15", CreatedAt: now.Add(-8 * time.Hour), LastSeenAt: now.Add(-time.Duration(i+1) * time.Minute), ExpiresAt: now.Add(22 * time.Hour)}, UserID: int64(i + 1), Current: i == 0, State: state}
			if scenario == "long" {
				row.Username = strings.Repeat("ОченьДлинноеИмя", 14)
				row.UserAgent = strings.Repeat("LongDeviceWithoutSpaces", 30)
				row.IP = "2001:db8:85a3:0000:0000:8a2e:0370:7334"
			}
			if scenario == "empty" || !strings.Contains(strings.ToLower(row.Username+" "+row.IP+" "+row.UserAgent), strings.ToLower(f.Search)) || (f.State != "all" && ((f.State == "valid") != (state == "valid"))) {
				continue
			}
			p.Items = append(p.Items, row)
		}
		if f.Sort == "oldest" {
			sort.Slice(p.Items, func(i, j int) bool { return p.Items[i].ID > p.Items[j].ID })
		}
		p.AdminSecurityPage = previewSecurityPage(len(p.Items), f.Offset, now)
		p.Filter.Offset = p.Offset
		p.Items = p.Items[p.Offset:min(len(p.Items), p.Offset+store.AdminSecurityPageSize)]
		if scenario == "error" {
			loadError = "Не удалось загрузить сессии. Повторите запрос."
		}
		if scenario == "uncertain" {
			message = "Результат отзыва не подтверждён. Обновите список перед повторной попыткой."
		}
		render("admin_sessions", "Сессии", map[string]any{"SessionList": gateway.BuildAdminSessionList(p, 6*time.Hour), "SecurityError": loadError, "SecurityMessage": message, "SecurityUncertain": scenario == "uncertain"})(w, r)
	}
	mux.HandleFunc("/preview/audit", audit)
	mux.HandleFunc("/admin/audit", audit)
	mux.HandleFunc("/preview/admin-sessions", sessions)
	mux.HandleFunc("/admin/sessions", sessions)
	mux.HandleFunc("/admin/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		if err := r.ParseForm(); err != nil || r.PostForm.Get("confirm_revoke") != "1" {
			http.Error(w, "confirmation required", 400)
			return
		}
		q := r.URL.Query()
		q.Set("status", "revoked")
		http.Redirect(w, r, "/admin/sessions?"+q.Encode(), http.StatusSeeOther)
	})
	mux.HandleFunc("/admin/audit/export", func(w http.ResponseWriter, r *http.Request) {
		f, err := gateway.ParseAdminAuditFilter(url.Values{"before": r.URL.Query()["before"]}, now)
		if err != nil {
			http.Error(w, "boundary", 400)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=ai-gateway-audit-preview.csv")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "HEAD" {
			return
		}
		w.Write([]byte{0xef, 0xbb, 0xbf})
		csvw := csv.NewWriter(w)
		defer csvw.Flush()
		csvw.Write([]string{"ID", "Время", "Инициатор", "Действие", "Тип объекта", "ID объекта", "IP", "User-Agent", "Метаданные"})
		for _, row := range auditRows("", "") {
			if row.CreatedAt.Before(f.Before) {
				csvw.Write([]string{strconv.FormatInt(row.ID, 10), row.CreatedAt.Format(time.RFC3339), row.Actor, row.Action, row.TargetType, "2", row.IP, row.UserAgent, row.Metadata})
			}
		}
	})
}

func previewSecurityPage(total, offset int, now time.Time) store.AdminSecurityPage {
	if total == 0 {
		offset = 0
	} else if offset >= total {
		offset = ((total - 1) / store.AdminSecurityPageSize) * store.AdminSecurityPageSize
	}
	return store.AdminSecurityPage{Total: int64(total), Offset: offset, At: now}
}
