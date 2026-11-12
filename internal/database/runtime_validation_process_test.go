package database

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/security"
)

type runtimeHTTPResponse struct {
	status int
	header http.Header
	body   []byte
}

func runtimeGalleryAttributes(body []byte) map[string]string {
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict, d.AutoClose, d.Entity = false, xml.HTMLAutoClose, xml.HTMLEntity
	for {
		token, err := d.Token()
		if err != nil {
			return nil
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "main" {
			continue
		}
		attributes := map[string]string{}
		for _, attribute := range start.Attr {
			attributes[attribute.Name.Local] = attribute.Value
		}
		if _, ok := attributes["data-user-gallery"]; ok {
			return attributes
		}
	}
}

func runtimeHTTPCall(client *http.Client, method, base, path, token string, form url.Values) (runtimeHTTPResponse, error) {
	r, err := http.NewRequest(method, base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return runtimeHTTPResponse{}, err
	}
	if token != "" {
		r.Header.Set("X-Gateway-Release-Token", token)
	}
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", base)
	}
	response, err := client.Do(r)
	if err != nil {
		return runtimeHTTPResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	return runtimeHTTPResponse{response.StatusCode, response.Header, body}, err
}

func TestRuntimeFenceValidationActualGateway(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, true)
	ctx := context.Background()
	binary := buildRuntimeProcess(t, "gateway")
	password := "ValidationSmoke!Password2026"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	runtimeExec(t, db, `INSERT INTO users(username,password_hash,role) VALUES ('smoke-admin',$1,'admin'),('smoke-user',$1,'user')`, hash)
	runtimeExec(t, db, `UPDATE users SET can_use_quick_generation=true,can_generate_text_to_image=true WHERE username='smoke-user'`)
	var adminID, userID int64
	if err := db.QueryRow(`SELECT id FROM users WHERE username='smoke-admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM users WHERE username='smoke-user'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	runtimeExec(t, db, `INSERT INTO users(username,password_hash,role,account_expires_at) VALUES ('expired-user',$1,'user',now()-interval '1 day')`, hash)
	runtimeExec(t, db, `INSERT INTO sessions(user_id,token_hash,expires_at,last_seen_at) SELECT id,$1,now()-interval '1 day',now()-interval '2 days' FROM users WHERE username='smoke-admin'`, strings.Repeat("e", 64))
	c, err := BeginRuntimeRelease(ctx, db, runtimeRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if yes, err := c.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("quiesce: %v %v", yes, err)
	}
	spool := t.TempDir()
	marker := filepath.Join(spool, "gateway-media-preserved")
	if err := os.WriteFile(marker, []byte("do not clean in validation"), 0600); err != nil {
		t.Fatal(err)
	}
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	publicReservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer publicReservation.Close()
	adminReservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer adminReservation.Close()
	publicAddress, adminAddress := publicReservation.Addr().String(), adminReservation.Addr().String()
	publicURL, adminURL := "http://"+publicAddress, "http://"+adminAddress
	request := runtimeValidationRequest()
	overrides := map[string]string{
		"PUBLIC_ADDR": publicAddress, "ADMIN_ADDR": adminAddress,
		"PUBLIC_BASE_URL": publicURL, "ADMIN_BASE_URL": adminURL, "ADMIN_ALLOWED_CIDRS": "127.0.0.0/8,::1/128",
		"RELEASE_VALIDATION_MODE": "true", "RELEASE_OPERATION_ID": request.OperationID,
		"RELEASE_OPERATION_TOKEN": request.Token, "RELEASE_OPERATION_EPOCH": "1",
		"RELEASE_CANDIDATE_MANIFEST_SHA256": request.CandidateManifestHash,
		"MINING_FENCED_COMMANDS":            "true", "COMFY_GPU_GUARD_REQUIRED": "true", "OLLAMA_GPU_GUARD_REQUIRED": "true",
		"COMFY_GPU_GUARD_TOKEN": strings.Repeat("k", 64), "OLLAMA_GPU_GUARD_TOKEN": strings.Repeat("o", 64),
		"COMFY_STORAGE_CLEANUP_ENABLED": "true", "LORA_CAPTION_DURABLE_INFERENCE": "true", "PROMPT_ASSISTANT_DURABLE_JOBS": "true",
	}
	for _, name := range []string{"COMFYUI_UPSTREAM", "OPENWEBUI_UPSTREAM", "OLLAMA_UPSTREAM", "CONTENT_MODERATOR_UPSTREAM", "INFERENCE_WORKER_URL", "MINING_AGENT_URL", "SYSTEM_MONITOR_AGENT_URL", "UPDATE_AGENT_URL", "LORA_TRAINING_AGENT_URL"} {
		overrides[name] = upstream.URL
	}
	for _, name := range []string{"MINING_AGENT_TOKEN", "SYSTEM_MONITOR_AGENT_TOKEN", "UPDATE_AGENT_TOKEN", "LORA_TRAINING_AGENT_TOKEN"} {
		overrides[name] = strings.Repeat("t", 64)
	}
	env := runtimeProcessEnvironment(dsn, spool, overrides)
	if err := c.WithQuiescentConnection(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `UPDATE schema_migrations SET name='incorrect_validation_name' WHERE version=93`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.AllowValidationConnections(ctx); err != nil {
		t.Fatal(err)
	}
	badCtx, badCancel := context.WithTimeout(ctx, 5*time.Second)
	bad := exec.CommandContext(badCtx, binary)
	bad.Env, bad.Dir = env, spool
	badOutput, badErr := bad.CombinedOutput()
	badCancel()
	if badErr == nil || !bytes.Contains(badOutput, []byte(ErrRuntimeSchemaMismatch.Error())) {
		t.Fatalf("mismatched validation binary did not refuse startup: %v\n%s", badErr, badOutput)
	}
	var storedName string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=93`).Scan(&storedName); err != nil || storedName != "incorrect_validation_name" {
		t.Fatalf("candidate repaired the migration catalog: %q %v", storedName, err)
	}
	if yes, err := c.TryQuiesce(ctx); !yes || err != nil {
		t.Fatalf("failed candidate left a connection: %v %v", yes, err)
	}
	if err := c.WithQuiescentConnection(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `UPDATE schema_migrations SET name=$1 WHERE version=93`, runtimeFenceMigration.name)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.AllowValidationConnections(ctx); err != nil {
		t.Fatal(err)
	}
	publicReservation.Close()
	adminReservation.Close()
	processCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	cmd := exec.CommandContext(processCtx, binary)
	cmd.Env, cmd.Dir = env, spool
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	stop := func() {
		cancel()
		if !waited {
			<-done
			waited = true
		}
	}
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			t.Log(output.String())
		}
	})
	defer stop()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			waited = true
			t.Fatalf("candidate exited before readiness: %v\n%s", err, output.String())
		default:
		}
		response, err := runtimeHTTPCall(client, http.MethodGet, publicURL, "/internal/release/validation", request.Token, nil)
		if err == nil && response.status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("candidate did not become ready for validation: %d %v", response.status, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	call := func(base, method, path, token string, form url.Values, want int) runtimeHTTPResponse {
		t.Helper()
		response, err := runtimeHTTPCall(client, method, base, path, token, form)
		if err != nil || response.status != want {
			t.Fatalf("%s %s: status=%d want=%d err=%v body=%s", method, path, response.status, want, err, response.body[:min(len(response.body), 2048)])
		}
		if response.header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s is cacheable in validation", path)
		}
		return response
	}
	for _, base := range []string{publicURL, adminURL} {
		call(base, "GET", "/login", "", nil, http.StatusServiceUnavailable)
		call(base, "GET", "/internal/release/validation", "wrong", nil, http.StatusServiceUnavailable)
		call(base, "GET", "/readyz", request.Token, nil, http.StatusServiceUnavailable)
		call(base, "GET", "/healthz", request.Token, nil, http.StatusOK)
		call(base, "GET", "/admin/users", request.Token, nil, http.StatusFound)
	}
	metadata := call(publicURL, "GET", "/internal/release/validation", request.Token, nil, http.StatusOK)
	var report struct {
		Mode            string                   `json:"mode"`
		ProductionReady bool                     `json:"production_ready"`
		Operation       RuntimeValidationRequest `json:"operation"`
		Gateway         buildinfo.Report         `json:"gateway"`
	}
	if err := json.Unmarshal(metadata.body, &report); err != nil || report.Mode != "release_validation" || report.ProductionReady || report.Operation.Epoch != 1 || report.Operation.OperationID != request.OperationID || report.Gateway.Component != buildinfo.Gateway || bytes.Contains(metadata.body, []byte(request.Token)) {
		t.Fatalf("invalid validation report: %v %s", err, metadata.body)
	}
	call(publicURL, "POST", "/login", request.Token, url.Values{"login": {"smoke-admin"}, "password": {password}, "next": {"/app"}}, http.StatusFound)
	for _, path := range []string{"/app", "/generate", "/gallery", "/train-lora", "/tasks"} {
		response := call(publicURL, "GET", path, request.Token, nil, http.StatusOK)
		if !bytes.Contains(response.body, []byte("<html")) || bytes.Contains(response.body, []byte(request.Token)) {
			t.Errorf("%s did not render private-token-free HTML", path)
		}
		if path == "/gallery" {
			attributes := runtimeGalleryAttributes(response.body)
			if attributes["data-owner"] != strconv.FormatInt(adminID, 10) || attributes["data-can-train"] != "true" {
				t.Errorf("admin gallery is missing its account-scoped owner or training permission: %+v", attributes)
			}
		}
	}
	for _, path := range []string{"/admin", "/admin/users", "/admin/invites", "/admin/release"} {
		response := call(adminURL, "GET", path, request.Token, nil, http.StatusOK)
		if !bytes.Contains(response.body, []byte("<html")) {
			t.Errorf("%s did not render HTML", path)
		}
	}
	call(adminURL, "GET", "/admin/release/status", request.Token, nil, http.StatusOK)
	call(publicURL, "GET", "/static/app.js", request.Token, nil, http.StatusOK)
	call(publicURL, "HEAD", "/internal/release/validation", request.Token, nil, http.StatusOK)
	for _, path := range []string{"/generate/run", "/train-lora", "/admin/users", "/internal/gpu/comfy/permit", "/internal/gpu/ollama/permit", "/admin/updates/install"} {
		call(adminURL, "POST", path, request.Token, url.Values{"action": {"start"}}, http.StatusServiceUnavailable)
	}
	for _, path := range []string{"/comfyui", "/openwebui/", "/api/queue", "/api/tasks/events", "/generate/library/1", "/debug/pprof/", "/%61pp"} {
		call(publicURL, "GET", path, request.Token, nil, http.StatusServiceUnavailable)
	}
	call(publicURL, "GET", "/app", "", nil, http.StatusServiceUnavailable)
	jar, _ = cookiejar.New(nil)
	client.Jar = jar
	call(publicURL, "POST", "/login", request.Token, url.Values{"login": {"smoke-user"}, "password": {password}}, http.StatusFound)
	call(adminURL, "GET", "/admin/users", request.Token, nil, http.StatusForbidden)
	userGallery := call(publicURL, "GET", "/gallery", request.Token, nil, http.StatusOK)
	attributes := runtimeGalleryAttributes(userGallery.body)
	if attributes["data-owner"] != strconv.FormatInt(userID, 10) || attributes["data-can-train"] != "false" {
		t.Errorf("ordinary gallery is missing its own account scope or has unexpected training permission: %+v", attributes)
	}
	var users, expiredSessions, work int
	if err := db.QueryRow(`SELECT count(*) FROM users`).Scan(&users); err != nil || users != 3 {
		t.Fatalf("candidate ran account bootstrap or temporary-user cleanup: %d %v", users, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE token_hash=$1`, strings.Repeat("e", 64)).Scan(&expiredSessions); err != nil || expiredSessions != 1 {
		t.Fatalf("candidate ran expired session cleanup: %d %v", expiredSessions, err)
	}
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM generation_jobs)+(SELECT count(*) FROM lora_training_jobs)+(SELECT count(*) FROM gpu_inference_requests)+(SELECT count(*) FROM gpu_work)`).Scan(&work); err != nil || work != 0 {
		t.Fatalf("candidate admitted background work: %d %v", work, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "do not clean in validation" {
		t.Fatalf("candidate touched configured media spool: %q %v", data, err)
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("candidate contacted configured upstreams %d times", calls)
	}
	requireRuntimeBlocked(t, dsn, ErrRuntimeFenced)
	c.Close()
	call(publicURL, "GET", "/internal/release/validation", request.Token, nil, http.StatusServiceUnavailable)
	call(publicURL, "POST", "/login", request.Token, url.Values{"login": {"smoke-admin"}, "password": {password}}, http.StatusServiceUnavailable)
	stop()
	state, err := ReadRuntimeFence(ctx, db)
	if err != nil || !state.Active || state.Epoch != 1 {
		t.Fatalf("candidate/controller exit cleared durable state: %+v %v", state, err)
	}
}
