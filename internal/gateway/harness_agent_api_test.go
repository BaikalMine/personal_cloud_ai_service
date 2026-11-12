package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ai-access-gateway/internal/config"
)

type harnessAgentFixture struct {
	mu         sync.Mutex
	tasks      map[string]harnessAgentTask
	prompts    map[string]int
	requests   []string
	failPrompt bool
	app        *App
	handler    http.Handler
}

func newHarnessAgentFixture(t *testing.T) *harnessAgentFixture {
	t.Helper()
	f := &harnessAgentFixture{tasks: map[string]harnessAgentTask{}, prompts: map[string]int{}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/session" {
			if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("b", 64) || r.Header.Get("Cookie") != "" {
				t.Error("bootstrap authentication leaked or missing")
			}
			writeJSON(w, 200, map[string]string{"cookie": "dsh-auth-test=private; HttpOnly; Path=/"})
			return
		}
		if r.Host != "public.example" || r.Header.Get("Origin") != "https://public.example" || r.Header.Get("Cookie") != "dsh-auth-test=private" || r.Header.Get("Authorization") != "" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("untrusted credentials/headers forwarded to Harness")
		}
		if r.URL.Path == "/api/gateway-files/manage" {
			writeJSON(w, 200, map[string]any{"ok": true, "value": map[string]string{"name": "note.txt"}})
			return
		}
		if r.URL.Path == "/api/gateway-files/download" {
			if r.URL.Query().Get("path") != "note.txt" {
				t.Error("file path changed")
			}
			w.Header().Set("Set-Cookie", "should-not-leak=1")
			w.Header().Set("Content-Disposition", "attachment; filename=note.txt")
			io.WriteString(w, "file fixture")
			return
		}
		var envelope struct {
			Method  string `json:"method"`
			Payload struct {
				Args struct {
					Request struct {
						ID        string `json:"sessionId"`
						Cwd       string `json:"cwd"`
						RequestID string `json:"requestId"`
					} `json:"request"`
				} `json:"args"`
			} `json:"payload"`
		}
		if json.NewDecoder(r.Body).Decode(&envelope) != nil {
			t.Error("invalid RPC envelope")
		}
		f.requests = append(f.requests, envelope.Method)
		request := envelope.Payload.Args.Request
		var value any = map[string]any{}
		switch envelope.Method {
		case "session/list":
			items := []harnessAgentTask{}
			for _, task := range f.tasks {
				items = append(items, task)
			}
			value = map[string]any{"items": items}
		case "session/create":
			f.tasks[request.ID] = harnessAgentTask{ID: request.ID, Cwd: request.Cwd, Blank: true}
			value = map[string]string{"sessionId": request.ID}
		case "session/prompt":
			if f.failPrompt {
				w.WriteHeader(503)
				return
			}
			f.prompts[request.RequestID]++
			value = map[string]bool{"accepted": true}
		case "session/cancel", "session/rename", "session/selectModel":
			value = map[string]bool{"accepted": true}
		case "session/projections":
			value = map[string]any{"asOfSeq": 4, "values": map[string]any{"inbox": map[string]any{"next-turn": []any{}, "next-step": []any{}}}}
		case "session/page":
			value = map[string]any{"records": []any{
				map[string]any{"type": "event", "event": map[string]any{"seq": 1, "time": 1000, "type": "turn/start", "data": map[string]any{"turn": 1}}},
				map[string]any{"type": "event", "event": map[string]any{"seq": 2, "time": 1500, "type": "assistant/message", "data": map[string]any{"message": map[string]any{"content": []map[string]string{{"type": "text", "text": "Agent fixture completed."}}}}}},
				map[string]any{"type": "event", "event": map[string]any{"seq": 3, "time": 2000, "type": "turn/end", "data": map[string]any{"reason": map[string]string{"kind": "completed"}}}},
			}, "hasMore": false}
		case "session/modelCatalog":
			value = map[string]any{"groups": []any{map[string]any{"id": "ollama-local", "models": []any{map[string]string{"id": "fixture"}}}}}
		default:
			t.Errorf("unexpected RPC method: %s", envelope.Method)
		}
		writeJSON(w, 200, map[string]any{"result": map[string]any{"ok": true, "value": value}})
	}))
	t.Cleanup(upstream.Close)
	f.app = &App{cfg: Config{PublicURL: harnessURL(t, "https://public.example"), Harness: config.HarnessConfig{
		Upstream: harnessURL(t, upstream.URL), BootstrapURL: harnessURL(t, upstream.URL), BootstrapKey: strings.Repeat("b", 64), AgentAPIKey: strings.Repeat("a", 64),
	}}}
	f.handler = f.app.harnessAgentAPI()
	return f
}

func (f *harnessAgentFixture) call(method, suffix, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, harnessAgentMount+suffix, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+f.app.cfg.Harness.AgentAPIKey)
	r.Header.Set("Cookie", "gateway_session=must-not-leak")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func TestHarnessAgentAPIAuthAndRouting(t *testing.T) {
	f := newHarnessAgentFixture(t)
	for _, tc := range []struct {
		name, key, origin, query string
		status                   int
	}{
		{"missing", "", "", "", 401}, {"wrong", "wrong", "", "", 401},
		{"origin", strings.Repeat("a", 64), "https://evil.example", "", 403},
		{"query credential", strings.Repeat("a", 64), "", "?token=secret", 400},
		{"valid", strings.Repeat("a", 64), "", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", harnessAgentMount+"models"+tc.query, nil)
			if tc.key != "" {
				r.Header.Set("Authorization", "Bearer "+tc.key)
			}
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("private response headers")
			}
		})
	}
	f.app.cfg.Harness.AgentAPIKey = ""
	w := f.call("GET", "models", "", "")
	if w.Code != 404 {
		t.Fatal("disabled agent API reachable")
	}
}

func TestHarnessAgentAPITaskLifecycle(t *testing.T) {
	f := newHarnessAgentFixture(t)
	w := f.call("POST", "tasks", `{"prompt":"Synthetic task","model":"fixture","title":"API test"}`, "create-task-0001")
	if w.Code != 202 {
		t.Fatalf("create %d: %s", w.Code, w.Body.String())
	}
	id := w.Header().Get("X-Harness-Task-ID")
	if !harnessAgentID.MatchString(id) {
		t.Fatal("task ID missing")
	}
	w = f.call("POST", "tasks", `{"prompt":"Do not submit this duplicate"}`, "create-task-0001")
	if w.Code != 200 || len(f.prompts) != 1 {
		t.Fatal("create retry dispatched another prompt")
	}
	w = f.call("GET", "tasks/"+id, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"completed"`) || !strings.Contains(w.Body.String(), "Agent fixture completed.") {
		t.Fatalf("status %s", w.Body.String())
	}
	w = f.call("GET", "tasks/"+id+"/events?max_messages=10", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"through_seq":4`) {
		t.Fatal("events unavailable")
	}
	w = f.call("POST", "tasks/"+id+"/messages", `{"prompt":"Continue the synthetic task"}`, "followup-0001")
	if w.Code != 202 || len(f.prompts) != 2 {
		t.Fatal("follow-up not submitted")
	}
	w = f.call("POST", "tasks/"+id+"/cancel", "", "")
	if w.Code != 202 {
		t.Fatal("cancellation not admitted")
	}
	f.tasks["browser-session"] = harnessAgentTask{ID: "browser-session", Cwd: "/workspace"}
	foreign := "hapi-" + strings.Repeat("f", 32)
	f.tasks[foreign] = harnessAgentTask{ID: foreign, Cwd: "/workspace"}
	w = f.call("GET", "tasks", "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "browser-session") || strings.Contains(w.Body.String(), foreign) {
		t.Fatal("non-API sessions exposed")
	}
	w = f.call("GET", "tasks/"+foreign, "", "")
	if w.Code != 404 {
		t.Fatal("foreign workspace admitted")
	}
	w = f.call("POST", "tasks/"+id+"/files", fmt.Sprintf(`{"name":"note.txt","data_base64":%q}`, base64.StdEncoding.EncodeToString([]byte("file fixture"))), "")
	if w.Code != 201 {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	w = f.call("GET", "tasks/"+id+"/files?path=note.txt", "", "")
	if w.Code != 200 || w.Body.String() != "file fixture" || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("download failed or cookie leaked")
	}
}

func TestHarnessAgentAPIInvalidRequests(t *testing.T) {
	f := newHarnessAgentFixture(t)
	id := "hapi-" + strings.Repeat("1", 32)
	f.tasks[id] = harnessAgentTask{ID: id, Cwd: harnessAgentCwd(id)}
	for _, tc := range []struct {
		method, path, body, key string
		status                  int
	}{
		{"POST", "tasks", `{}`, "", 400}, {"POST", "tasks", `{"extra":1}`, "create-0001", 400},
		{"POST", "tasks", `{} {}`, "create-0001", 400}, {"POST", "tasks", `{"preset":"arbitrary"}`, "create-0001", 400},
		{"POST", "tasks/" + id + "/messages", `{"prompt":" "}`, "message-001", 400},
		{"GET", "tasks/" + id + "/events?max_messages=0", "", "", 400},
		{"GET", "tasks/" + id + "/events?through_seq=100", "", "", 400},
		{"GET", "tasks/" + id + "/files?path=../secret", "", "", 400},
		{"GET", "tasks/" + id + "/files?path=/data/secret", "", "", 400},
		{"GET", "tasks?limit=0", "", "", 400}, {"GET", "tasks?limit=1&limit=2", "", "", 400},
		{"POST", "tasks/" + id + "/cancel", `{}`, "", 400},
		{"POST", "tasks/" + id + "/files", `{"name":"../secret","data_base64":""}`, "", 400},
		{"POST", "tasks/" + id + "/files", `{"name":"note.txt","data_base64":"AAAA\n"}`, "", 400},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			w := f.call(tc.method, tc.path, tc.body, tc.key)
			if w.Code != tc.status {
				t.Fatalf("status %d, wanted %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func TestHarnessAgentAPIUnknownSubmissionIsNotRetried(t *testing.T) {
	f := newHarnessAgentFixture(t)
	f.failPrompt = true
	w := f.call("POST", "tasks", `{"prompt":"Synthetic ambiguous submission"}`, "ambiguous-0001")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "harness_outcome_unknown") {
		t.Fatal("unknown outcome hidden")
	}
	w = f.call("POST", "tasks", `{"prompt":"Synthetic ambiguous submission"}`, "ambiguous-0001")
	if w.Code != 200 {
		t.Fatal("durable session not recovered")
	}
	calls := 0
	for _, method := range f.requests {
		if method == "session/prompt" {
			calls++
		}
	}
	if calls != 1 {
		t.Fatal("ambiguous HTTP submission replayed")
	}
}

func TestHarnessAgentStatusDoesNotCallErrorsCompleted(t *testing.T) {
	for _, tc := range []struct{ reason, state string }{{"completed", "completed"}, {"error", "failed"}, {"aborted", "cancelled"}, {"max-tokens", "output_limit"}} {
		raw := json.RawMessage(fmt.Sprintf(`{"event":{"type":"turn/end","seq":5,"data":{"reason":{"kind":%q}}}}`, tc.reason))
		result := harnessAgentResult(harnessAgentTask{}, harnessAgentPage{Records: []json.RawMessage{raw}})
		if result["state"] != tc.state {
			t.Fatalf("%s: %v", tc.reason, result)
		}
	}
}
