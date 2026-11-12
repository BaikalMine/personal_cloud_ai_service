package inference

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/modelruntime"
)

func harnessFixture(t *testing.T) *ollamaHTTPFixture {
	x := newOllamaHTTPFixture(t)
	x.api.config.AdmissionURL = strings.Replace(x.api.config.AdmissionURL, "/ollama/permit", "/harness/permit", 1)
	return x
}

func harnessLifecycle(t *testing.T, x *ollamaHTTPFixture, action, task, session string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"action": action, "taskId": task, "sessionId": session})
	r, _ := http.NewRequest("POST", x.server.URL+harnessTaskRoute, strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+x.api.config.ClientKey)
	response, err := x.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func harnessChat(t *testing.T, x *ollamaHTTPFixture, session string) *http.Response {
	t.Helper()
	r, _ := http.NewRequest("POST", x.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"fixture","stream":true,"messages":[]}`))
	r.Header.Set("Authorization", "Bearer "+x.api.config.ClientKey)
	r.Header.Set(harnessSessionHeader, session)
	response, err := x.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestHarnessTaskRetainsPermitAcrossToolsAndDelegation(t *testing.T) {
	x := harnessFixture(t)
	x.runtime.handle = func(ctx context.Context, input modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
		x.assertRunning(t)
		if !input.Retain {
			t.Fatal("Harness runtime not retained")
		}
		err := consume(200, http.Header{"Content-Type": {"text/event-stream"}}, strings.NewReader("data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		return modelruntime.Result{Status: 200, Dispatched: true, Retained: true}, err
	}
	if harnessLifecycle(t, x, "begin", "task-parent", "session-parent") != 200 {
		t.Fatal("begin refused")
	}
	permit := x.assertRunning(t)
	for i := 0; i < 3; i++ {
		response := harnessChat(t, x, "session-parent")
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !strings.Contains(string(body), "[DONE]") {
			t.Fatal("stream incomplete", err)
		}
		if x.assertRunning(t).ID != permit.ID {
			t.Fatal("permit changed between steps")
		}
		if x.runtime.stopCalls.Load() != 0 {
			t.Fatal("runtime stopped between tools")
		}
	}
	ordinary := x.request(t, context.Background(), "/api/chat")
	ordinary.Body.Close()
	if ordinary.StatusCode != 409 {
		t.Fatal("ordinary request overlapped task")
	}
	if worked, err := x.worker.RunOne(context.Background()); worked || err != nil {
		t.Fatal("managed inference overlapped task", err)
	}
	if harnessLifecycle(t, x, "begin", "task-child", "session-child") != 200 {
		t.Fatal("delegation refused")
	}
	if harnessLifecycle(t, x, "end", "task-parent", "session-parent") != 200 {
		t.Fatal("parent end refused")
	}
	x.assertRunning(t)
	if harnessLifecycle(t, x, "touch", "task-child", "session-child") != 200 {
		t.Fatal("heartbeat refused")
	}
	if harnessLifecycle(t, x, "end", "task-child", "session-child") != 200 {
		t.Fatal("end refused")
	}
	if x.runtime.stopCalls.Load() != 1 {
		t.Fatal("physical stop not performed exactly once")
	}
	if _, err := x.store.ActiveGPUExecutorPermit(context.Background(), permit.ResourceID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("permit still active", err)
	}
	x.runtime.handle = func(_ context.Context, input modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
		if input.Retain {
			t.Fatal("ordinary request acquired retention")
		}
		err := consume(200, http.Header{}, strings.NewReader(`{"done":true}`))
		return modelruntime.Result{Status: 200, Dispatched: true, Stopped: true}, err
	}
	normal := x.request(t, context.Background(), "/api/chat")
	_, err := io.Copy(io.Discard, normal.Body)
	normal.Body.Close()
	if err != nil || normal.StatusCode != 200 {
		t.Fatal("ordinary request did not recover", err)
	}
}

func TestHarnessTaskCancelStopsInFlightRequest(t *testing.T) {
	x := harnessFixture(t)
	started := make(chan struct{})
	x.runtime.handle = func(ctx context.Context, _ modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
		close(started)
		<-ctx.Done()
		return modelruntime.Result{Stopped: true, Dispatched: true}, ctx.Err()
	}
	if harnessLifecycle(t, x, "begin", "task-cancel", "session-cancel") != 200 {
		t.Fatal("begin")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		response := harnessChat(t, x, "session-cancel")
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}()
	await(t, started)
	if harnessLifecycle(t, x, "end", "task-cancel", "session-cancel") != 200 {
		t.Fatal("cancel failed")
	}
	await(t, done)
	if x.runtime.stopCalls.Load() != 1 {
		t.Fatal("cancel lacked physical proof")
	}
}

func TestHarnessTaskWatchdogAndUnconfirmedStop(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(map[bool]string{true: "expired", false: "unconfirmed"}[confirmed], func(t *testing.T) {
			x := harnessFixture(t)
			if !confirmed {
				x.runtime.stopHandle = func(context.Context) (bool, error) { return false, errors.New("no physical proof") }
			}
			if harnessLifecycle(t, x, "begin", "task-expire", "session-expire") != 200 {
				t.Fatal("begin")
			}
			permit := x.assertRunning(t)
			x.api.expireHarnessTasks(time.Now().Add(harnessTaskTTL + time.Second))
			x.api.tasksMu.Lock()
			live := x.api.taskPool != nil
			x.api.tasksMu.Unlock()
			if live {
				t.Fatal("pool survived expiry")
			}
			var state string
			err := x.db.QueryRow("SELECT state FROM gpu_executor_permits WHERE id=$1", permit.ID).Scan(&state)
			if err != nil {
				t.Fatal(err)
			}
			if confirmed && state != "closed" {
				t.Fatal("watchdog did not settle")
			}
			if !confirmed && (state != "running" || x.worker.ownedReady()) {
				t.Fatal("unsafe stop cleared permit")
			}
			if x.runtime.stopCalls.Load() != 1 {
				t.Fatal("watchdog did not stop")
			}
		})
	}
}

func TestHarnessTaskIdentityAndScope(t *testing.T) {
	x := harnessFixture(t)
	if harnessLifecycle(t, x, "begin", "task-scope", "session-scope") != 200 {
		t.Fatal("begin")
	}
	permit := x.assertRunning(t)
	if harnessLifecycle(t, x, "end", "task-scope", "other-session") != 200 {
		t.Fatal("idempotent end failed")
	}
	if x.assertRunning(t).ID != permit.ID {
		t.Fatal("wrong session released task")
	}
	response := harnessChat(t, x, "forged-session")
	response.Body.Close()
	if response.StatusCode != 503 || x.runtime.httpCalls.Load() != 0 {
		t.Fatal("unregistered session dispatched")
	}
	if harnessLifecycle(t, x, "end", "task-scope", "session-scope") != 200 {
		t.Fatal("end")
	}
	x.api.config.AdmissionURL = strings.Replace(x.api.config.AdmissionURL, "/harness/permit", "/ollama/permit", 1)
	if harnessLifecycle(t, x, "begin", "task-disabled", "session-disabled") != 404 {
		t.Fatal("non-Harness retention exposed")
	}
	response = harnessChat(t, x, "session-disabled")
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal("non-Harness session header accepted")
	}
}
