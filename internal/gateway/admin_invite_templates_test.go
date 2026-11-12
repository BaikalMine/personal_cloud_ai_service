package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestAdminInviteTemplatesHTTPIntegration(t *testing.T) {
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
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('templates-admin','hash','admin') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	app := &App{store: store.New(db), csrfSigner: security.NewCSRFSigner("template-http-test")}
	admin := &User{ID: id, Role: "admin"}
	request := func(method, path string, form url.Values, actor *User, csrf bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		copy := url.Values{}
		for k, v := range form {
			copy[k] = append([]string(nil), v...)
		}
		if csrf {
			copy.Set("csrf", app.csrfSigner.Token("template-session"))
		}
		r := httptest.NewRequest(method, path, strings.NewReader(copy.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "template-session"})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, actor))
		w := httptest.NewRecorder()
		app.handleAdminRoutes(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	form := url.Values{"action": {"save"}, "name": {"Фото и видео"}, "grant_quick_generation": {"on"}, "grant_text_to_image": {"on"}, "grant_video": {"on"}, "grant_train_image_lora": {"on"}, "grant_advanced_generation_settings": {"on"}, "image_generation_daily_limit": {"12"}, "image_generation_total_limit": {"50"}, "video_generation_daily_limit": {"2"}, "video_generation_total_limit": {"8"}, "max_video_generation_quality": {"720"}, "queue_policy_version": {"2"}, "queue_priority": {"on"}, "invite_ttl_hours": {"1"}, "account_type": {"temporary"}, "temporary_account_hours": {"168"}, "max_uses": {"2"}}
	request("POST", "/admin/invite-templates", form, admin, false, 403)
	request("POST", "/admin/invite-templates", form, &User{ID: id, Role: "user"}, true, 403)
	request("PUT", "/admin/invite-templates", nil, admin, false, 405)
	form.Set("action", "preview")
	request("POST", "/admin/invite-templates", form, admin, true, 200)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM invite_templates`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview wrote templates: %d %v", count, err)
	}
	form.Set("action", "save")
	w := request("POST", "/admin/invite-templates", form, admin, true, 200)
	var saved struct {
		ID       string                        `json:"id"`
		Revision string                        `json:"revision"`
		Settings domain.InviteTemplateSettings `json:"settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Revision != "1" || saved.Settings.Access.ImageDailyLimit != 12 || saved.Settings.Access.VideoDailyLimit != 2 || saved.Settings.LinkLifetimeHours != 1 || saved.Settings.AccountLifetimeSeconds != 168*3600 {
		t.Fatalf("saved: %+v", saved)
	}
	request("GET", "/admin/invite-templates?id="+saved.ID, nil, admin, false, 200)
	request("GET", "/admin/invite-templates?id=01", nil, admin, false, 400)
	request("GET", "/admin/invite-templates?q=x&q=y", nil, admin, false, 400)
	request("GET", "/admin/invite-templates?offset=-1", nil, admin, false, 400)
	request("POST", "/admin/invite-templates?q=x", form, admin, true, 400)
	form.Set("template_id", saved.ID)
	form.Set("revision", saved.Revision)
	form.Set("name", "Изменено")
	request("POST", "/admin/invite-templates", form, admin, true, 200)
	request("POST", "/admin/invite-templates", form, admin, true, 409)
	form.Set("action", "delete")
	request("POST", "/admin/invite-templates", form, admin, true, 409)
	form.Set("revision", "2")
	request("POST", "/admin/invite-templates", form, admin, true, 200)
	request("GET", "/admin/invite-templates?id="+saved.ID, nil, admin, false, 404)
	if _, err := db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	request("GET", "/admin/invite-templates", nil, admin, false, 403)
	for _, preset := range invitationDefaults() {
		if err := preset["settings"].(domain.InviteTemplateSettings).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
