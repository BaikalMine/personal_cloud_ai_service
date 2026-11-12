package gateway

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestAccessListViewsAndURL(t *testing.T) {
	now := time.Now()
	f := store.AccessListFilter{Search: "a%_&b", State: "active", Access: "video", Sort: "expires", AccountType: "temporary", ExpiringSoon: true, Offset: 50}
	parsed, err := url.Parse(accessListURL(f, false, 50))
	if err != nil || parsed.Host != "" || parsed.Path != "/admin/users" {
		t.Fatal(parsed, err)
	}
	again, err := ParseAccessListFilter(parsed.Query(), false)
	if err != nil || again != f {
		t.Fatal(again, err)
	}
	for _, q := range []string{"offset=-1", "offset=01", "offset=1e2", "q=a&q=b", "sort=name&sort=expires", "soon=on", "state=revoked", "access=admin", "sort=invalid"} {
		v, _ := url.ParseQuery(q)
		if _, err := ParseAccessListFilter(v, false); err == nil {
			t.Fatal("accepted", q)
		}
	}
	u := domain.User{ID: 7, CanUseQuickGeneration: true, CanGenerateTextToImage: true, CanGenerateVideo: true, MaxVideoGenerationQuality: 720, GenerationDailyLimit: 12, GenerationTotalLimit: 50, GenerationTotalUsed: 17, VideoGenerationDailyLimit: 2, VideoGenerationTotalLimit: 8, VideoGenerationTotalUsed: 10}
	p := store.UserAccessPage{AccessListPage: store.AccessListPage{Filter: f, Total: 125, Now: now}, Items: []store.UserAccessRow{{User: u, ImageUsedToday: 3, VideoUsedToday: 1}}}
	v := BuildUserAccessList(p)
	if v.First != 51 || v.Last != 51 || v.NextURL == "" || v.PreviousURL == "" || len(v.Users[0].Quotas) != 2 {
		t.Fatal(v)
	}
	if v.Users[0].Quotas[0].Daily != "Осталось 9 из 12 сегодня" || v.Users[0].Quotas[0].Total != "Осталось 33 из 50 всего" || v.Users[0].Quotas[1].Total != "Осталось 0 из 8 всего" {
		t.Fatal(v.Users[0].Quotas)
	}
	p.Items[0].Disabled = true
	v = BuildUserAccessList(p)
	if v.Users[0].AccessLabel != "Нет доступа" || len(v.Users[0].Quotas) != 0 || v.Users[0].State != "disabled" {
		t.Fatal(v.Users[0])
	}
	p.Items[0].Disabled = false
	p.Items[0].AccountExpiresAt = sql.NullTime{Time: now, Valid: true}
	if BuildUserAccessList(p).Users[0].State != "expired" {
		t.Fatal("expiry not shown")
	}
	i := domain.InviteRow{ID: 9007199254741009, GrantOpenWebUI: true, GenerationDailyLimit: 1, Revoked: true, ExpiresAt: now.Add(-time.Hour), MaxUses: 1}
	ip := store.InviteAccessPage{AccessListPage: store.AccessListPage{Filter: f, Total: 1, Now: now}, Items: []domain.InviteRow{i}}
	iv := BuildInviteAccessList(ip).Invites[0]
	if len(iv.Quotas) != 0 || iv.RestoreLabel != "Снять отзыв" || iv.AccessLabel != "Нет доступа" {
		t.Fatal(iv)
	}
	action, err := url.Parse(iv.RevokeURL)
	if err != nil || action.Path != "/admin/invites/9007199254741009/unrevoke" || action.Query().Get("q") != f.Search {
		t.Fatal(iv.RevokeURL, err)
	}
}

func TestAccessListHTTPIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(username,password_hash,role,can_use_quick_generation,can_generate_text_to_image,generation_daily_limit,generation_total_limit,generation_total_used)
 SELECT 'page-'||lpad(n::text,3,'0'),'hash',CASE WHEN n=1 THEN 'admin' ELSE 'user' END,true,true,12,50,17 FROM generate_series(1,65)n`)
	var adminID int64
	if err = db.QueryRow(`SELECT id FROM users WHERE username='page-001'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO invites(token_hash,created_by_user_id,max_uses,expires_at,grant_openwebui) SELECT 'page-invite-'||n,$1,1,now()+interval '1 day',true FROM generate_series(1,65)n`, adminID)
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store.New(db), tpl: tpl, csrfSigner: security.NewCSRFSigner("access-list-test")}
	h := a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes))
	request := func(method, target, role string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		u, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, u.RequestURI(), strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "list-session"})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: adminID, Role: role}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/admin/users?sort=name&offset=50", "/admin/invites?state=active&offset=50"} {
		w := request("GET", path, "admin", nil)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Count(w.Body.String(), `data-access-row=`) != 15 || !strings.Contains(w.Body.String(), "51–65 из 65") {
			t.Fatalf("page %s %d %s", path, w.Code, w.Body.String())
		}
		if w := request("GET", path, "user", nil); w.Code != 403 {
			t.Fatal("nonadmin list", w.Code)
		}
	}
	for _, path := range []string{"/admin/users?q=missing&offset=500", "/admin/invites?q=missing&offset=500"} {
		w := request("GET", path, "admin", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "0 результатов") || strings.Contains(w.Body.String(), `data-access-row=`) {
			t.Fatal("empty page", w.Code)
		}
	}
	if w := request("GET", "/admin/invites?access=none&offset=50", "admin", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "51–65 из 65") {
		t.Fatal("retired grants incorrectly count as active invitation access", w.Code)
	}
	for _, path := range []string{"/admin/users?offset=-1", "/admin/invites?state=active&state=revoked", "/admin/users?sort=arbitrary"} {
		if w := request("GET", path, "admin", nil); w.Code != 400 {
			t.Fatal("bad filter", path, w.Code)
		}
	}
	var inviteID int64
	if err = db.QueryRow(`SELECT id FROM invites ORDER BY id LIMIT 1`).Scan(&inviteID); err != nil {
		t.Fatal(err)
	}
	base := "/admin/invites/" + strconv.FormatInt(inviteID, 10)
	form := url.Values{"csrf": {a.csrfSigner.Token("list-session")}}
	if w := request("POST", base+"/revoke?state=active&offset=50", "admin", nil); w.Code != 403 {
		t.Fatal("CSRF bypass", w.Code)
	}
	w := request("POST", base+"/revoke?state=active&offset=50", "admin", form)
	if w.Code != 302 || w.Header().Get("Location") != "/admin/invites?offset=50&state=active#access-results" {
		t.Fatal("redirect lost context", w.Code, w.Header())
	}
	w = request("GET", w.Header().Get("Location"), "admin", nil)
	if !strings.Contains(w.Body.String(), "51–64 из 64") {
		t.Fatal("mutated count not refreshed")
	}
	w = request("POST", base+"/delete?q=page-001&offset=500", "admin", form)
	if w.Code != 302 {
		t.Fatal("delete", w.Code, w.Body.String())
	}
	w = request("GET", w.Header().Get("Location"), "admin", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "51–64 из 64") {
		t.Fatal("deleted last page clamp", w.Code, w.Body.String())
	}
	// Simulate an independent list read failure after a creating operation has
	// already produced a token. The page must not discard that token or retry.
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/admin/invites", nil).WithContext(context.WithValue(ctx, userCtxKey, &User{ID: adminID, Role: "admin"}))
	w = httptest.NewRecorder()
	a.renderAdminInvites(w, r, map[string]any{"Title": "Приглашения", "InviteLink": "https://example.test/invite/already-created"})
	if !strings.Contains(w.Body.String(), "already-created") || !strings.Contains(w.Body.String(), "Список приглашений не загрузился") || strings.Contains(w.Body.String(), "0 результатов") {
		t.Fatal("read failure discarded issued link", w.Body.String())
	}
}
