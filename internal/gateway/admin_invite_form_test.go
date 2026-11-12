package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestInvitationReviewBeforeCreateIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	var id int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('review-admin','hash','admin') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store.New(db), tpl: tpl, csrfSigner: security.NewCSRFSigner("invite-review-test"), cfg: Config{PublicBaseURL: "https://example.test"}}
	f := url.Values{"action": {"review"}, "csrf": {a.csrfSigner.Token("review-session")}, "grant_quick_generation": {"on"}, "grant_text_to_image": {"on"}, "grant_video": {"on"}, "queue_policy_version": {"2"}, "queue_priority": {"on"}, "limit_form_version": {"2"}, "image_generation_daily_limit": {"12"}, "image_generation_total_limit": {"50"}, "video_generation_daily_limit": {"2"}, "video_generation_total_limit": {"8"}, "max_video_generation_quality": {"720"}, "invite_ttl_hours": {"1"}, "account_type": {"temporary"}, "temporary_account_hours": {"168"}, "max_uses": {"2"}}
	post := func() *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/admin/invites", strings.NewReader(f.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "review-session"})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: id, Role: "admin"}))
		w := httptest.NewRecorder()
		a.handleAdminInvites(w, r)
		if w.Code != 200 {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		return w
	}
	count := func(want int) {
		t.Helper()
		var got int
		if err := db.QueryRow(`SELECT count(*) FROM invites`).Scan(&got); err != nil || got != want {
			t.Fatalf("invites=%d want=%d err=%v", got, want, err)
		}
	}
	digest := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		match := regexp.MustCompile(`name="invite_review" value="([a-f0-9]{64})"`).FindStringSubmatch(w.Body.String())
		if len(match) != 2 {
			t.Fatalf("missing review digest: %s", w.Body.String())
		}
		return match[1]
	}
	w := post()
	count(0)
	first := digest(w)
	if !strings.Contains(w.Body.String(), "12 в сутки; 50 всего") || !strings.Contains(w.Body.String(), "2 в сутки; 8 всего") {
		t.Fatal("separate effective limits missing")
	}
	f.Set("action", "create")
	f.Set("invite_review", first)
	f.Set("video_generation_daily_limit", "3")
	w = post()
	count(0)
	second := digest(w)
	if first == second {
		t.Fatal("changed quota retained old review")
	}
	f.Set("invite_review", second)
	f.Set("image_generation_daily_limit", "-1")
	w = post()
	count(0)
	if !strings.Contains(w.Body.String(), `value="-1"`) || !strings.Contains(w.Body.String(), `value="168"`) {
		t.Fatal("error lost form fields")
	}
	f.Set("image_generation_daily_limit", "12")
	f.Set("invite_review", second)
	w = post()
	count(1)
	if !strings.Contains(w.Body.String(), "https://example.test/invite/") {
		t.Fatal("missing created invitation")
	}
	invites, err := a.store.ListInvites(ctx, 1)
	if err != nil || len(invites) != 1 || invites[0].VideoGenerationDailyLimit != 3 || invites[0].GenerationDailyLimit != 12 || invites[0].AccountLifetimeSeconds != 168*3600 || !invites[0].QueuePriority {
		t.Fatalf("stored %+v %v", invites, err)
	}
	f.Set("action", "create")
	f.Set("invite_review", "")
	post()
	count(1)
}

func TestInviteFormDefaultsAndCopy(t *testing.T) {
	f := inviteForm(nil)
	if !f.Checked("image_generation_daily_unlimited") || len(f.Quotas()) != 4 {
		t.Fatal("default unlimited state missing")
	}
	values := url.Values{"image_generation_daily_limit": {"-1"}, "limit_form_version": {"2"}}
	copy := inviteForm(values)
	values.Set("image_generation_daily_limit", "20")
	if copy.Value("image_generation_daily_limit") != "-1" || copy.Checked("image_generation_daily_unlimited") {
		t.Fatal("failed form lost exact values")
	}
}

func TestInvitationAssetsArePublished(t *testing.T) {
	for _, path := range []string{"/static/admin-access.js", "/static/admin-access.css"} {
		if staticJavaScriptAssets[path] == "" && staticCSSAssets[path] == "" {
			t.Fatalf("unpublished asset %s", path)
		}
	}
	if _, err := ParseTemplates(); err != nil {
		t.Fatal(err)
	}
}
