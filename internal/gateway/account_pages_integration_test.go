package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestAccountPagesIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name, marker string
	if err := db.QueryRowContext(ctx, `SELECT current_database(),COALESCE(current_setting('ai_gateway.disposable_test',true),'')`).Scan(&name, &marker); err != nil {
		t.Fatal(err)
	}
	// The marker must be checked before migrations or destructive fixture setup.
	if name != "account_d09_ephemeral" || marker != "account-d09-20260915" {
		t.Skip("requires marked disposable account database")
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	defer resetGatewayIntegrationDatabase(t, db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	oldHash, err := security.HashPassword("OnlyTest-OldPass-7319")
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO users(id,username,email,password_hash,role,can_use_quick_generation,can_generate_text_to_image,generation_daily_limit,generation_total_limit) VALUES (1,'account-admin','admin@example.com',$1,'admin',true,true,0,0),(2,'account-user','user@example.com',$1,'user',true,true,12,50),(3,'account-foreign','taken@example.com',$1,'user',false,false,0,0)`, oldHash)
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	s := store.New(db)
	a := &App{store: s, tpl: tpl, csrfSigner: security.NewCSRFSigner("account-fixture"), websocketConnections: make(map[*trackedWebSocket]struct{})}
	a.cfg.SessionIdleTimeout = 6 * time.Hour
	token := "account-test-current-token"
	currentHash := security.HashToken(token)
	exec(`INSERT INTO sessions(user_id,token_hash,expires_at,last_seen_at,ip,user_agent) VALUES(2,$1,now()+interval '1 day',now(),'127.0.0.1','current-device'),(3,'foreign-private-hash',now()+interval '1 day',now(),'10.9.8.7','foreign-device')`, currentHash)
	user := func(identity string) *User {
		t.Helper()
		u, _, err := s.FindUserWithPassword(ctx, identity)
		if err != nil {
			t.Fatal(err)
		}
		return &u
	}
	request := func(app *App, method, target string, u *User, values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(values.Encode())).WithContext(context.WithValue(ctx, userCtxKey, u))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "text/html")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		w := httptest.NewRecorder()
		switch r.URL.Path {
		case "/account/profile":
			app.handleAccountProfile(w, r)
		case "/account/password":
			app.handleAccountPassword(w, r)
		case "/account/sessions":
			app.handleAccountSessions(w, r)
		case "/account/notifications":
			app.handleAccountNotifications(w, r)
		case "/account/theme":
			app.handleAccountTheme(w, r)
		case "/account/quick-generation-priority":
			app.handleAccountQuickGenerationPriority(w, r)
		case "/account/generation-mining":
			app.handleAccountGenerationMining(w, r)
		default:
			t.Fatal(target)
		}
		for _, secret := range []string{"OnlyTest-OldPass-7319", "OnlyTest-NewPass-5284", currentHash, "foreign-private-hash"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("secret echoed", target)
			}
		}
		return w
	}
	form := func(values url.Values) url.Values {
		if values == nil {
			values = url.Values{}
		}
		values.Set("csrf", a.csrfSigner.Token(token))
		return values
	}
	want := func(w *httptest.ResponseRecorder, status int, text string) {
		t.Helper()
		if w.Code != status || !strings.Contains(w.Body.String(), text) {
			t.Fatalf("status=%d want=%d text=%q body=%s", w.Code, status, text, w.Body.String())
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	regular, admin := user("account-user"), user("account-admin")
	for _, path := range []string{"/account/profile", "/account/profile?tab=notifications", "/account/profile?tab=access", "/account/password", "/account/sessions"} {
		w := request(a, "GET", path, regular, nil)
		want(w, 200, "data-account-page")
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private page is cacheable", path)
		}
		want(request(a, "PUT", path, regular, nil), 405, "метод не поддерживается")
	}
	want(request(a, "GET", "/account/profile?tab=invalid", regular, nil), 400, "Раздел настроек не найден")
	want(request(a, "POST", "/account/profile", regular, nil), 403, "Не удалось подтвердить")
	w := request(a, "POST", "/account/profile", regular, form(url.Values{"username": {"forged-admin-name"}, "email": {"Updated@Example.com"}}))
	if w.Code != 303 || w.Header().Get("Location") != "/account/profile?updated=1" {
		t.Fatal(w.Code, w.Header())
	}
	regular = user("account-user")
	if regular.Email.String != "updated@example.com" {
		t.Fatal(regular)
	}
	want(request(a, "POST", "/account/profile", regular, form(url.Values{"email": {"taken@example.com"}})), 200, `href="#profile-email"`)
	want(request(a, "POST", "/account/profile", admin, form(url.Values{"username": {"x"}, "email": {"valid@example.com"}})), 200, `href="#profile-username"`)
	want(request(a, "POST", "/account/profile", admin, form(url.Values{"username": {"account-foreign"}})), 200, `href="#profile-username"`)
	want(request(a, "POST", "/account/profile", regular, form(url.Values{"email": {"bad"}})), 200, `value="bad"`)
	want(request(a, "POST", "/account/profile", regular, form(url.Values{"email": {strings.Repeat("x", 70<<10)}})), 403, "Не удалось подтвердить")
	w = request(a, "POST", "/account/profile", regular, form(url.Values{"email": {""}}))
	if w.Code != 303 || user("account-user").Email.Valid {
		t.Fatal("email not cleared", w.Code)
	}
	w = request(a, "POST", "/account/profile", admin, form(url.Values{"username": {"account-admin-renamed"}, "email": {"admin@example.com"}}))
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	admin = user("account-admin-renamed")
	for _, theme := range []string{"system", "light", "dark"} {
		w = request(a, "POST", "/account/theme", regular, form(url.Values{"theme": {theme}}))
		if w.Code != 303 {
			t.Fatal(w.Code)
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != "ai_gateway_theme" || cookies[0].Value != theme || cookies[0].Path != "/" || cookies[0].SameSite != http.SameSiteLaxMode {
			t.Fatal(cookies)
		}
	}
	for _, values := range []url.Values{{"theme": {"bad"}}, {"theme": {"dark", "light"}}} {
		want(request(a, "POST", "/account/theme", regular, form(values)), 400, "Выберите системную")
	}
	want(request(a, "POST", "/account/theme", regular, url.Values{"theme": {"dark"}}), 403, "Не удалось подтвердить")
	for _, path := range []string{"/account/quick-generation-priority", "/account/generation-mining"} {
		want(request(a, "POST", path, regular, form(nil)), 403, "только администратору")
	}
	w = request(a, "POST", "/account/quick-generation-priority", admin, form(url.Values{"enabled": {"on"}, "return_to": {"account-access"}}))
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "tab=access") || !user(admin.Username).QueuePriority {
		t.Fatal(w.Code, w.Header())
	}
	w = request(a, "POST", "/account/quick-generation-priority", admin, form(url.Values{"return_to": {"https://external.example"}}))
	if w.Code != 303 || w.Header().Get("Location") != "/app" {
		t.Fatal(w.Code, w.Header())
	}
	w = request(a, "POST", "/account/generation-mining", admin, form(url.Values{"pause_mining_for_quick_generation": {"on"}}))
	if w.Code != 303 || !user(admin.Username).PauseMiningForQuickGeneration {
		t.Fatal(w.Code)
	}
	w = request(a, "POST", "/account/notifications", regular, form(url.Values{"in_app_enabled": {"on"}, "success_enabled": {"on"}}))
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "tab=notifications") {
		t.Fatal(w.Code, w.Header())
	}
	summary, err := s.UserNotificationSummary(ctx, regular.ID)
	if err != nil || !summary.Preferences.InAppEnabled || !summary.Preferences.SuccessEnabled {
		t.Fatal(summary, err)
	}
	jsonRequest := httptest.NewRequest("POST", "/account/notifications", strings.NewReader(form(url.Values{"browser_enabled": {"on"}}).Encode())).WithContext(context.WithValue(ctx, userCtxKey, regular))
	jsonRequest.Header.Set("Accept", "application/json")
	jsonRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	jsonRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	jsonResponse := httptest.NewRecorder()
	a.handleAccountNotifications(jsonResponse, jsonRequest)
	want(jsonResponse, 200, `"browser_enabled":false`)

	exec(`INSERT INTO sessions(user_id,token_hash,expires_at,last_seen_at,ip,user_agent) SELECT 2,'own-bulk-'||n,now()+interval '1 day',now()-interval '1 minute','192.168.1.1',CASE WHEN n=1 THEN 'literal%_device' ELSE 'browser-'||n END FROM generate_series(1,107)n`)
	seen := map[int64]bool{}
	for offset := 0; offset < 108; offset += 50 {
		page, err := s.ListUserSessionsPage(ctx, 2, currentHash, 6*time.Hour, store.UserProfileFilter{Offset: offset})
		if err != nil || page.Total != 108 || len(page.Items) != min(50, 108-offset) {
			t.Fatal(page.Total, len(page.Items), err)
		}
		for _, item := range page.Items {
			if seen[item.ID] || item.UserAgent == "foreign-device" {
				t.Fatal("duplicate/foreign", item)
			}
			seen[item.ID] = true
		}
	}
	page, err := s.ListUserSessionsPage(ctx, 2, currentHash, 6*time.Hour, store.UserProfileFilter{Search: "%_"})
	if err != nil || page.Total != 1 {
		t.Fatal(page, err)
	}
	targetID := page.Items[0].ID
	page, err = s.ListUserSessionsPage(ctx, 2, currentHash, 6*time.Hour, store.UserProfileFilter{Offset: 99999})
	if err != nil || page.Filter.Offset != 100 || len(page.Items) != 8 {
		t.Fatal(page, err)
	}
	for _, result := range []string{"current", "others"} {
		page, err := s.ListUserSessionsPage(ctx, 2, currentHash, 6*time.Hour, store.UserProfileFilter{Result: result})
		n := int64(107)
		if result == "current" {
			n = 1
		}
		if err != nil || page.Total != n {
			t.Fatal(page, err)
		}
	}
	want(request(a, "GET", "/account/sessions?q=missing", regular, nil), 200, "Подходящих сессий нет")
	want(request(a, "GET", "/account/sessions?offset=050", regular, nil), 400, "Параметры списка не распознаны")
	revoke := form(url.Values{"action": {"revoke"}, "session_id": {fmt.Sprint(targetID)}, "confirm_revoke": {"1"}})
	for _, confirmation := range [][]string{nil, {"1", "1"}, {"0"}} {
		fields := form(url.Values{"action": {"revoke"}, "session_id": {fmt.Sprint(targetID)}, "confirm_revoke": confirmation})
		want(request(a, "POST", "/account/sessions", regular, fields), 400, "Подтвердите завершение")
	}
	for _, hash := range []string{currentHash, "foreign-private-hash"} {
		var id int64
		if err := db.QueryRowContext(ctx, `SELECT id FROM sessions WHERE token_hash=$1`, hash).Scan(&id); err != nil {
			t.Fatal(err)
		}
		want(request(a, "POST", "/account/sessions", regular, form(url.Values{"action": {"revoke"}, "session_id": {fmt.Sprint(id)}, "confirm_revoke": {"1"}})), 409, "недоступна или является текущей")
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	a.registerWebSocket(2, "own-bulk-1", "comfyui", server)
	w = request(a, "POST", "/account/sessions?q=%25_&result=others&sort=oldest", regular, revoke)
	location, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != 303 || location.Query().Get("q") != "%_" || location.Query().Get("result") != "others" {
		t.Fatal(w.Code, location)
	}
	client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("socket not closed")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("socket not closed")
	}
	want(request(a, "POST", "/account/sessions", regular, revoke), 409, "уже завершена")

	password := form(url.Values{"current_password": {"OnlyTest-OldPass-7319"}, "new_password": {"OnlyTest-NewPass-5284"}, "confirm_password": {"OnlyTest-NewPass-5284"}})
	for _, tc := range []struct{ key, value, field string }{{"current_password", "incorrect", "current-password"}, {"new_password", "short", "new-password"}, {"confirm_password", "mismatch", "confirm-password"}} {
		original := password.Get(tc.key)
		password.Set(tc.key, tc.value)
		want(request(a, "POST", "/account/password", regular, password), 200, `href="#account-`+tc.field+`"`)
		password.Set(tc.key, original)
	}
	exec(`CREATE FUNCTION account_test_reject_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected session delete failure'; END $$; CREATE TRIGGER account_test_reject_delete BEFORE DELETE ON sessions FOR EACH ROW EXECUTE FUNCTION account_test_reject_delete()`)
	defer db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS account_test_reject_delete ON sessions; DROP FUNCTION IF EXISTS account_test_reject_delete()`)
	want(request(a, "POST", "/account/password", regular, password), 503, "Результат смены пароля не подтверждён")
	gotHash, err := s.PasswordHash(ctx, 2)
	if err != nil || gotHash != oldHash || count(`SELECT count(*) FROM sessions WHERE user_id=2`) != 107 {
		t.Fatal("password/revocation did not roll back", err)
	}
	want(request(a, "POST", "/account/sessions", regular, form(url.Values{"action": {"revoke_others"}, "confirm_revoke": {"1"}})), 503, "Результат завершения не подтверждён")
	exec(`DROP TRIGGER account_test_reject_delete ON sessions; DROP FUNCTION account_test_reject_delete()`)
	serverCurrent, clientCurrent := net.Pipe()
	defer serverCurrent.Close()
	defer clientCurrent.Close()
	trackedCurrent := a.registerWebSocket(2, currentHash, "comfyui", serverCurrent)
	serverOther, clientOther := net.Pipe()
	defer serverOther.Close()
	defer clientOther.Close()
	a.registerWebSocket(2, "own-bulk-2", "comfyui", serverOther)
	w = request(a, "POST", "/account/password", regular, password)
	if w.Code != 303 || w.Header().Get("Location") != "/account/password?updated=1" {
		t.Fatal(w.Code, w.Body.String())
	}
	gotHash, err = s.PasswordHash(ctx, 2)
	if err != nil || !security.VerifyPassword(gotHash, "OnlyTest-NewPass-5284") || count(`SELECT count(*) FROM sessions WHERE user_id=2`) != 1 || count(`SELECT count(*) FROM sessions WHERE user_id=3`) != 1 {
		t.Fatal("password or session scope wrong", err)
	}
	a.websocketMu.Lock()
	_, kept := a.websocketConnections[trackedCurrent]
	a.websocketMu.Unlock()
	if !kept {
		t.Fatal("current connection closed")
	}
	clientOther.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientOther.Read(make([]byte, 1)); err == nil {
		t.Fatal("other socket open")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("other socket open")
	}
	if changed, err := s.ChangeOwnPassword(ctx, 2, oldHash, "must-not-win", currentHash); err != nil || changed {
		t.Fatal("stale password change accepted", changed, err)
	}
	start := make(chan struct{})
	type passwordResult struct {
		changed bool
		err     error
	}
	results := make(chan passwordResult, 2)
	for _, candidate := range []string{"candidate-a", "candidate-b"} {
		go func() {
			<-start
			changed, err := s.ChangeOwnPassword(ctx, 3, oldHash, candidate, "foreign-private-hash")
			results <- passwordResult{changed, err}
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.changed {
			winners++
		}
	}
	if winners != 1 || count(`SELECT count(*) FROM sessions WHERE user_id=3`) != 1 {
		t.Fatal("concurrent password changes were not serialized", winners)
	}
	exec(`INSERT INTO sessions(user_id,token_hash,expires_at,last_seen_at) VALUES(2,'another-session',now()+interval '1 day',now())`)
	w = request(a, "POST", "/account/sessions?result=current", regular, form(url.Values{"action": {"revoke_others"}, "confirm_revoke": {"1"}}))
	if w.Code != 303 || count(`SELECT count(*) FROM sessions WHERE user_id=2`) != 1 || count(`SELECT count(*) FROM sessions WHERE user_id=3`) != 1 {
		t.Fatal("revoke others scope", w.Code)
	}

	brokenDB, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	brokenDB.Close()
	broken := &App{store: store.New(brokenDB), tpl: tpl, csrfSigner: a.csrfSigner, cfg: a.cfg}
	for _, tc := range []struct{ path, text string }{{"/account/profile?tab=notifications", "Не удалось загрузить настройки уведомлений"}, {"/account/profile?tab=access", "Остаток пока неизвестен"}, {"/account/sessions", "Их состояние пока неизвестно"}} {
		w := request(broken, "GET", tc.path, regular, nil)
		want(w, 503, tc.text)
		if strings.Contains(w.Body.String(), "Подходящих сессий нет") || strings.Contains(w.Body.String(), "Суточный лимит не задан") || (strings.Contains(tc.path, "notifications") && strings.Contains(w.Body.String(), "data-notification-settings")) {
			t.Fatal("unavailable data presented as known")
		}
	}
	w = request(broken, "POST", "/account/profile", regular, form(url.Values{"email": {"unsaved@example.com"}}))
	want(w, 503, `value="unsaved@example.com"`)
	want(w, 503, "disabled")
	w = request(broken, "POST", "/account/notifications", regular, form(url.Values{"in_app_enabled": {"on"}}))
	want(w, 503, "Результат сохранения не подтверждён")
	want(w, 503, "disabled")
}
