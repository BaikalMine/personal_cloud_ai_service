package gateway

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestAdminAssistantModelTemplateStates(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"Profile": domain.User{Role: "user"}, "CSRF": "test-token"}
	var b bytes.Buffer
	if err := tpl.ExecuteTemplate(&b, "admin_assistant_models", data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Дополнительные модели не настроены.") || strings.Contains(b.String(), "<form") {
		t.Fatal("empty state", b.String())
	}
	data["AssistantModels"] = BuildAdminAssistantModels([]domain.PromptAssistantModel{{ID: "large", Label: "<unsafe-label>"}}, nil)
	b.Reset()
	if err := tpl.ExecuteTemplate(&b, "admin_assistant_models", data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "<unsafe-label>") || !strings.Contains(b.String(), "&lt;unsafe-label&gt;") {
		t.Fatal("label not escaped")
	}
	data["Profile"] = domain.User{Role: "admin"}
	b.Reset()
	if err := tpl.ExecuteTemplate(&b, "admin_assistant_models", data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "<form") {
		t.Fatal("admin access editable")
	}
}

func TestAdminAssistantModelRows(t *testing.T) {
	rows := BuildAdminAssistantModels([]domain.PromptAssistantModel{
		{ID: "large", Label: "Large", Model: "private-runtime-tag"}, {ID: "vision", Label: "Vision"},
	}, []string{"large", "retired"})
	if len(rows) != 3 || !rows[0].Granted || !rows[0].Available || rows[1].Granted || !rows[2].Granted || rows[2].Available {
		t.Fatal(rows)
	}
	if rows[0].Label != "Large" || rows[2].ID != "retired" {
		t.Fatal(rows)
	}
	if len(BuildAdminAssistantModels(nil, nil)) != 0 {
		t.Fatal("empty catalog")
	}
}

func TestAdminAssistantModelGrantHTTPIntegration(t *testing.T) {
	f, adminID := assistantModelGrantFixture(t, nil)
	ctx := context.Background()
	var err error
	f.app.tpl, err = ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/admin/users/%d", f.user.ID)
	h := f.app.requireAdmin(http.HandlerFunc(f.app.handleAdminRoutes))
	request := func(method, path string, actorID int64, csrf bool, values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		actor, err := f.app.store.UserByID(ctx, actorID)
		if err != nil {
			t.Fatal(err)
		}
		if csrf {
			values.Set("csrf", f.app.csrfSigner.Token("assistant-session"))
		}
		r := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "assistant-session"})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	form := func(id string, granted bool) url.Values {
		v := url.Values{"action": {"update_assistant_model"}, "assistant_model": {id}}
		if granted {
			v.Set("granted", "1")
		}
		return v
	}
	assertAllowed := func(id string, want bool) {
		t.Helper()
		got, err := f.app.store.PromptAssistantModelAllowed(ctx, f.user.ID, id)
		if err != nil || got != want {
			t.Fatal(id, got, err)
		}
	}
	for _, method := range []string{"GET", "POST"} {
		if w := request(method, base, f.user.ID, true, form("gemma-31b", true)); w.Code != 403 {
			t.Fatal("nonadmin", method, w.Code)
		}
	}
	if w := request("POST", base, adminID, false, form("gemma-31b", true)); w.Code != 403 {
		t.Fatal("CSRF", w.Code)
	}
	for _, id := range []string{"", "unknown", "arbitrary:runtime-tag"} {
		if w := request("POST", base, adminID, true, form(id, true)); w.Code != 400 {
			t.Fatal("unknown model", id, w.Code)
		}
	}
	for _, key := range []string{"action", "assistant_model", "granted"} {
		v := form("gemma-31b", true)
		v.Add(key, v.Get(key))
		if w := request("POST", base, adminID, true, v); w.Code != 400 {
			t.Fatal("duplicate", key, w.Code)
		}
	}
	bad := form("gemma-31b", true)
	bad.Set("granted", "true")
	if w := request("POST", base, adminID, true, bad); w.Code != 400 {
		t.Fatal("invalid boolean", w.Code)
	}
	if w := request("POST", fmt.Sprintf("/admin/users/%d", adminID), adminID, true, form("gemma-31b", true)); w.Code != 403 {
		t.Fatal("admin target", w.Code)
	}
	assertAllowed("gemma-31b", false)
	if w := request("GET", base, adminID, false, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `data-assistant-model="gemma-31b"`) || strings.Contains(w.Body.String(), "saved-text") {
		t.Fatal("catalog view", w.Code)
	}
	w := request("POST", base, adminID, true, form("gemma-31b", true))
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "assistant_model=changed") {
		t.Fatal("grant", w.Code, w.Body.String())
	}
	assertAllowed("gemma-31b", true)
	assertAllowed("other-model", false)
	if err := f.app.store.SetPromptAssistantModelGrant(ctx, adminID, f.user.ID, "other-model", true); err != nil {
		t.Fatal(err)
	}
	f.app.cfg.PromptAssistantModels = nil
	w = request("GET", base, adminID, false, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Нет в каталоге") || !strings.Contains(w.Body.String(), `name="granted" value="1" checked`) {
		t.Fatal("missing grant view", w.Code)
	}
	if w = request("POST", base+"?granted=1", adminID, true, form("gemma-31b", false)); w.Code != 302 {
		t.Fatal("revoke retired", w.Code)
	}
	assertAllowed("gemma-31b", false)
	assertAllowed("other-model", true)
	var audit int
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_log WHERE actor_user_id=$1 AND target_id=$2 AND action='prompt_assistant_model_access_updated'`, adminID, f.user.ID).Scan(&audit); err != nil || audit != 3 {
		t.Fatal("audit", audit, err)
	}
	if w = request("POST", "/admin/users/999999999", adminID, true, form("other-model", false)); w.Code != 404 {
		t.Fatal("missing user", w.Code)
	}
}
