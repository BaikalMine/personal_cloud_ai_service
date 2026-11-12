package gateway

import (
	"context"
	"encoding/json"
	"errors"
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
	"ai-access-gateway/internal/updates"
)

func TestAdminUpdatesWorkspaceEvidence(t *testing.T) {
	now := time.Now().UTC()
	base := updates.ComponentStatus{Name: "gateway", DisplayName: "Gateway", Configured: true, CheckedAt: now, CurrentVersion: "v1", LatestVersion: "v2", UpdateAvailable: true, CanInstall: true}
	for _, tc := range []struct {
		name      string
		edit      func(*updates.ComponentStatus)
		available bool
		err       error
		install   bool
	}{
		{"ready", func(*updates.ComponentStatus) {}, true, nil, true},
		{"unknown time", func(c *updates.ComponentStatus) { c.CheckedAt = time.Time{} }, true, nil, false},
		{"stale", func(c *updates.ComponentStatus) { c.CheckedAt = now.Add(-16 * time.Minute) }, true, nil, false},
		{"future", func(c *updates.ComponentStatus) { c.CheckedAt = now.Add(time.Minute) }, true, nil, false},
		{"unconfigured", func(c *updates.ComponentStatus) { c.Configured = false }, true, nil, false},
		{"diagnostic", func(c *updates.ComponentStatus) { c.Message = "busy" }, true, nil, false},
		{"unknown version", func(c *updates.ComponentStatus) { c.CurrentVersion = "" }, true, nil, false},
		{"offline", func(*updates.ComponentStatus) {}, false, nil, false},
		{"failed response with data", func(*updates.ComponentStatus) {}, true, errors.New("failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := base
			tc.edit(&item)
			v := newAdminUpdatesView(now, updates.Status{Available: tc.available, Components: []updates.ComponentStatus{item}}, []string{"gateway"}, "install", tc.err)
			if len(v.Components) != 1 || v.Components[0].InstallAllowed != tc.install || !v.Components[0].Selected || v.Overview.Current != 0 {
				t.Fatal(v)
			}
			if tc.err != nil && (v.Result.Label != "Результат установки не подтвержден" || v.Available) {
				t.Fatal(v)
			}
		})
	}
	base.LatestVersion, base.UpdateAvailable, base.CanInstall = base.CurrentVersion, false, false
	v := newAdminUpdatesView(now, updates.Status{Available: true, Components: []updates.ComponentStatus{base, base, {Name: "unknown"}}}, nil, "", nil)
	if len(v.Components) != 1 || v.Overview.Current != 1 || v.CanInstall || !v.CanCheck {
		t.Fatal(v)
	}
}

func TestAdminUpdatesWorkspaceHTTP(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	commands := make(chan string, 8)
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		commands <- r.Method + " " + r.URL.Path
		_ = json.NewEncoder(w).Encode(updates.Status{Components: []updates.ComponentStatus{{Name: "gateway", DisplayName: "<script>bad()</script>", Configured: true, CheckedAt: time.Now().UTC(), CurrentVersion: "v1", LatestVersion: "v2", UpdateAvailable: true, CanInstall: true}}})
	}))
	defer agent.Close()
	base, _ := url.Parse(agent.URL)
	a := &App{tpl: tpl, updates: updates.NewClient(base, strings.Repeat("x", 32)), csrfSigner: security.NewCSRFSigner(strings.Repeat("s", 32))}
	call := func(role, method string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/admin/updates", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "updates-session"})
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: role}))
		w := httptest.NewRecorder()
		a.handleAdminUpdates(w, r)
		return w
	}
	for _, tc := range []struct {
		role, method string
		form         url.Values
		code         int
	}{
		{"user", "GET", nil, 403}, {"admin", "DELETE", nil, 405},
		{"admin", "POST", url.Values{"action": {"install:gateway"}}, 403},
	} {
		if w := call(tc.role, tc.method, tc.form); w.Code != tc.code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if len(commands) != 0 {
		t.Fatal(commands)
	}
	w := call("admin", "GET", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "<script>bad()") || !strings.Contains(w.Body.String(), "&lt;script&gt;") || !strings.Contains(w.Body.String(), "admin-updates.css") {
		t.Fatal(w.Code, w.Body.String())
	}
	csrf := a.csrfSigner.Token("updates-session")
	w = call("admin", "POST", url.Values{"csrf": {csrf}, "action": {"destroy"}, "components": {"gateway"}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Неизвестное действие") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("admin", "POST", url.Values{"csrf": {csrf}, "action": {"check"}, "components": {"gateway"}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Проверка завершена") || !strings.Contains(w.Body.String(), "value=\"gateway\" aria-label=") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("admin", "POST", url.Values{"csrf": {csrf}, "action": {"install:gateway"}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Подтвердите установку") {
		t.Fatal(w.Code, w.Body.String())
	}
	gotCommands := []string{<-commands, <-commands, <-commands, <-commands}
	if strings.Join(gotCommands, ",") != "GET /v1/status,GET /v1/status,POST /v1/check,GET /v1/status" || len(commands) != 0 {
		t.Fatal(gotCommands)
	}
}

func TestAdminUpdatesInstallFailureHTTPIntegration(t *testing.T) {
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
	var id int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('updates-http','disabled','admin') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"not json", `{"available":true,"message":"<script>private</script>"}`} {
		var got updates.Request
		agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/install" {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(500)
			_, _ = w.Write([]byte(body))
		}))
		base, _ := url.Parse(agent.URL)
		a := &App{tpl: tpl, store: store.New(db), updates: updates.NewClient(base, strings.Repeat("x", 32)), csrfSigner: security.NewCSRFSigner(strings.Repeat("s", 32))}
		form := url.Values{"csrf": {a.csrfSigner.Token("updates-session")}, "action": {"install:gateway"}, "confirm_install": {"1"}, "components": {"gateway", "comfyui"}}
		r := httptest.NewRequest("POST", "/admin/updates", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "updates-session"})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: id, Role: "admin"}))
		w := httptest.NewRecorder()
		a.handleAdminUpdates(w, r)
		agent.Close()
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Результат установки не подтвержден") || strings.Contains(w.Body.String(), "Команда обновления принята") || strings.Contains(w.Body.String(), "<script>private") || len(got.Components) != 1 || got.Components[0] != "gateway" {
			t.Fatal(w.Code, got, w.Body.String())
		}
	}
}
