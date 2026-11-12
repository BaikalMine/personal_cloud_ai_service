package gateway

import (
	"context"
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

func TestAuthPagesIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	// Never migrate or clear an unmarked database for this destructive fixture.
	if name != "auth_d09_ephemeral" || marker != "auth-d09-20260914" {
		t.Skip("requires marked disposable auth database")
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	defer resetGatewayIntegrationDatabase(t, db)
	hash, err := security.HashPassword("integration-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,email,password_hash,role) VALUES (1,'auth-admin',NULL,$1,'admin'),(2,'taken','taken@example.com',$1,'user')`, hash); err != nil {
		t.Fatal(err)
	}
	// Explicit IDs above must not collide with registration's generated ID.
	if _, err := db.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('users','id'),2,true)`); err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store.New(db), tpl: tpl, csrfSigner: security.NewCSRFSigner("auth-test-only")}
	a.cfg.SessionTTL = time.Hour
	a.cfg.AccountLockThreshold = 20
	a.cfg.AccountLockDuration = time.Minute
	deadline := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	makeInvite := func(token string, expires time.Time) int64 {
		t.Helper()
		id, err := a.store.CreateInvite(ctx, store.CreateInviteParams{TokenHash: security.HashToken(token), CreatedByUserID: 1, MaxUses: 1, ExpiresAt: expires,
			GrantQuickGeneration: true, GrantTextToImage: true, GrantVideo: true, GrantTrainImageLora: true, QueuePriority: true,
			GenerationDailyLimit: 12, GenerationTotalLimit: 50, VideoGenerationDailyLimit: 2, VideoGenerationTotalLimit: 8, MaxVideoGenerationQuality: 720, AccountLifetimeSeconds: 7 * 24 * 3600})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	inviteID := makeInvite("active-test-token", deadline)
	request := func(method, path string, values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(values.Encode())).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		if strings.HasPrefix(path, "/invite/") {
			a.handleInvite(w, r)
		} else {
			a.handleLogin(w, r)
		}
		if strings.Contains(w.Body.String(), "integration-password") {
			t.Fatal("password echoed")
		}
		return w
	}
	want := func(w *httptest.ResponseRecorder, status int, text string) {
		t.Helper()
		if w.Code != status || !strings.Contains(w.Body.String(), text) {
			t.Fatalf("status=%d want=%d text=%q body=%s", w.Code, status, text, w.Body.String())
		}
	}
	used := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT used_count FROM invites WHERE id=$1`, inviteID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	access, err := a.store.AvailableInvite(ctx, security.HashToken("active-test-token"))
	if err != nil || !access.ExpiresAt.Equal(deadline) {
		t.Fatal("expiry was not loaded", access, err)
	}
	w := request("GET", "/invite/active-test-token", nil)
	want(w, 200, deadline.Format("02.01.2006 15:04"))
	for _, text := range []string{"Обучение LoRA", "Изображения в сутки", "Повышенный", "после регистрации"} {
		want(w, 200, text)
	}
	fields := url.Values{"username": {"new-user"}, "email": {"new@example.com"}, "password": {"integration-password"}}
	for _, tc := range []struct{ key, value, target string }{{"username", "x", "auth-username"}, {"email", "bad-address", "auth-email"}, {"password", "short", "auth-password"}, {"username", "taken", "auth-username"}, {"email", "taken@example.com", "auth-email"}} {
		original := fields.Get(tc.key)
		fields.Set(tc.key, tc.value)
		w := request("POST", "/invite/active-test-token", fields)
		want(w, 200, `href="#`+tc.target+`"`)
		want(w, 200, deadline.Format("02.01.2006"))
		if used() != 0 {
			t.Fatal("failed registration consumed invitation")
		}
		fields.Set(tc.key, original)
	}
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION auth_test_reject_user() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.username='uncertain-user' THEN RAISE EXCEPTION 'injected registration storage failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER auth_test_reject_user BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION auth_test_reject_user()`); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS auth_test_reject_user ON users; DROP FUNCTION IF EXISTS auth_test_reject_user()`)
	fields.Set("username", "uncertain-user")
	w = request("POST", "/invite/active-test-token", fields)
	want(w, 503, `value="uncertain-user"`)
	want(w, 503, "Результат регистрации не подтверждён")
	if used() != 0 || strings.Contains(w.Body.String(), "injected registration") || !strings.Contains(w.Body.String(), `type="submit" disabled`) {
		t.Fatal("unsafe uncertain registration response", w.Body.String())
	}
	fields.Set("username", "new-user")
	w = request("POST", "/invite/active-test-token", fields)
	if w.Code != 302 || w.Header().Get("Location") != "/login" || used() != 1 {
		t.Fatal(w.Code, w.Header(), used())
	}
	user, registeredHash, err := a.store.FindUserWithPassword(ctx, "new@example.com")
	if err != nil || !security.VerifyPassword(registeredHash, fields.Get("password")) || !user.CanGenerateVideo || user.MaxVideoGenerationQuality != 720 || !user.AccountExpiresAt.Valid {
		t.Fatal("registration grants or password wrong", user, err)
	}
	fields.Set("username", "second-user")
	fields.Set("email", "second@example.com")
	want(request("POST", "/invite/active-test-token", fields), 200, "уже использовано")
	if used() != 1 {
		t.Fatal("reused one-use invitation")
	}
	makeInvite("expired-test-token", time.Now().Add(-time.Minute))
	revokedID := makeInvite("revoked-test-token", deadline)
	if _, err := a.store.SetInviteRevoked(ctx, revokedID, true); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"expired-test-token", "revoked-test-token", "missing-test-token"} {
		w := request("GET", "/invite/"+token, nil)
		want(w, 200, "Приглашение недоступно")
		if strings.Contains(w.Body.String(), `type="submit"`) {
			t.Fatal("invalid invitation has active form")
		}
	}
	login := url.Values{"login": {"new@example.com"}, "password": {"wrong-password"}, "next": {"/generate?draft=kept"}}
	w = request("POST", "/login", login)
	want(w, 200, "Неверный логин или пароль")
	want(w, 200, `value="new@example.com"`)
	want(w, 200, `value="/generate?draft=kept"`)
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("failed login issued cookie")
	}
	login.Set("password", "integration-password")
	w = request("POST", "/login", login)
	if w.Code != 302 || w.Header().Get("Location") != "/generate?draft=kept" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" {
		t.Fatal("session cookie contract", cookies)
	}
	sessionUser, err := a.store.UserBySessionHash(ctx, security.HashToken(cookies[0].Value), time.Hour)
	if err != nil || sessionUser.ID != user.ID {
		t.Fatal("session not persisted", sessionUser, err)
	}
	login.Del("login")
	login.Set("username", "new-user")
	login.Set("next", "//foreign.invalid")
	w = request("POST", "/login", login)
	if w.Code != 302 || w.Header().Get("Location") != "/app" {
		t.Fatal("legacy identity or safe next broken", w.Code, w.Header())
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET account_expires_at=now()-interval '1 minute' WHERE id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	want(request("POST", "/login", login), 200, "Неверный логин или пароль")
	// Verify service failures without closing the fixture DB needed by deferred cleanup.
	closed, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	a.store = store.New(closed)
	want(request("POST", "/login", login), 503, "Вход временно недоступен")
	w = request("POST", "/invite/active-test-token", fields)
	want(w, 503, "Не удалось проверить приглашение")
	want(w, 503, `value="second-user"`)
	if !strings.Contains(w.Body.String(), "disabled") {
		t.Fatal("unknown invitation state must not be submittable")
	}
}
