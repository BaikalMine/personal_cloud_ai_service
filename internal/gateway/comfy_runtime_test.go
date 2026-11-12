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
	"ai-access-gateway/internal/updates"
)

func TestAdminComfyRuntimeAccessAndReadOnlyValidation(t *testing.T) {
	calls := 0
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
			t.Error("unexpected agent request")
		}
		_ = json.NewEncoder(w).Encode(updates.ComfyRuntimeStatus{Available: true, State: "stopped", Version: "stopped"})
	}))
	defer agent.Close()
	base, _ := url.Parse(agent.URL)
	a := &App{updates: updates.NewClient(base, strings.Repeat("x", 32)), csrfSigner: security.NewCSRFSigner("runtime-fixture")}
	for _, tc := range []struct {
		role, method, query, csrf string
		code                      int
	}{
		{"user", "GET", "", "", 403}, {"admin", "DELETE", "", "", 405},
		{"admin", "POST", "", "", 403}, {"admin", "POST", "", "valid", 409},
		{"admin", "GET", "?id=../../bad", "", 400}, {"admin", "GET", "?id=%zz", "", 400},
		{"admin", "GET", "?other=value", "", 400}, {"admin", "GET", "?id=", "", 400},
		{"admin", "GET", "?id=" + strings.Repeat("a", 32) + "&id=" + strings.Repeat("b", 32), "", 400},
		{"admin", "GET", "", "", 200},
	} {
		r := httptest.NewRequest(tc.method, "/admin/services/comfyui/runtime"+tc.query, strings.NewReader(`{}`))
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "runtime-session"})
		if tc.csrf == "valid" {
			r.Header.Set("X-CSRF-Token", a.csrfSigner.Token("runtime-session"))
		}
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: tc.role}))
		w := httptest.NewRecorder()
		a.handleAdminComfyRuntime(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s %s %s: %d %s", tc.role, tc.method, tc.query, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private state cacheable")
		}
	}
	if calls != 1 {
		t.Fatalf("invalid or unauthorized request reached agent: %d", calls)
	}
}

func TestAdminComfyRuntimeGuardsIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	if _, err := db.Exec(`TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	s := store.New(db)
	if err := s.EnsurePrimaryGPUResource(ctx); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('runtime-fixture','disabled','admin') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	commands := 0
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		commands++
		var input updates.ComfyRuntimeRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil || !input.Valid() || !input.ExpiresAt.After(time.Now()) {
			t.Error("invalid forwarded command")
		}
		_ = json.NewEncoder(w).Encode(updates.ComfyRuntimeStatus{Available: true, Version: "stopped", State: "stopped", Receipt: &updates.ComfyRuntimeReceipt{ID: input.ID, Action: input.Action, State: "completed"}})
	}))
	defer agent.Close()
	base, _ := url.Parse(agent.URL)
	a := &App{store: s, cfg: Config{ComfyGPUGuardRequired: true}, updates: updates.NewClient(base, strings.Repeat("x", 32)), csrfSigner: security.NewCSRFSigner("runtime-fixture")}
	a.gpuCoordinator = &gpuCoordinator{app: a}
	// Lifecycle control does not require the optional executor-side plugin.
	a.cfg.ComfyGPUGuardRequired = false
	call := func(body string, code int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/admin/services/comfyui/runtime", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "runtime-session"})
		r.Header.Set("X-CSRF-Token", a.csrfSigner.Token("runtime-session"))
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: id, Role: "admin"}))
		w := httptest.NewRecorder()
		a.handleAdminComfyRuntime(w, r)
		if w.Code != code {
			t.Fatalf("got %d expected %d: %s", w.Code, code, w.Body.String())
		}
	}
	payload, _ := json.Marshal(map[string]any{"id": strings.Repeat("a", 32), "action": "stop", "version": "stopped", "force": true, "expires_at": time.Now().Add(20 * time.Second)})
	call(string(payload), 200)
	var audits int
	if err := db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='comfyui.runtime.stop'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("audit missing", err, audits)
	}
	if _, err := db.Exec(`INSERT INTO gpu_executor_permits(id,resource_id,executor,instance_id,token_hash,state) VALUES($1,$2,'comfyui',$3,$4,'running')`, strings.Repeat("b", 32), domain.GPUPrimaryResource, strings.Repeat("c", 32), strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	call(string(payload), 409)
	if _, err := db.Exec(`DELETE FROM gpu_executor_permits`); err != nil {
		t.Fatal(err)
	}
	if err := s.WithGPUAdmissionGuard(ctx, func() error { call(string(payload), 409); return nil }); err != nil {
		t.Fatal(err)
	}
	locked, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	if _, err := locked.ExecContext(ctx, `SELECT pg_advisory_lock(746218497)`); err != nil {
		t.Fatal(err)
	}
	call(string(payload), 409)
	if _, err := locked.ExecContext(ctx, `SELECT pg_advisory_unlock(746218497)`); err != nil {
		t.Fatal(err)
	}
	call(strings.Replace(string(payload), `"stop"`, `"shell"`, 1), 400)
	call(strings.TrimSuffix(string(payload), "}")+`,"command":"evil"}`, 400)
	if commands != 1 {
		t.Fatalf("blocked command reached agent: %d", commands)
	}
	if _, err := db.Exec(`INSERT INTO generation_jobs(user_id,public_id,correlation_id,request_id,state,template_id) VALUES($1,repeat('e',32),repeat('e',32),repeat('e',32),'waiting_for_resources','text-to-image')`, id); err != nil {
		t.Fatal(err)
	}
	call(string(payload), 409)
	call(strings.Replace(string(payload), `"stop"`, `"start"`, 1), 200)
	if commands != 2 {
		t.Fatal("waiting jobs prevent service recovery")
	}
}
