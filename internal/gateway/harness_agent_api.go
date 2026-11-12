package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const harnessAgentMount = "/api/harness/v1/"
const harnessAgentPromptLimit = 1 << 20

var harnessAgentID = regexp.MustCompile(`^hapi-[a-f0-9]{32}$`)
var harnessAgentKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,128}$`)

type harnessAgentPrompt struct {
	Prompt string `json:"prompt"`
}

type harnessAgentCreate struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model,omitempty"`
	Preset string `json:"preset,omitempty"`
	Title  string `json:"title,omitempty"`
}

type harnessAgentTask struct {
	ID        string `json:"sessionId"`
	Running   bool   `json:"running"`
	Blank     bool   `json:"blank"`
	Cwd       string `json:"cwd"`
	UpdatedAt int64  `json:"updatedAt"`
}

func harnessAgentCwd(id string) string { return "/workspace/agent-api/" + id }

func harnessAgentDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func harnessAgentReadJSON(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.Header.Get("Content-Encoding") != "" {
		return &harnessAgentFault{415, "json_required"}
	}
	if r.ContentLength > limit {
		return &harnessAgentFault{413, "request_too_large"}
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &harnessAgentFault{400, "invalid_json"}
	}
	if decoder.Decode(new(any)) != io.EOF {
		return &harnessAgentFault{400, "invalid_json"}
	}
	return nil
}

func harnessAgentIdempotency(r *http.Request) (string, error) {
	if len(r.Header.Values("Idempotency-Key")) != 1 || !harnessAgentKey.MatchString(r.Header.Get("Idempotency-Key")) {
		return "", &harnessAgentFault{400, "idempotency_key_required"}
	}
	return r.Header.Get("Idempotency-Key"), nil
}

func (a *App) harnessAgentAPI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		key := a.cfg.Harness.AgentAPIKey
		if !a.cfg.Harness.Enabled() || a.cfg.RestoreDrillMode || len(key) < 32 {
			http.NotFound(w, r)
			return
		}
		if len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+key)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="harness-agent"`)
			harnessAgentWriteError(w, &harnessAgentFault{401, "unauthorized"}, "")
			return
		}
		if r.Header.Get("Origin") != "" || strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
			harnessAgentWriteError(w, &harnessAgentFault{403, "service_client_required"}, "")
			return
		}
		if r.URL.RawPath != "" || path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") {
			http.NotFound(w, r)
			return
		}
		a.harnessAgentOnce.Do(func() { a.harnessAgentSlots = make(chan struct{}, 4) })
		select {
		case a.harnessAgentSlots <- struct{}{}:
			defer func() { <-a.harnessAgentSlots }()
		default:
			w.Header().Set("Retry-After", "2")
			harnessAgentWriteError(w, &harnessAgentFault{429, "api_busy"}, "")
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, harnessAgentMount), "/")
		allowedQuery := map[string]bool{}
		if len(parts) == 1 && parts[0] == "tasks" && r.Method == "GET" {
			allowedQuery["limit"], allowedQuery["cursor"] = true, true
		}
		if len(parts) == 3 && parts[2] == "events" && r.Method == "GET" {
			allowedQuery["before_seq"], allowedQuery["through_seq"], allowedQuery["max_messages"] = true, true, true
		}
		if len(parts) == 3 && parts[2] == "files" && r.Method == "GET" {
			allowedQuery["path"] = true
		}
		_, err := parseHarnessAgentQuery(r, allowedQuery)
		if err != nil {
			harnessAgentWriteError(w, err, "")
			return
		}
		client, err := a.newHarnessAgentClient(r.Context())
		if err != nil {
			harnessAgentWriteError(w, err, "")
			return
		}
		if len(parts) == 1 && parts[0] == "models" && r.Method == "GET" {
			var catalog json.RawMessage
			if err := client.rpc(r.Context(), "session/modelCatalog", map[string]any{}, &catalog); err != nil {
				harnessAgentWriteError(w, err, "")
				return
			}
			writeJSON(w, 200, catalog)
			return
		}
		if len(parts) == 1 && parts[0] == "tasks" {
			switch r.Method {
			case "POST":
				a.harnessAgentCreateTask(w, r, client)
			case "GET":
				a.harnessAgentListTasks(w, r, client)
			default:
				harnessAgentWriteError(w, &harnessAgentFault{405, "method_not_allowed"}, "")
			}
			return
		}
		if len(parts) < 2 || len(parts) > 3 || parts[0] != "tasks" || !harnessAgentID.MatchString(parts[1]) {
			http.NotFound(w, r)
			return
		}
		task, err := client.task(r, parts[1])
		if err != nil {
			harnessAgentWriteError(w, err, parts[1])
			return
		}
		if len(parts) == 2 && r.Method == "GET" {
			client.status(w, r, task)
			return
		}
		if len(parts) == 3 {
			switch {
			case parts[2] == "events" && r.Method == "GET":
				client.events(w, r, task)
				return
			case parts[2] == "messages" && r.Method == "POST":
				client.prompt(w, r, task)
				return
			case parts[2] == "cancel" && r.Method == "POST":
				if r.ContentLength != 0 {
					harnessAgentWriteError(w, &harnessAgentFault{400, "empty_body_required"}, task.ID)
					return
				}
				err := client.rpc(r.Context(), "session/cancel", harnessAgentArgs(map[string]any{"sessionId": task.ID}), nil)
				if err != nil {
					harnessAgentWriteError(w, err, task.ID)
					return
				}
				writeJSON(w, 202, map[string]any{"id": task.ID, "state": "cancelling"})
				return
			case parts[2] == "files":
				client.files(w, r, task)
				return
			}
		}
		harnessAgentWriteError(w, &harnessAgentFault{405, "method_not_allowed"}, task.ID)
	})
}

func (c *harnessAgentClient) tasks(r *http.Request) ([]harnessAgentTask, error) {
	var list struct {
		Items []harnessAgentTask `json:"items"`
	}
	if err := c.rpc(r.Context(), "session/list", map[string]any{"_request": map[string]any{}}, &list); err != nil {
		return nil, err
	}
	result := []harnessAgentTask{}
	for _, task := range list.Items {
		if harnessAgentID.MatchString(task.ID) && task.Cwd == harnessAgentCwd(task.ID) {
			result = append(result, task)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID > result[j].ID })
	return result, nil
}

func (c *harnessAgentClient) task(r *http.Request, id string) (harnessAgentTask, error) {
	tasks, err := c.tasks(r)
	if err != nil {
		return harnessAgentTask{}, err
	}
	for _, task := range tasks {
		if task.ID == id {
			return task, nil
		}
	}
	return harnessAgentTask{}, &harnessAgentFault{404, "task_not_found"}
}

func (a *App) harnessAgentCreateTask(w http.ResponseWriter, r *http.Request, client *harnessAgentClient) {
	key, err := harnessAgentIdempotency(r)
	if err != nil {
		harnessAgentWriteError(w, err, "")
		return
	}
	var input harnessAgentCreate
	if err := harnessAgentReadJSON(w, r, 8<<20, &input); err != nil {
		harnessAgentWriteError(w, err, "")
		return
	}
	if len(input.Prompt) > harnessAgentPromptLimit || len(input.Model) > 256 || len(input.Title) > 200 || strings.IndexByte(input.Prompt+input.Title, 0) >= 0 ||
		(input.Preset != "" && input.Preset != "ptc" && input.Preset != "standard" && input.Preset != "cordis") {
		harnessAgentWriteError(w, &harnessAgentFault{400, "invalid_task"}, "")
		return
	}
	id := "hapi-" + harnessAgentDigest("create\x00"+key)
	w.Header().Set("Location", harnessAgentMount+"tasks/"+id)
	w.Header().Set("X-Harness-Task-ID", id)
	a.harnessAgentMu.Lock()
	defer a.harnessAgentMu.Unlock()
	if _, err := client.task(r, id); err == nil {
		writeJSON(w, 200, map[string]any{"id": id, "replayed": true, "status_url": harnessAgentMount + "tasks/" + id})
		return
	} else if fault, ok := err.(*harnessAgentFault); !ok || fault.Status != 404 {
		harnessAgentWriteError(w, err, id)
		return
	}
	request := map[string]any{"sessionId": id, "cwd": harnessAgentCwd(id)}
	if input.Preset != "" {
		request["agentPreset"] = input.Preset
	}
	if err := client.rpc(r.Context(), "session/create", harnessAgentArgs(request), nil); err != nil {
		harnessAgentWriteError(w, err, id)
		return
	}
	if input.Model != "" {
		if err := client.rpc(r.Context(), "session/selectModel", harnessAgentArgs(map[string]any{"sessionId": id, "provider": "ollama-local", "model": input.Model}), nil); err != nil {
			harnessAgentWriteError(w, err, id)
			return
		}
	}
	if input.Title != "" {
		if err := client.rpc(r.Context(), "session/rename", harnessAgentArgs(map[string]any{"sessionId": id, "title": input.Title}), nil); err != nil {
			harnessAgentWriteError(w, err, id)
			return
		}
	}
	state := "created"
	if strings.TrimSpace(input.Prompt) != "" {
		if err := client.sendPrompt(r, id, id+"-initial", input.Prompt); err != nil {
			harnessAgentWriteError(w, err, id)
			return
		}
		state = "accepted"
	}
	writeJSON(w, 202, map[string]any{"id": id, "state": state, "status_url": harnessAgentMount + "tasks/" + id, "workspace": harnessAgentCwd(id)})
}

func (c *harnessAgentClient) sendPrompt(r *http.Request, id, requestID, text string) error {
	return c.rpc(r.Context(), "session/prompt", harnessAgentArgs(map[string]any{
		"sessionId": id, "requestId": requestID, "mode": "queue", "clientTimeZone": "Europe/Moscow",
		"content": []map[string]string{{"type": "text", "text": text}},
	}), nil)
}

func (c *harnessAgentClient) prompt(w http.ResponseWriter, r *http.Request, task harnessAgentTask) {
	key, err := harnessAgentIdempotency(r)
	if err != nil {
		harnessAgentWriteError(w, err, task.ID)
		return
	}
	var input harnessAgentPrompt
	if err := harnessAgentReadJSON(w, r, 8<<20, &input); err != nil {
		harnessAgentWriteError(w, err, task.ID)
		return
	}
	if strings.TrimSpace(input.Prompt) == "" || len(input.Prompt) > harnessAgentPromptLimit || strings.IndexByte(input.Prompt, 0) >= 0 {
		harnessAgentWriteError(w, &harnessAgentFault{400, "invalid_prompt"}, task.ID)
		return
	}
	requestID := "hapi-msg-" + harnessAgentDigest(task.ID+"\x00"+key)
	// Native prompt admission deduplicates the durable requestId, including after reconnect.
	c.app.harnessAgentMu.Lock()
	defer c.app.harnessAgentMu.Unlock()
	if err := c.sendPrompt(r, task.ID, requestID, input.Prompt); err != nil {
		harnessAgentWriteError(w, err, task.ID)
		return
	}
	writeJSON(w, 202, map[string]any{"id": task.ID, "request_id": requestID, "state": "accepted"})
}

func parseHarnessAgentQuery(r *http.Request, allowed map[string]bool) (map[string][]string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || r.URL.ForceQuery {
		return nil, &harnessAgentFault{400, "invalid_query"}
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			return nil, &harnessAgentFault{400, "invalid_query"}
		}
	}
	return query, nil
}

func harnessAgentNumber(r *http.Request, key string, fallback, minimum, maximum int64) (int64, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback, nil
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number < minimum || number > maximum {
		return 0, &harnessAgentFault{400, "invalid_" + key}
	}
	return number, nil
}

func (a *App) harnessAgentListTasks(w http.ResponseWriter, r *http.Request, client *harnessAgentClient) {
	limit, err := harnessAgentNumber(r, "limit", 50, 1, 100)
	if err != nil {
		harnessAgentWriteError(w, err, "")
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && !harnessAgentID.MatchString(cursor) {
		harnessAgentWriteError(w, &harnessAgentFault{400, "invalid_cursor"}, "")
		return
	}
	tasks, err := client.tasks(r)
	if err != nil {
		harnessAgentWriteError(w, err, "")
		return
	}
	items := []map[string]any{}
	next := ""
	for _, task := range tasks {
		if cursor != "" && task.ID >= cursor {
			continue
		}
		if int64(len(items)) == limit {
			next = fmt.Sprint(items[len(items)-1]["id"])
			break
		}
		items = append(items, map[string]any{"id": task.ID, "running": task.Running, "blank": task.Blank, "updated_at": task.UpdatedAt})
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}
