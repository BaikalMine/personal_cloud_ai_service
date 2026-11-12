package gateway

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestAdminSecurityFiltersAndLinks(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for _, query := range []string{"q=a&q=b", "offset=-1", "offset=01", "offset=1000000001", "sort=invalid", "from=2026-02-30", "from=2026-09-15&to=2026-09-14", "before=2026-09-14T13:00:00Z", "action=" + strings.Repeat("a", 129), "q=%00"} {
		values, _ := url.ParseQuery(query)
		if _, err := ParseAdminAuditFilter(values, now); err == nil {
			t.Fatal("accepted audit filter", query)
		}
	}
	for _, query := range []string{"state=bad", "state=valid&state=all", "q=" + strings.Repeat("x", 121), "offset=+1", "sort=oldest&sort=newest"} {
		values, _ := url.ParseQuery(query)
		if _, err := ParseAdminSessionFilter(values); err == nil {
			t.Fatal("accepted session filter", query)
		}
	}
	values := url.Values{"q": {"  Кириллица %_  "}, "from": {"2026-09-12"}, "to": {"2026-09-12"}, "sort": {"oldest"}, "offset": {"50"}}
	f, err := ParseAdminAuditFilter(values, now)
	if err != nil || f.Search != "Кириллица %_" || f.Until.Sub(*f.From) != 24*time.Hour {
		t.Fatal(f, err)
	}
	p := store.AdminAuditPage{AdminSecurityPage: store.AdminSecurityPage{Total: 267, Offset: 50, At: now}, Filter: f, Items: make([]store.AdminAuditRow, 50)}
	v := BuildAdminAuditList(p)
	if v.First != 51 || v.Last != 100 || v.PageCount != 6 || v.PageNumber != 2 {
		t.Fatal(v)
	}
	next, _ := url.Parse(v.NextURL)
	last, _ := url.Parse(v.LastURL)
	fresh, _ := url.Parse(v.RefreshURL)
	export, _ := url.Parse(v.ExportURL)
	if next.Query().Get("offset") != "100" || last.Query().Get("offset") != "250" || next.Query().Get("q") != f.Search || next.Query().Get("before") == "" || fresh.Query().Has("before") || fresh.Query().Has("offset") {
		t.Fatal(v)
	}
	if len(export.Query()) != 1 || export.Query().Get("before") == "" {
		t.Fatal("CSV must not inherit list filters", v.ExportURL)
	}
}

func TestAdminSecurityTemplatesAndAccess(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	f, _ := ParseAdminAuditFilter(nil, now)
	ap := store.AdminAuditPage{AdminSecurityPage: store.AdminSecurityPage{Total: 1, At: now}, Filter: f, Items: []store.AdminAuditRow{{AuditRow: domain.AuditRow{ID: 4, Actor: "<script>never()</script>", Action: "custom-action", TargetType: "user", Metadata: "{\"id\":9007199254740993,\"detail\":\"<img src=x>\"}"}}}}
	av := BuildAdminAuditList(ap)
	sf, _ := ParseAdminSessionFilter(url.Values{"q": {"a&b"}})
	sp := store.AdminSessionPage{AdminSecurityPage: store.AdminSecurityPage{Total: 1, At: now}, Filter: sf, Items: []store.AdminSessionRow{{SessionRow: domain.SessionRow{ID: 7, Username: "<script>never()</script>", LastSeenAt: now}, UserID: 1, Current: true, State: "valid"}}}
	sv := BuildAdminSessionList(sp, 6*time.Hour)
	if !sv.Items[0].IdleExpiresAt.Equal(now.Add(6*time.Hour)) || !strings.Contains(sv.Items[0].Confirmation, "войти заново") {
		t.Fatal(sv)
	}
	for _, tc := range []struct {
		name  string
		data  map[string]any
		wants []string
	}{
		{"admin_audit", map[string]any{"Audit": av}, []string{"9007199254740993", "&lt;script&gt;", "&lt;img", "Весь журнал CSV", "custom-action"}},
		{"admin_sessions", map[string]any{"SessionList": sv, "CSRF": "form-csrf", "SecurityUncertain": true}, []string{"confirm_revoke", "Текущая сессия", "Проверить состояние", "disabled", "&lt;script&gt;"}},
	} {
		tc.data["User"] = &User{Role: "admin"}
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, tc.name, tc.data); err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.wants {
			if !strings.Contains(out.String(), want) {
				t.Fatal(tc.name, "missing", want)
			}
		}
		if strings.Contains(out.String(), "<script>never()") {
			t.Fatal("unescaped template content")
		}
	}
	a := &App{tpl: tpl}
	for _, handler := range []http.HandlerFunc{a.handleAdminAudit, a.handleAdminSessions, a.handleAdminAuditExport} {
		for _, tc := range []struct {
			role, method string
			status       int
		}{{"", "GET", 403}, {"user", "HEAD", 403}, {"admin", "POST", 405}} {
			r := httptest.NewRequest(tc.method, "/admin/audit", nil)
			if tc.role != "" {
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{Role: tc.role}))
			}
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
	for _, handler := range []http.HandlerFunc{a.handleAdminAudit, a.handleAdminSessions} {
		r := httptest.NewRequest("GET", "/admin/list?offset=invalid", nil).WithContext(context.WithValue(context.Background(), userCtxKey, &User{Role: "admin"}))
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "role=\"alert\"") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
