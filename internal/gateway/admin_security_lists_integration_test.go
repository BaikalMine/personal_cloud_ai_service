package gateway

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
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

func TestAdminSecurityListsIntegration(t *testing.T) {
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
	// Never migrate or clear an unmarked database for this destructive fixture.
	var name, marker string
	if err := db.QueryRowContext(ctx, `SELECT current_database(),COALESCE(current_setting('ai_gateway.disposable_test',true),'')`).Scan(&name, &marker); err != nil {
		t.Fatal(err)
	}
	if name != "security_lists_d09_ephemeral" || marker != "security-lists-d09-20260914" {
		t.Skip("requires marked disposable security-list database")
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	defer resetGatewayIntegrationDatabase(t, db)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,username,password_hash,role,disabled,account_expires_at) VALUES
 (1,'security-admin','hash','admin',false,NULL),(2,'disabled-user','hash','user',true,NULL),(3,'expired-user','hash','user',false,now()-interval '1 day')`)
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-time.Hour)
	exec(`INSERT INTO audit_log(actor_user_id,action,target_type,target_id,created_at,ip,user_agent,metadata,request_id,correlation_id)
 SELECT 1,CASE WHEN n%2=0 THEN 'session_revoked' ELSE 'user_updated' END,'user',n,$1,'127.0.0.1',
 CASE WHEN n=1 THEN 'literal%_device' ELSE 'device' END,'{"id":9007199254740993}'::jsonb,'request-'||n,'trace_security_test'
 FROM generate_series(1,317)n`, at)
	s := store.New(db)
	f := store.AdminAuditFilter{Before: now}
	seen := map[int64]bool{}
	var previous int64 = 1 << 62
	for offset := 0; offset < 317; offset += 50 {
		f.Offset = offset
		p, err := s.ListAdminAuditPage(ctx, f)
		if err != nil || p.Total != 317 || len(p.Items) != min(50, 317-offset) {
			t.Fatal(p.Total, len(p.Items), err)
		}
		for _, row := range p.Items {
			if seen[row.ID] || row.ID >= previous {
				t.Fatal("duplicate or unstable audit order", row.ID)
			}
			seen[row.ID] = true
			previous = row.ID
		}
	}
	if len(seen) != 317 {
		t.Fatal("audit truncated", len(seen))
	}
	f.Offset = 9999
	p, err := s.ListAdminAuditPage(ctx, f)
	if err != nil || p.Offset != 300 || len(p.Items) != 17 {
		t.Fatal(p, err)
	}
	f.Offset = 0
	f.Search = "%_"
	p, err = s.ListAdminAuditPage(ctx, f)
	if err != nil || p.Total != 1 || p.Items[0].UserAgent != "literal%_device" {
		t.Fatal(p, err)
	}
	f.Search = "request-317"
	p, err = s.ListAdminAuditPage(ctx, f)
	if err != nil || p.Total != 1 {
		t.Fatal(p, err)
	}
	f.Search = ""
	f.Action = "session_revoked"
	f.Target = "user"
	f.Sort = "oldest"
	p, err = s.ListAdminAuditPage(ctx, f)
	if err != nil || p.Total != 158 || p.Items[0].TargetID.Int64 != 2 {
		t.Fatal(p, err)
	}
	f.Action = ""
	f.Target = ""
	f.From = &at
	until := at.Add(time.Microsecond)
	f.Until = &until
	p, err = s.ListAdminAuditPage(ctx, f)
	if err != nil || p.Total != 317 {
		t.Fatal("inclusive lower boundary", p.Total, err)
	}
	f.From = nil
	f.Until = &at
	p, err = s.ListAdminAuditPage(ctx, f)
	if err != nil || p.Total != 0 {
		t.Fatal("exclusive upper boundary", p.Total, err)
	}
	exec(`INSERT INTO audit_log(action,target_type,created_at) VALUES('boundary','system',$1),('old','system',$2)`, now, now.Add(-2*time.Hour))
	exec(`INSERT INTO sessions(user_id,token_hash,expires_at,last_seen_at,ip,user_agent)
 SELECT 1,'bulk-'||n,$1,$2,'192.168.1.1',CASE WHEN n=1 THEN 'literal%_device' ELSE 'browser' END FROM generate_series(1,205)n`, now.Add(24*time.Hour), now.Add(-time.Minute))
	token := "security-list-test-token"
	hash := security.HashToken(token)
	exec(`INSERT INTO sessions(user_id,token_hash,expires_at,last_seen_at) VALUES
 (1,$1,$2,$3),(2,'disabled',$2,$3),(3,'account-expired',$2,$3),(1,'expired',$4,$3),(1,'idle',$2,$5)`, hash, now.Add(24*time.Hour), now, now.Add(-time.Hour), now.Add(-7*time.Hour))
	sf := store.AdminSessionFilter{}
	sessions, err := s.ListAdminSessionsPage(ctx, sf, hash, 6*time.Hour)
	if err != nil || sessions.Total != 206 || !sessions.Items[0].Current {
		t.Fatal(sessions, err)
	}
	sessionSeen := map[int64]bool{}
	previous = 1 << 62
	for offset := 0; offset < 206; offset += 50 {
		sf.Offset = offset
		page, err := s.ListAdminSessionsPage(ctx, sf, hash, 6*time.Hour)
		if err != nil || page.Total != 206 {
			t.Fatal(page, err)
		}
		for _, row := range page.Items {
			if sessionSeen[row.ID] || row.ID >= previous {
				t.Fatal("session order", row.ID)
			}
			sessionSeen[row.ID] = true
			previous = row.ID
		}
	}
	if len(sessionSeen) != 206 {
		t.Fatal("session limit", len(sessionSeen))
	}
	sf = store.AdminSessionFilter{State: "inactive"}
	sessions, err = s.ListAdminSessionsPage(ctx, sf, hash, 6*time.Hour)
	states := map[string]bool{}
	for _, row := range sessions.Items {
		states[row.State] = true
	}
	if err != nil || sessions.Total != 4 || len(states) != 4 || !states["disabled"] || !states["idle"] || !states["expired"] || !states["account_expired"] {
		t.Fatal(sessions, err)
	}
	sf = store.AdminSessionFilter{State: "all", Search: "%_"}
	sessions, err = s.ListAdminSessionsPage(ctx, sf, hash, 6*time.Hour)
	if err != nil || sessions.Total != 1 {
		t.Fatal(sessions, err)
	}
	sf = store.AdminSessionFilter{State: "all", Offset: 9999}
	sessions, err = s.ListAdminSessionsPage(ctx, sf, hash, 6*time.Hour)
	if err != nil || sessions.Total != 210 || sessions.Offset != 200 || len(sessions.Items) != 10 {
		t.Fatal(sessions, err)
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: s, tpl: tpl, csrfSigner: security.NewCSRFSigner("security-lists"), websocketConnections: make(map[*trackedWebSocket]struct{})}
	a.cfg.SessionIdleTimeout = 6 * time.Hour
	request := func(method, target, role string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: 1, Role: role}))
		w := httptest.NewRecorder()
		a.handleAdminRoutes(w, r)
		return w
	}
	for _, target := range []string{"/admin/audit?offset=300", "/admin/sessions?offset=200"} {
		w := request("GET", target, "admin", nil)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), hash) || strings.Contains(w.Body.String(), "bulk-1") {
			t.Fatal("list", w.Code, w.Body.String())
		}
	}
	csvw := request("GET", "/admin/audit/export?before="+url.QueryEscape(now.Format(time.RFC3339))+"&q=does-not-exist&offset=300", "admin", nil)
	if csvw.Code != 200 || !strings.HasPrefix(csvw.Body.String(), "\xef\xbb\xbf") {
		t.Fatal(csvw.Code, csvw.Body.String())
	}
	csvRows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(csvw.Body.String(), "\xef\xbb\xbf"))).ReadAll()
	if err != nil || len(csvRows) != 319 || len(csvRows[0]) != 9 {
		t.Fatal("whole CSV contract", len(csvRows), err)
	}
	for _, row := range csvRows[1:] {
		if row[3] == "boundary" {
			t.Fatal("cutoff leaked")
		}
	}
	var otherID, currentID int64
	if err := db.QueryRow(`SELECT id FROM sessions WHERE token_hash='bulk-1'`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM sessions WHERE token_hash=$1`, hash).Scan(&currentID); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf": {a.csrfSigner.Token(token)}, "confirm_revoke": {"1"}}
	target := fmt.Sprintf("/admin/sessions/%d/revoke?state=all&q=literal%%25_&offset=200", otherID)
	for _, tc := range []struct {
		method, role string
		form         url.Values
		status       int
	}{
		{"POST", "user", form, 403}, {"GET", "admin", form, 405}, {"POST", "admin", url.Values{"confirm_revoke": {"1"}}, 403},
		{"POST", "admin", url.Values{"csrf": {a.csrfSigner.Token(token)}}, 400},
		{"POST", "admin", url.Values{"csrf": {a.csrfSigner.Token(token)}, "confirm_revoke": {"1", "1"}}, 400},
	} {
		if w := request(tc.method, target, tc.role, tc.form); w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	if w := request("POST", target+"&state=valid", "admin", form); w.Code != 400 {
		t.Fatal("ambiguous return accepted", w.Code)
	}
	serverA, clientA := net.Pipe()
	defer clientA.Close()
	defer serverA.Close()
	serverB, clientB := net.Pipe()
	defer clientB.Close()
	defer serverB.Close()
	a.registerWebSocket(1, "bulk-1", "comfyui", serverA)
	trackedCurrent := a.registerWebSocket(1, hash, "comfyui", serverB)
	w := request("POST", target, "admin", form)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	back, err := url.Parse(w.Header().Get("Location"))
	if err != nil || back.Path != "/admin/sessions" || back.Query().Get("state") != "all" || back.Query().Get("q") != "literal%_" || back.Query().Get("status") != "revoked" {
		t.Fatal(back, err)
	}
	clientA.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientA.Read(make([]byte, 1)); err == nil {
		t.Fatal("revoked connection remains open")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("revoke did not close socket")
	}
	a.websocketMu.Lock()
	_, kept := a.websocketConnections[trackedCurrent]
	a.websocketMu.Unlock()
	if !kept {
		t.Fatal("current connection removed")
	}
	w = request("POST", target, "admin", form)
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "status=missing") {
		t.Fatal("repeat revoke", w.Code)
	}
	var revokedAudits int
	if err := db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='session_revoked' AND target_type='session' AND target_id=$1`, otherID).Scan(&revokedAudits); err != nil || revokedAudits != 1 {
		t.Fatal("duplicate revoke audit", revokedAudits, err)
	}
	w = request("POST", fmt.Sprintf("/admin/sessions/%d/revoke", currentID), "admin", form)
	cookies := w.Result().Cookies()
	if w.Code != 303 || w.Header().Get("Location") != "/login" || len(cookies) != 1 || cookies[0].Name != sessionCookieName || cookies[0].MaxAge >= 0 {
		t.Fatal("own revoke", w.Code, w.Header())
	}
	clientB.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientB.Read(make([]byte, 1)); err == nil {
		t.Fatal("own connection remains open")
	}
	// net/http suppresses HEAD response bodies on the wire, not in ResponseRecorder.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: "admin"}))
		a.handleAdminRoutes(w, r)
	}))
	for _, path := range []string{"/admin/audit", "/admin/sessions", "/admin/audit/export"} {
		response, err := server.Client().Head(server.URL + path)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || len(body) != 0 || response.Header.Get("Cache-Control") != "no-store" {
			server.Close()
			t.Fatal("HEAD response", path, response.StatusCode, len(body), err)
		}
	}
	server.Close()
	closedDB, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	closedDB.Close()
	a.store = store.New(closedDB)
	for _, path := range []string{"/admin/audit", "/admin/sessions"} {
		w := request("GET", path, "admin", nil)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "role=\"alert\"") || strings.Contains(w.Body.String(), "<table") {
			t.Fatal("database error must not render an empty list", path, w.Code)
		}
	}
	w = request("POST", target, "admin", form)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "Результат отзыва не подтверждён") || !strings.Contains(w.Body.String(), "Проверить состояние") {
		t.Fatal("unknown mutation result", w.Code, w.Body.String())
	}
}
