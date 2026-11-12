package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/security"
)

// The real entry points use only synthetic credentials, an isolated database and
// loopback services. No production model, GPU agent or configuration is used.
func TestAssistantModelRolloutActualProcesses(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, false)
	if err := applyMigrationsThroughForTest(context.Background(), db, 94); err != nil {
		t.Fatal(err)
	}
	gatewayBinary := buildRuntimeProcess(t, "gateway")
	workerBinary := buildRuntimeProcess(t, "inference-worker")
	spool, workerSpool := t.TempDir(), t.TempDir()
	workerEnv := runtimeProcessEnvironment(dsn, workerSpool, nil)
	badCtx, badCancel := context.WithTimeout(context.Background(), 5*time.Second)
	bad := exec.CommandContext(badCtx, workerBinary)
	bad.Env, bad.Dir = workerEnv, workerSpool
	out, err := bad.CombinedOutput()
	badCancel()
	if err == nil || !bytes.Contains(out, []byte("schema is not installed")) {
		t.Fatalf("worker accepted schema 94: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(workerSpool, "outcomes")); !os.IsNotExist(err) {
		t.Fatalf("incompatible worker initialized journal: %v", err)
	}

	password := "AssistantRollout!Test2026"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	runtimeExec(t, db, `INSERT INTO users(username,password_hash,role) VALUES ('assistant-admin',$1,'admin'),('assistant-user',$1,'user'),('other-user',$1,'user')`, hash)
	runtimeExec(t, db, `UPDATE users SET can_use_quick_generation=true,can_generate_text_to_image=true WHERE role='user'`)
	var userID int64
	if err := db.QueryRow(`SELECT id FROM users WHERE username='assistant-user'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var startOnce, releaseOnce sync.Once
	unblockModel := func() { releaseOnce.Do(func() { close(release) }) }
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/queue":
			io.WriteString(w, `{"queue_running":[],"queue_pending":[]}`)
		case "/api/ps", "/api/tags":
			io.WriteString(w, `{"models":[]}`)
		case "/api/chat":
			modelCalls.Add(1)
			var request struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "rollout-strong-text" {
				t.Errorf("wrong selected model: %q %v", request.Model, err)
			}
			startOnce.Do(func() { close(started) })
			select {
			case <-release:
				io.WriteString(w, `{"done":true,"message":{"content":"{\"prompt\":\"A green vase in daylight.\",\"references\":[]}"},"eval_count":12}`)
			case <-r.Context().Done():
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer model.Close()
	defer unblockModel()
	address := func() string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := listener.Addr().String()
		listener.Close()
		return addr
	}
	publicAddr, adminAddr, workerAddr := address(), address(), address()
	publicURL, adminURL, workerURL := "http://"+publicAddr, "http://"+adminAddr, "http://"+workerAddr
	overrides := map[string]string{
		"PUBLIC_ADDR": publicAddr, "ADMIN_ADDR": adminAddr, "PUBLIC_BASE_URL": publicURL, "ADMIN_BASE_URL": adminURL,
		"ADMIN_ALLOWED_CIDRS": "127.0.0.0/8,::1/128", "COMFYUI_UPSTREAM": model.URL, "OLLAMA_UPSTREAM": model.URL,
		"INFERENCE_WORKER_URL": workerURL, "PROMPT_ASSISTANT_DURABLE_JOBS": "true", "PROMPT_ASSISTANT_MODEL": "rollout-default",
		"PROMPT_ASSISTANT_MODELS": `[{"id":"strong","label":"Stronger test model","model":"rollout-strong-text"}]`,
	}
	workerEnv = runtimeProcessEnvironment(dsn, workerSpool, map[string]string{"INFERENCE_WORKER_ADDR": workerAddr, "OLLAMA_UPSTREAM": model.URL})
	start := func(binary, dir string, env []string) func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cmd := exec.CommandContext(ctx, binary)
		cmd.Env, cmd.Dir = env, dir
		var logs bytes.Buffer
		cmd.Stdout, cmd.Stderr = &logs, &logs
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				_ = cmd.Wait()
				if t.Failed() {
					t.Log(logs.String())
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	wait := func(label string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !ready() {
			if time.Now().After(deadline) {
				t.Fatal("timed out: " + label)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	newClient := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		transport := &http.Transport{Proxy: nil}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Jar: jar, Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	admin, user, other := newClient(), newClient(), newClient()
	call := func(client *http.Client, method, base, path string, form url.Values, want int) runtimeHTTPResponse {
		t.Helper()
		response, err := runtimeHTTPCall(client, method, base, path, "", form)
		if err != nil || response.status != want {
			t.Fatalf("%s %s: %d want %d, %v: %.1500s", method, path, response.status, want, err, response.body)
		}
		return response
	}
	health := func(base string, worker bool) bool {
		response, err := runtimeHTTPCall(user, "GET", base, "/healthz", "", nil)
		if err != nil || response.status != 200 {
			return false
		}
		if !worker {
			return true
		}
		var status struct {
			Ready    bool `json:"ready"`
			Protocol int  `json:"protocol"`
			Access   int  `json:"assistant_model_access"`
		}
		return json.Unmarshal(response.body, &status) == nil && status.Ready && status.Protocol == 2 && status.Access == 1
	}
	csrf := func(body []byte) string {
		t.Helper()
		decoder := xml.NewDecoder(bytes.NewReader(body))
		decoder.Strict, decoder.AutoClose, decoder.Entity = false, xml.HTMLAutoClose, xml.HTMLEntity
		for {
			token, err := decoder.Token()
			if err != nil {
				t.Fatal("CSRF field not found", err)
			}
			if element, ok := token.(xml.StartElement); ok && element.Name.Local == "input" {
				attrs := map[string]string{}
				for _, attr := range element.Attr {
					attrs[attr.Name.Local] = attr.Value
				}
				if attrs["name"] == "csrf" && attrs["value"] != "" {
					return attrs["value"]
				}
			}
		}
	}
	stopGateway := start(gatewayBinary, spool, runtimeProcessEnvironment(dsn, spool, overrides))
	wait("Gateway startup and migration", func() bool { return health(publicURL, false) })
	var migrationName string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=95`).Scan(&migrationName); err != nil || migrationName != "prompt_assistant_model_access" {
		t.Fatal("migration 95 missing", migrationName, err)
	}
	for client, login := range map[*http.Client]string{admin: "assistant-admin", user: "assistant-user", other: "other-user"} {
		call(client, "POST", publicURL, "/login", url.Values{"login": {login}, "password": {password}}, 302)
	}
	page := call(user, "GET", publicURL, "/generate", nil, 200)
	if bytes.Contains(page.body, []byte(`value="strong"`)) {
		t.Fatal("ungranted model exposed")
	}
	userCSRF := csrf(page.body)
	jobID := strings.Repeat("a", 32)
	form := url.Values{"csrf": {userCSRF}, "template_id": {"text-to-image"}, "assistant_template": {"workflow-default"}, "assistant_model": {"strong"}, "prompt": {"A green vase on a desk"}, "context": {"rollout-context"}, "request_id": {jobID}}
	if !bytes.Contains(page.body, []byte(`data-durable-jobs="true"`)) {
		t.Fatal("enabled queue missing from page")
	}
	call(user, "POST", publicURL, "/generate/prompt-assistant/jobs", form, 403)
	profilePath := fmt.Sprintf("/admin/users/%d?tab=access", userID)
	adminPage := call(admin, "GET", adminURL, profilePath, nil, 200)
	grant := url.Values{"csrf": {csrf(adminPage.body)}, "action": {"update_assistant_model"}, "assistant_model": {"strong"}, "granted": {"1"}}
	call(admin, "POST", adminURL, profilePath, grant, 302)
	page = call(user, "GET", publicURL, "/generate", nil, 200)
	if !bytes.Contains(page.body, []byte(`value="strong"`)) || bytes.Contains(page.body, []byte("rollout-strong-text")) {
		t.Fatal("authorized selector missing or leaked runtime tag")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM prompt_assistant_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("job existed before first POST", count, err)
	}

	// Withhold the first real acceptance receipt at the transport boundary. The
	// client disconnects without learning the response, then recovers only by GET.
	accepted, disconnected := make(chan int, 1), make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forward, err := http.NewRequestWithContext(r.Context(), "POST", publicURL+r.URL.RequestURI(), r.Body)
		if err != nil {
			accepted <- 0
			return
		}
		forward.Header = r.Header.Clone()
		response, err := user.Transport.RoundTrip(forward)
		if err != nil {
			accepted <- 0
			return
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		accepted <- response.StatusCode
		<-r.Context().Done()
		close(disconnected)
	}))
	defer proxy.Close()
	lostCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	request, _ := http.NewRequestWithContext(lostCtx, "POST", proxy.URL+"/generate/prompt-assistant/jobs", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", publicURL)
	base, _ := url.Parse(publicURL)
	for _, cookie := range user.Jar.Cookies(base) {
		request.AddCookie(cookie)
	}
	lost := make(chan error, 1)
	go func() {
		response, err := user.Do(request)
		if response != nil {
			response.Body.Close()
		}
		lost <- err
	}()
	select {
	case status := <-accepted:
		if status != 202 {
			t.Fatalf("first acceptance: %d", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no acceptance")
	}
	disconnect()
	if err := <-lost; !errors.Is(err, context.Canceled) {
		t.Fatal("receipt was not lost", err)
	}
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect not observed")
	}
	call(user, "GET", publicURL, "/generate/prompt-assistant/jobs?id="+jobID, nil, 200)
	// Retrying an unacknowledged submission uses the same immutable request ID.
	call(user, "POST", publicURL, "/generate/prompt-assistant/jobs", form, 202)
	call(user, "POST", publicURL, "/generate/prompt-assistant/jobs", form, 202)
	if err := db.QueryRow(`SELECT count(*) FROM prompt_assistant_jobs WHERE id=$1 AND model_id='strong'`, jobID).Scan(&count); err != nil || count != 1 {
		t.Fatal("first acceptance not persisted", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM gpu_inference_requests`).Scan(&count); err != nil || count != 0 {
		t.Fatal("offline executor was dispatched", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM gpu_work WHERE state IN ('held','uncertain')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("offline executor retained GPU", count, err)
	}

	stopWorker := start(workerBinary, workerSpool, workerEnv)
	wait("compatible executor", func() bool { return health(workerURL, true) })
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("model not called")
	}
	grant.Del("granted")
	call(admin, "POST", adminURL, profilePath, grant, 302)
	page = call(user, "GET", publicURL, "/generate", nil, 200)
	if bytes.Contains(page.body, []byte(`value="strong"`)) {
		t.Fatal("revoked model still selectable")
	}
	if err := db.QueryRow(`SELECT count(*) FROM gpu_work WHERE state IN ('held','uncertain')`).Scan(&count); err != nil || count != 1 {
		t.Fatal("running model lost GPU protection", count, err)
	}
	stopGateway()
	runtimeExec(t, db, `CREATE FUNCTION rollout_block_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='completed' THEN RAISE EXCEPTION 'synthetic commit outage'; END IF; RETURN NEW; END $$; CREATE TRIGGER rollout_block_completion BEFORE UPDATE ON gpu_inference_requests FOR EACH ROW EXECUTE FUNCTION rollout_block_completion()`)
	unblockModel()
	digest := sha256.Sum256([]byte(jobID))
	receiptPath := filepath.Join(workerSpool, "outcomes", hex.EncodeToString(digest[:])+".receipt")
	wait("persistent completion receipt", func() bool { _, err := os.Stat(receiptPath); return err == nil })
	stopWorker()
	var state string
	if err := db.QueryRow(`SELECT state FROM gpu_inference_requests WHERE id=$1`, jobID).Scan(&state); err != nil || state != "running" {
		t.Fatal("completion committed before executor crash", state, err)
	}
	if receipt, err := os.ReadFile(receiptPath); err != nil || bytes.Contains(receipt, []byte("A green vase")) {
		t.Fatal("receipt missing or plaintext", err)
	}
	runtimeExec(t, db, `DROP TRIGGER rollout_block_completion ON gpu_inference_requests; DROP FUNCTION rollout_block_completion()`)
	start(workerBinary, workerSpool, workerEnv)
	wait("executor journal replay", func() bool { return health(workerURL, true) })
	overrides["PROMPT_ASSISTANT_DURABLE_JOBS"] = "false"
	start(gatewayBinary, spool, runtimeProcessEnvironment(dsn, spool, overrides))
	wait("Gateway rollback-mode restart", func() bool { return health(publicURL, false) })
	var cached runtimeHTTPResponse
	wait("cached response after both restarts", func() bool {
		cached = call(user, "GET", publicURL, "/generate/prompt-assistant/jobs?id="+jobID, nil, 200)
		var view struct {
			State  string          `json:"state"`
			Result json.RawMessage `json:"result"`
		}
		return json.Unmarshal(cached.body, &view) == nil && view.State == "completed" && bytes.Contains(view.Result, []byte("A green vase in daylight."))
	})
	repeated := call(user, "GET", publicURL, "/generate/prompt-assistant/jobs?id="+jobID, nil, 200)
	if !bytes.Equal(cached.body, repeated.body) {
		t.Fatal("cached response changed")
	}
	call(other, "GET", publicURL, "/generate/prompt-assistant/jobs?id="+jobID, nil, 404)
	form.Set("request_id", strings.Repeat("b", 32))
	call(user, "POST", publicURL, "/generate/prompt-assistant/jobs", form, 409)
	if modelCalls.Load() != 1 {
		t.Fatal("model replayed", modelCalls.Load())
	}
	if err := db.QueryRow(`SELECT count(*) FROM gpu_work WHERE state IN ('held','uncertain')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("GPU still held", count, err)
	}
	if _, err := os.Stat(receiptPath); !os.IsNotExist(err) {
		t.Fatal("committed receipt not retired", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM content_events WHERE correlation_id=$1 AND kind='prompt_assistant'`, jobID).Scan(&count); err != nil || count != 1 {
		t.Fatal("content result duplicated", count, err)
	}
}
