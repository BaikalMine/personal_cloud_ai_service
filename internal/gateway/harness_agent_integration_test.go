package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/config"
)

// This fixture runs the installed Harness against a synthetic model, never the GPU.
func TestHarnessAgentAPIIntegration(t *testing.T) {
	upstream, bootstrap := os.Getenv("HARNESS_AGENT_TEST_UPSTREAM"), os.Getenv("HARNESS_AGENT_TEST_BOOTSTRAP")
	if upstream == "" || bootstrap == "" {
		t.Skip("requires an isolated Harness fixture")
	}
	app := &App{cfg: Config{PublicURL: harnessURL(t, "http://agent-api-fixture:3099"), Harness: config.HarnessConfig{
		Upstream: harnessURL(t, upstream), BootstrapURL: harnessURL(t, bootstrap), BootstrapKey: strings.Repeat("b", 64), AgentAPIKey: strings.Repeat("a", 64),
	}}}
	f := &harnessAgentFixture{app: app, handler: app.harnessAgentAPI()}
	decode := func(w *httptest.ResponseRecorder, status int) map[string]any {
		t.Helper()
		if w.Code != status {
			t.Fatalf("HTTP %d, want %d: %s", w.Code, status, w.Body.String())
		}
		var result map[string]any
		if json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal("invalid fixture response")
		}
		return result
	}
	decode(f.call("GET", "models", "", ""), 200)
	created := decode(f.call("POST", "tasks", `{"model":"fixture-model","title":"Synthetic agent API test"}`, "native-fixture-create-001"), 202)
	id := created["id"].(string)
	decode(f.call("POST", "tasks/"+id+"/files", `{"name":"note.txt","data_base64":"`+base64.StdEncoding.EncodeToString([]byte("Synthetic API file."))+`"}`, ""), 201)
	download := f.call("GET", "tasks/"+id+"/files?path=note.txt", "", "")
	if download.Code != 200 || download.Body.String() != "Synthetic API file." {
		t.Fatalf("native file download %d: %s", download.Code, download.Body.String())
	}
	decode(f.call("POST", "tasks/"+id+"/messages", `{"prompt":"tool-fixture: run the harmless synthetic workspace verification."}`, "native-fixture-message-001"), 202)
	wait := func(id, expected string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			result := decode(f.call("GET", "tasks/"+id, "", ""), 200)
			if result["state"] == expected {
				return result
			}
			if result["state"] == "failed" {
				t.Fatalf("native task failed: %v", result)
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("native task did not reach %s", expected)
		return nil
	}
	result := wait(id, "completed")
	if !strings.Contains(result["answer"].(string), "Harness tool verification complete.") {
		t.Fatalf("tool result absent: %v", result)
	}
	events := f.call("GET", "tasks/"+id+"/events", "", "")
	if events.Code != 200 || !strings.Contains(events.Body.String(), "tool/result") {
		t.Fatal("native tool history not returned")
	}
	decode(f.call("POST", "tasks", `{}`, "native-fixture-create-001"), 200)
	// A fresh Gateway API instance reuses durable native sessions, not an in-memory job map.
	fresh := &App{cfg: app.cfg}
	f.app, f.handler = fresh, fresh.harnessAgentAPI()
	wait(id, "completed")
	decode(f.call("GET", "tasks?limit=1", "", ""), 200)
	cancelled := decode(f.call("POST", "tasks", `{"prompt":"cancel-fixture","model":"fixture-model"}`, "native-fixture-cancel-001"), 202)
	cancelID := cancelled["id"].(string)
	wait(cancelID, "running")
	time.Sleep(500 * time.Millisecond)
	decode(f.call("POST", "tasks/"+cancelID+"/cancel", "", ""), 202)
	wait(cancelID, "cancelled")
	t.Log("Installed Harness: models, persistent tasks, native shell tool, files, events, idempotency and cancellation passed without GPU")
}
