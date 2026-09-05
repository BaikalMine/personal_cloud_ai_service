package gateway

import (
	"context"
	"fmt"
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

func TestParseQueuePrioritySupportsLegacyForms(t *testing.T) {
	for _, version := range []string{"", "2"} {
		for _, allowed := range []bool{false, true} {
			for _, legacy := range []bool{false, true} {
				for _, priority := range []bool{false, true} {
					r := httptest.NewRequest(http.MethodPost, "/", nil)
					r.Form = url.Values{"queue_policy_version": {version}}
					if priority {
						r.Form.Set("queue_priority", "on")
					}
					want := allowed && ((version == "2" && priority) || (version == "" && legacy))
					if got := parseQueuePriority(r, allowed, legacy); got != want {
						t.Fatalf("version=%q allowed=%v legacy=%v priority=%v got=%v", version, allowed, legacy, priority, got)
					}
				}
			}
		}
	}
}

func TestQueuePriorityHandlersIntegration(t *testing.T) {
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
	repository := store.New(db)
	var adminID, userID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,pause_mining_for_quick_generation) VALUES('priority-admin','hash','admin',true) RETURNING id`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('priority-user','hash','user') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: repository, tpl: tpl, csrfSigner: security.NewCSRFSigner("policy-test"), cfg: Config{PublicBaseURL: "https://example.test"}}
	const session = "policy-session"
	post := func(user *User, path string, form url.Values, handler http.HandlerFunc, want int) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf", app.csrfSigner.Token(session))
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, user))
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != want {
			t.Fatalf("%s code=%d want=%d body=%s", path, w.Code, want, w.Body.String())
		}
		return w
	}
	admin := &User{ID: adminID, Role: "admin", Username: "priority-admin"}
	userDetail := func(w http.ResponseWriter, r *http.Request) {
		app.handleAdminUserDetail(w, r, strings.TrimPrefix(r.URL.Path, "/admin/users/"))
	}
	assertUser := func(id int64, priority, pause bool) {
		t.Helper()
		user, err := repository.UserByID(ctx, id)
		if err != nil || user.QueuePriority != priority || user.PauseMiningForQuickGeneration != pause {
			t.Fatalf("stored policy user=%+v err=%v want=%v/%v", user, err, priority, pause)
		}
	}
	post(admin, "/account/quick-generation-priority", url.Values{"enabled": {"on"}}, app.handleAccountQuickGenerationPriority, http.StatusOK)
	assertUser(adminID, true, true)
	post(admin, "/account/generation-mining", url.Values{}, app.handleAccountGenerationMining, http.StatusSeeOther)
	assertUser(adminID, true, false)
	post(admin, "/account/quick-generation-priority", url.Values{}, app.handleAccountQuickGenerationPriority, http.StatusOK)
	assertUser(adminID, false, false)
	post(admin, "/account/generation-mining", url.Values{"pause_mining_for_quick_generation": {"on"}}, app.handleAccountGenerationMining, http.StatusSeeOther)
	assertUser(adminID, false, true)
	post(&User{ID: userID, Role: "user"}, "/account/generation-mining", url.Values{}, app.handleAccountGenerationMining, http.StatusForbidden)
	for _, tc := range []struct {
		version, priority, pause string
		wantPriority, wantPause  bool
	}{{"2", "on", "", true, false}, {"2", "", "on", false, true}, {"", "", "on", true, true}, {"", "", "", false, false}} {
		form := url.Values{"action": {"update_access"}, "can_use_quick_generation": {"on"}, "can_generate_text_to_image": {"on"}, "queue_policy_version": {tc.version}, "queue_priority": {tc.priority}, "pause_mining_for_quick_generation": {tc.pause}}
		post(admin, fmt.Sprintf("/admin/users/%d", userID), form, userDetail, http.StatusFound)
		assertUser(userID, tc.wantPriority, tc.wantPause)
		form.Set("grant_quick_generation", "on")
		form.Set("grant_text_to_image", "on")
		form.Set("invite_ttl_hours", "24")
		post(admin, "/admin/invites", form, app.handleAdminInvites, http.StatusOK)
		invites, err := repository.ListInvites(ctx, 1)
		if err != nil || len(invites) != 1 || invites[0].QueuePriority != tc.wantPriority || invites[0].PauseMiningForQuickGeneration != tc.wantPause {
			t.Fatalf("created invite %+v err=%v want=%v/%v", invites, err, tc.wantPriority, tc.wantPause)
		}
	}
	form := url.Values{"action": {"update_access"}, "can_train_image_lora": {"on"}, "queue_policy_version": {"2"}, "queue_priority": {"on"}}
	post(admin, fmt.Sprintf("/admin/users/%d", userID), form, userDetail, http.StatusFound)
	assertUser(userID, true, false)
}
