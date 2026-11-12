package gateway

import (
	"bytes"
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
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestAdminUserProfileQueryAndForm(t *testing.T) {
	missing := missingProfilePolicyValues([]string{"present", "gone", "gone"}, []string{"present"})
	if len(missing) != 1 || missing[0] != "gone" {
		t.Fatal("missing policy selections", missing)
	}
	for raw, tab := range map[string]string{"": "access", "tab=tasks": "tasks", "password=invalid": "sessions", "security=unlocked": "sessions", "tab=activity&q=chat&sort=oldest&offset=50": "activity"} {
		v, _ := url.ParseQuery(raw)
		q, err := ParseAdminUserProfileQuery(v)
		if err != nil || q.Tab != tab {
			t.Fatal(raw, q, err)
		}
	}
	for _, raw := range []string{"tab=unknown", "tab=tasks&kind=a&kind=b", "tab=sessions&offset=-1", "tab=activity&offset=01", "tab=sessions&service=comfyui", "tab=activity&sort=unsafe"} {
		v, _ := url.ParseQuery(raw)
		if _, err := ParseAdminUserProfileQuery(v); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	u := domain.User{CanUseQuickGeneration: true, CanGenerateTextToImage: true, CanManageMining: true, GenerationDailyLimit: 12, VideoGenerationTotalLimit: 8, MaxVideoGenerationQuality: 720}
	f := userAccessForm(u, nil)
	if !f.Checked("can_use_quick_generation") || !f.Checked("can_manage_mining") || f.Value("image_generation_daily_limit") != "12" || !f.Checked("image_generation_total_unlimited") || f.Checked("video_generation_total_unlimited") {
		t.Fatal("user defaults", f)
	}
	values := url.Values{"limit_form_version": {"2"}, "image_generation_daily_limit": {"-4"}, "can_generate_video": {"on"}}
	f = userAccessForm(u, values)
	values.Set("image_generation_daily_limit", "99")
	if f.Value("image_generation_daily_limit") != "-4" || f.Checked("can_use_quick_generation") || !f.Checked("can_generate_video") || f.Checked("image_generation_total_unlimited") {
		t.Fatal("lost submitted values", f)
	}
	rows := BuildAdminProfileTasks([]domain.Task{{ID: "profile-generation-job", Kind: "generation", State: "waiting_for_resources", Preview: &domain.TaskPreview{MediaID: 42}}, {ID: "training", Kind: "lora_training", State: "caching"}})
	if rows[0].Preview != nil || rows[0].InspectURL != "/admin/jobs/profile-generation-job" || rows[0].StateLabel != "Ожидание ресурсов" || rows[1].StateLabel != "Кеширование" || rows[1].InspectURL != "" {
		t.Fatal("admin task views", rows)
	}
	activities := adminProfileActivities([]domain.Activity{{Service: "comfyui", Method: "POST", Path: "/comfyui/prompt?token=secret"}, {Service: "comfyui", Method: "GET", Path: "/comfyui/api/system_stats"}})
	if len(activities) != 2 || activities[0].Path != "/prompt" || activities[0].Summary != "Запуск генерации" || activities[1].Summary != "Фоновый запрос" {
		t.Fatal("activity view", activities)
	}
}

func TestAdminUserProfileTabTemplates(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	u := domain.User{ID: 42, Username: "<long-user>", Role: "user", CanUseQuickGeneration: true, CanGenerateTextToImage: true}
	for _, tab := range []string{"access", "tasks", "activity", "sessions"} {
		var b bytes.Buffer
		data := map[string]any{"Profile": u, "ProfileTab": tab, "Stats": domain.UserStats{}, "TaskQuery": AdminUserProfileQuery{Tab: tab}, "ProfilePagination": BuildAdminProfilePagination(u.ID, tab, store.UserProfilePage{}, 0)}
		if err := tpl.ExecuteTemplate(&b, "admin_user_detail", data); err != nil {
			t.Fatal(tab, err)
		}
		page := b.String()
		if strings.Count(page, `aria-current="page"`) != 1 || strings.Contains(page, "<long-user>") {
			t.Fatal("tab/escaping", tab)
		}
		if (tab == "access") != strings.Contains(page, "data-admin-user-access-form") || (tab == "sessions") != strings.Contains(page, `name="new_password"`) {
			t.Fatal("tab isolation", tab)
		}
	}
}

func TestAdminUserProfileHTTPIntegration(t *testing.T) {
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
	var adminID, ownerID, otherID int64
	for name, dest := range map[string]*int64{"profile-admin": &adminID, "profile-owner": &ownerID, "profile-other": &otherID} {
		if err := db.QueryRow(`INSERT INTO users(username,password_hash,role,can_use_quick_generation,can_generate_text_to_image,generation_daily_limit) VALUES($1,'hash','user',true,true,12) RETURNING id`, name).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE users SET role='admin' WHERE id=$1`, adminID)
	exec(`UPDATE users SET failed_login_count=3,locked_until=now()+interval '1 hour' WHERE id=$1`, ownerID)
	exec(`INSERT INTO sessions(user_id,token_hash,expires_at,user_agent) SELECT $1,'profile-session-'||n,now()+interval '1 day','Browser '||n FROM generate_series(1,65)n`, ownerID)
	exec(`INSERT INTO sessions(user_id,token_hash,expires_at,user_agent) VALUES($1,$2,now()+interval '1 day','Current browser'),($3,'other-session',now()+interval '1 day','Foreign browser')`, adminID, security.HashToken("profile-http-session"), otherID)
	exec(`INSERT INTO proxy_requests(user_id,service,method,path,status_code,duration_ms) SELECT $1,'comfyui','POST','/comfyui/prompt?token=do-not-render',200,10 FROM generate_series(1,65)`, ownerID)
	exec(`INSERT INTO proxy_requests(user_id,service,method,path,status_code,duration_ms) VALUES($1,'comfyui','POST','/foreign-only',200,10)`, otherID)
	r := store.New(db)
	policy := domain.GenerationAccessPolicy{UserID: ownerID, PresetIDs: []string{"unavailable-preset"}, ModelIDs: []string{"unavailable-model"}, KreaLoraGroups: []string{"missing-krea-group"}, FluxLoraGroups: []string{"missing-flux-group"}}
	if err := r.SaveGenerationAccessPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 65; i++ {
		id := fmt.Sprintf("profile-generation-%03d", i)
		if _, _, err := r.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: id, RequestID: id, UserID: ownerID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := r.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: "foreign-generation-job", RequestID: "foreign-generation-job", UserID: otherID}); err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: r, tpl: tpl, csrfSigner: security.NewCSRFSigner("profile-http-test"), cfg: Config{SessionIdleTimeout: time.Hour}}
	h := a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes))
	request := func(method, path, role string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "profile-http-session"})
		req = req.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: adminID, Role: role, Username: "profile-admin"}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	base := fmt.Sprintf("/admin/users/%d", ownerID)
	for _, tab := range []string{"access", "tasks", "activity", "sessions"} {
		w := request("GET", base+"?tab="+tab, "admin", nil)
		if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("read tab", tab, w.Code, w.Header())
		}
		if strings.Contains(w.Body.String(), "foreign-generation-job") || strings.Contains(w.Body.String(), "Foreign browser") || strings.Contains(w.Body.String(), "do-not-render") {
			t.Fatal("cross-owner or query disclosure", tab)
		}
		if tab == "access" {
			for _, value := range []string{"unavailable-preset", "unavailable-model", "missing-krea-group", "missing-flux-group"} {
				if !strings.Contains(w.Body.String(), `value="`+value+`" checked`) {
					t.Fatal("catalog loss cleared policy", value)
				}
			}
		}
		if request("GET", base+"?tab="+tab, "user", nil).Code != 403 {
			t.Fatal("nonadmin profile", tab)
		}
	}
	w := request("GET", base+"?tab=sessions&offset=50", "admin", nil)
	if w.Code != 200 || strings.Count(w.Body.String(), "data-profile-session=") != 15 || !strings.Contains(w.Body.String(), "51–65 из 65") {
		t.Fatal("session pagination", w.Code, w.Body.String())
	}
	w = request("GET", base+"?tab=tasks", "admin", nil)
	if strings.Count(w.Body.String(), "data-profile-task=") != 50 {
		t.Fatal("task page size", w.Body.String())
	}
	page, err := r.ListTasks(ctx, domain.TaskScope{UserID: ownerID, Generation: true, Training: true}, domain.TaskFilter{}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	w = request("GET", base+"?tab=tasks&cursor="+url.QueryEscape(page.NextCursor), "admin", nil)
	if w.Code != 200 || strings.Count(w.Body.String(), "data-profile-task=") != 15 {
		t.Fatal("task cursor", w.Code, w.Body.String())
	}
	if request("GET", fmt.Sprintf("/admin/users/%d?tab=tasks&cursor=%s", otherID, url.QueryEscape(page.NextCursor)), "admin", nil).Code != 400 {
		t.Fatal("cursor transferred to another owner")
	}
	for _, path := range []string{base + "?tab=invalid", base + "?tab=sessions&offset=-1", base + "?tab=tasks&cursor=invalid", base + "?tab=tasks&kind=unknown"} {
		if request("GET", path, "admin", nil).Code != 400 {
			t.Fatal("invalid query", path)
		}
	}
	if request("PUT", base, "admin", nil).Code != 405 || request("GET", "/admin/users/9999999", "admin", nil).Code != 404 {
		t.Fatal("method/not found")
	}
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		form.Set("csrf", a.csrfSigner.Token("profile-http-session"))
		return request("POST", path, "admin", form)
	}
	invalid := url.Values{"action": {"update_access"}, "can_use_quick_generation": {"on"}, "can_generate_video": {"on"}, "image_generation_daily_limit": {"-4"}, "video_generation_daily_limit": {"2"}, "can_manage_mining": {"on"}, "max_video_generation_quality": {"2160"}}
	w = post(base, invalid)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `value="-4"`) || !strings.Contains(w.Body.String(), "Изменения не сохранены") || !strings.Contains(w.Body.String(), `name="can_generate_video" type="checkbox" checked`) {
		t.Fatal("invalid form retention", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `value="2160" selected`) {
		t.Fatal("invalid quality value was lost")
	}
	u, err := r.UserByID(ctx, ownerID)
	if err != nil || u.GenerationDailyLimit != 12 || u.CanGenerateVideo {
		t.Fatal("invalid save mutated access", u, err)
	}
	valid := url.Values{"action": {"review_access"}, "limit_form_version": {"2"}, "queue_policy_version": {"2"}, "can_train_image_lora": {"on"}, "queue_priority": {"on"}, "can_manage_mining": {"on"}, "image_generation_daily_unlimited": {"on"}, "image_generation_total_unlimited": {"on"}, "video_generation_daily_unlimited": {"on"}, "video_generation_total_limit": {"8"}, "max_video_generation_quality": {"720"}}
	w = post(base, valid)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Проверено. Изменения ещё не сохранены.") {
		t.Fatal("review", w.Code, w.Body.String())
	}
	u, _ = r.UserByID(ctx, ownerID)
	if u.CanTrainImageLora {
		t.Fatal("review mutated rights")
	}
	valid.Set("action", "update_access")
	w = post(base, valid)
	u, err = r.UserByID(ctx, ownerID)
	if w.Code != 302 || err != nil || u.CanUseQuickGeneration || !u.CanTrainImageLora || !u.QueuePriority || !u.CanManageMining || u.VideoGenerationTotalLimit != 8 {
		t.Fatal("saved rights", w.Code, u, err)
	}
	if request("POST", base, "admin", url.Values{"action": {"disable"}}).Code != 403 {
		t.Fatal("CSRF missing")
	}
	for _, action := range []string{"disable", "enable", "delete", "update_access", "update_generation_policy"} {
		if post(fmt.Sprintf("/admin/users/%d", adminID), url.Values{"action": {action}, "confirm_username": {"profile-admin"}}).Code != 403 {
			t.Fatal("admin protection", action)
		}
	}
	adminBase := fmt.Sprintf("/admin/users/%d?tab=access", adminID)
	w = request("GET", adminBase, "admin", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "data-admin-resource-form") || strings.Contains(w.Body.String(), "data-admin-user-access-form") {
		t.Fatal("admin resource editor missing", w.Code)
	}
	for _, flags := range []struct{ priority, pause bool }{{false, true}, {true, false}} {
		form := url.Values{"action": {"update_generation_resources"}}
		if flags.priority {
			form.Set("queue_priority", "on")
		}
		if flags.pause {
			form.Set("pause_mining_for_quick_generation", "on")
		}
		w = post(adminBase, form)
		admin, err := r.UserByID(ctx, adminID)
		if w.Code != 303 || w.Header().Get("Location") != adminBase+"&access=changed" || err != nil || admin.Role != "admin" || admin.QueuePriority != flags.priority || admin.PauseMiningForQuickGeneration != flags.pause || !admin.CanUseQuickGeneration || admin.GenerationDailyLimit != 12 {
			t.Fatal("admin resources not independent or rights changed", w.Code, admin, err)
		}
	}
	if post(base, url.Values{"action": {"update_generation_resources"}}).Code != 400 {
		t.Fatal("admin-only editor accepted ordinary target")
	}
	for _, form := range []url.Values{
		{"action": {"update_generation_resources"}, "queue_priority": {"on", "on"}},
		{"action": {"update_generation_resources"}, "pause_mining_for_quick_generation": {"true"}},
	} {
		if post(adminBase, form).Code != 400 {
			t.Fatal("malformed resource settings accepted")
		}
	}
	if request("POST", adminBase, "admin", url.Values{"action": {"update_generation_resources"}}).Code != 403 || request("POST", adminBase, "user", url.Values{"action": {"update_generation_resources"}}).Code != 403 {
		t.Fatal("admin resource editor bypassed CSRF or role check")
	}
	var resourceAudits int
	if err := db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='admin_generation_resources_updated' AND target_type='user' AND target_id=$1`, adminID).Scan(&resourceAudits); err != nil || resourceAudits != 2 {
		t.Fatal("resource edits not audited", resourceAudits, err)
	}
	var ownSession, foreignSession, currentSession int64
	if err := db.QueryRow(`SELECT id FROM sessions WHERE token_hash='profile-session-1'`).Scan(&ownSession); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM sessions WHERE token_hash='other-session'`).Scan(&foreignSession); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM sessions WHERE token_hash=$1`, security.HashToken("profile-http-session")).Scan(&currentSession); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{foreignSession, currentSession} {
		w = post(base+"?tab=sessions&q=Browser&offset=50", url.Values{"action": {"revoke_session"}, "session_id": {fmt.Sprint(id)}})
		if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "session_unavailable") {
			t.Fatal("foreign/current revoke", w.Code)
		}
	}
	w = post(fmt.Sprintf("/admin/users/%d?tab=sessions", adminID), url.Values{"action": {"revoke_session"}, "session_id": {fmt.Sprint(currentSession)}})
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "session_unavailable") {
		t.Fatal("own current browser session revoked")
	}
	w = post(base+"?tab=sessions&q=Browser&offset=50", url.Values{"action": {"revoke_session"}, "session_id": {fmt.Sprint(ownSession)}})
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "q=Browser") || !strings.Contains(w.Header().Get("Location"), "offset=50") || !strings.Contains(w.Header().Get("Location"), "session_revoked") {
		t.Fatal("revoke redirect", w.Header())
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE id=$1`, ownSession).Scan(&count); err != nil || count != 0 {
		t.Fatal("selected session not revoked", count, err)
	}
	w = post(base, url.Values{"action": {"unlock"}})
	u, _ = r.UserByID(ctx, ownerID)
	if w.Code != 302 || u.FailedLoginCount != 0 || u.IsLocked(time.Now()) {
		t.Fatal("unlock", u)
	}
	w = post(base, url.Values{"action": {"disable"}})
	u, _ = r.UserByID(ctx, ownerID)
	if w.Code != 302 || !u.Disabled {
		t.Fatal("disable")
	}
	w = post(base, url.Values{"action": {"enable"}})
	u, _ = r.UserByID(ctx, ownerID)
	if w.Code != 302 || u.Disabled {
		t.Fatal("enable")
	}
	w = post(base, url.Values{"action": {"delete"}, "confirm_username": {"wrong"}})
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "confirmation_invalid") {
		t.Fatal("delete confirmation")
	}
	w = post(base, url.Values{"action": {"change_password"}, "new_password": {"short"}, "confirm_password": {"short"}})
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "password=invalid") {
		t.Fatal("password validation")
	}
	if post(base, url.Values{"action": {"unknown"}}).Code != 400 {
		t.Fatal("unknown action")
	}
}
