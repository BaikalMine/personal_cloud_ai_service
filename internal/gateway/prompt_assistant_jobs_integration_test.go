package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/inference"
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

type durableAssistantFixture struct {
	*dispatchFixture
	job      domain.PromptAssistantJob
	input    domain.PromptAssistantJob
	snapshot promptAssistantJobSnapshot
	worker   *inference.Worker
	base     *url.URL
	calls    atomic.Int32
}

func newDurableAssistantFixture(t *testing.T, respond http.HandlerFunc) *durableAssistantFixture {
	t.Helper()
	f := &durableAssistantFixture{dispatchFixture: gpuDispatchFixture(t)}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			writeJSON(w, 200, map[string]any{"models": []any{}})
			return
		}
		f.calls.Add(1)
		var payload struct {
			Model    string                    `json:"model"`
			Messages []promptassistant.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "saved-text" || len(payload.Messages) != 2 || !strings.Contains(payload.Messages[1].Content, "green vase") {
			t.Errorf("request snapshot lost: %+v", payload)
		}
		if respond != nil {
			respond(w, r)
			return
		}
		writeJSON(w, 200, map[string]any{"done": true, "message": map[string]string{"content": `{"prompt":"A green vase in daylight.","references":[]}`}, "eval_count": 12})
	}))
	t.Cleanup(model.Close)
	f.base, _ = url.Parse(model.URL)
	f.app.promptAssistant = promptassistant.NewClient(f.base, "saved-text")
	f.app.promptAssistantSlots = make(chan struct{}, 1)
	f.app.cfg.MediaInFlightLimitBytes = 256 << 20
	f.app.cfg.PromptAssistantDurableJobs = true
	f.app.csrfSigner = security.NewCSRFSigner("assistant-fixture-csrf-secret")
	var err error
	f.worker, err = inference.NewWorker(f.app.store, f.app.contentCipher, f.base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.worker.Close)
	prepared, err := f.app.promptAssistant.PrepareEnhancement(promptassistant.ModeTextToImage, promptassistant.ProfileWorkflowDefault, "A green vase on a desk", nil, promptassistant.VideoContext{}, false)
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot = promptAssistantJobSnapshot{Request: prepared, Original: "A green vase on a desk", Profile: promptassistant.ProfileWorkflowDefault, Context: "saved-panel-context"}
	f.input, err = f.app.makePromptAssistantJob(newRequestID(), f.user.ID, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f.job, err = f.app.store.EnqueuePromptAssistantJob(context.Background(), f.input)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *durableAssistantFixture) step(t *testing.T) domain.PromptAssistantJob {
	t.Helper()
	if _, err := f.db.Exec(`UPDATE prompt_assistant_jobs SET available_at=now() WHERE id=$1`, f.job.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, err := f.app.refreshPromptAssistantJobs(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := f.app.store.PromptAssistantJob(ctx, f.user.ID, f.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func (f *durableAssistantFixture) restart() {
	old := f.app
	f.app = &App{cfg: old.cfg, store: store.New(f.db), contentCipher: old.contentCipher,
		promptAssistant: promptassistant.NewClient(f.base, "changed-model"), promptAssistantSlots: make(chan struct{}, 1), csrfSigner: old.csrfSigner}
	f.app.gpuCoordinator = &gpuCoordinator{app: f.app}
	f.app.cfg.PromptAssistantDurableJobs = false
	f.app.cfg.MediaInFlightLimitBytes = 1
	f.app.promptAssistantSlots <- struct{}{}
}

func (f *durableAssistantFixture) request(t *testing.T, method, path string, form url.Values, user User) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "assistant-session"})
	r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &user))
	w := httptest.NewRecorder()
	if strings.HasSuffix(path, "/cancel") {
		f.app.handleCancelPromptAssistantJob(w, r)
	} else {
		f.app.handlePromptAssistantJobs(w, r)
	}
	return w
}

func TestDurableAssistantLiveProgressRecovery(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f := newDurableAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"message":{"content":"{\"prompt\":\"A green vase"},"done":false}` + "\n"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"message":{"content":" in daylight.\",\"references\":[]}"},"done":true,"prompt_eval_count":34,"eval_count":12,"eval_duration":2000000000}` + "\n"))
	})
	job := f.step(t)
	if job.InferenceWorkID == "" {
		t.Fatalf("not admitted: %s", job.State)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.worker.RunOne(ctx); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	var view promptAssistantJobView
	for time.Now().Before(deadline) {
		w := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+job.ID, nil, f.user)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.Progress != nil && view.Progress.Prompt == "A green vase" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if view.Progress == nil || view.Progress.Prompt != "A green vase" || view.Progress.StartedAt == nil || len(view.Result) != 0 {
		t.Fatalf("missing live progress: %+v", view)
	}
	var cipher []byte
	if err := f.db.QueryRow(`SELECT cipher FROM gpu_inference_progress WHERE id=$1`, job.InferenceWorkID).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte("green vase")) {
		t.Fatal("plaintext progress stored")
	}
	foreign := f.user
	foreign.ID++
	if w := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+job.ID, nil, foreign); w.Code != 404 {
		t.Fatal("foreign progress visible")
	}
	f.restart()
	w := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+job.ID, nil, f.user)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "A green vase") {
		t.Fatal("progress lost on reconnect")
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if final := f.step(t); final.State != "completed" {
		t.Fatalf("completion lost: %+v", final)
	}
	w = f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+job.ID, nil, f.user)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.State != "completed" || view.Progress.FinishedAt == nil || !bytes.Contains(view.Result, []byte(`"completion_tokens":12`)) {
		t.Fatalf("final metrics missing: %+v", view)
	}
	if f.calls.Load() != 1 {
		t.Fatal("model was dispatched twice")
	}
}

func TestDurableAssistantHTTPReceiptOwnershipAndCancel(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	form := url.Values{"csrf": {f.app.csrfSigner.Token("assistant-session")}, "template_id": {"text-to-image"}, "assistant_template": {"workflow-default"},
		"prompt": {f.snapshot.Original}, "context": {f.snapshot.Context}, "request_id": {f.job.ID}}
	for range 2 {
		w := f.request(t, http.MethodPost, "/generate/prompt-assistant/jobs", form, f.user)
		if w.Code != 202 || !strings.Contains(w.Body.String(), f.job.ID) {
			t.Fatalf("receipt: %d %s", w.Code, w.Body.String())
		}
	}
	form.Set("context", "different-input")
	if w := f.request(t, http.MethodPost, "/generate/prompt-assistant/jobs", form, f.user); w.Code != 409 {
		t.Fatalf("ID reused: %d %s", w.Code, w.Body.String())
	}
	f.app.cfg.PromptAssistantDurableJobs = false
	if w := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+f.job.ID, nil, f.user); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	foreign := f.user
	foreign.ID++
	if w := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+f.job.ID, nil, foreign); w.Code != 404 {
		t.Fatal("foreign job visible", w.Code)
	}
	cancel := url.Values{"id": {f.job.ID}}
	if w := f.request(t, http.MethodPost, "/generate/prompt-assistant/jobs/cancel", cancel, f.user); w.Code != 403 {
		t.Fatal("cancel missing CSRF", w.Code)
	}
	cancel.Set("csrf", f.app.csrfSigner.Token("assistant-session"))
	if w := f.request(t, http.MethodPost, "/generate/prompt-assistant/jobs/cancel", cancel, f.user); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if job := f.step(t); job.State != "cancelled" || job.ContentEventID != 0 || f.calls.Load() != 0 {
		t.Fatalf("cancel: %+v", job)
	}
}

func TestDurableAssistantCancelBeforeAcceptance(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	id := newRequestID()
	form := url.Values{"csrf": {f.app.csrfSigner.Token("assistant-session")}, "id": {id}}
	for range 2 {
		w := f.request(t, http.MethodPost, "/generate/prompt-assistant/jobs/cancel", form, f.user)
		if w.Code != 202 || !strings.Contains(w.Body.String(), `"state":"cancelled"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	input, err := f.app.makePromptAssistantJob(id, f.user.ID, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.EnqueuePromptAssistantJob(context.Background(), input); !errors.Is(err, store.ErrPromptAssistantConflict) {
		t.Fatal("cancelled HTTP submission accepted later", err)
	}
	foreign := f.user
	foreign.ID++
	if w := f.request(t, http.MethodPost, "/generate/prompt-assistant/jobs/cancel", form, foreign); w.Code != 409 {
		t.Fatal("foreign cancellation", w.Code, w.Body.String())
	}
	if w := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+id, nil, f.user); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"cancelled"`) {
		t.Fatal("cancellation lost on reload", w.Code, w.Body.String())
	}
	if w := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+id, nil, foreign); w.Code != 404 {
		t.Fatal("foreign cancellation exposed", w.Code, w.Body.String())
	}
	if _, err := f.db.Exec(`UPDATE gpu_work SET created_at=now()-interval '25 hours' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if w := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+id, nil, f.user); w.Code != 404 {
		t.Fatal("expired cancellation exposed", w.Code, w.Body.String())
	}
}

func TestDurableAssistantConcurrentEnqueueAndQuota(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := f.app.store.EnqueuePromptAssistantJob(ctx, f.input)
			if err != nil || job.ID != f.job.ID {
				t.Errorf("duplicate: %s %v", job.ID, err)
			}
		}()
	}
	wg.Wait()
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM prompt_assistant_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if len(f.job.RequestCipher) != 0 || bytes.Contains(f.job.MetadataCipher, []byte(f.snapshot.Original)) {
		t.Fatal("status exposes request or plaintext")
	}
	for range 3 {
		input, err := f.app.makePromptAssistantJob(newRequestID(), f.user.ID, f.snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.app.store.EnqueuePromptAssistantJob(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	input, _ := f.app.makePromptAssistantJob(newRequestID(), f.user.ID, f.snapshot)
	if _, err := f.app.store.EnqueuePromptAssistantJob(ctx, input); !errors.Is(err, store.ErrPromptAssistantQuota) {
		t.Fatal("queue quota", err)
	}
	if _, err := f.app.store.ClaimPromptAssistantJob(ctx, ""); !errors.Is(err, store.ErrGPUWorkInput) {
		t.Fatal("empty token accepted", err)
	}
}

func TestDurableAssistantRestoresResponseAndOneContentCard(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	job := f.step(t)
	if job.InferenceWorkID != job.ID || job.State != "queued" || len(job.RequestCipher) != 0 || f.calls.Load() != 0 {
		t.Fatalf("handoff: %+v", job)
	}
	var sourceBytes int
	if err := f.db.QueryRow(`SELECT octet_length(request_cipher) FROM prompt_assistant_jobs WHERE id=$1`, job.ID).Scan(&sourceBytes); err != nil || sourceBytes != 0 {
		t.Fatal(sourceBytes, err)
	}
	if worked, err := f.worker.RunOne(context.Background()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if _, err := f.db.Exec(`UPDATE gpu_inference_requests SET started_at=now()-interval '10 seconds',finished_at=now()-interval '8 seconds' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE prompt_assistant_jobs SET state='running',run_token='old-worker',created_at=now()-interval '10 minutes' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	f.restart()
	completed := f.step(t)
	if completed.State != "completed" || completed.ContentEventID == 0 {
		t.Fatalf("not recovered: %+v", completed)
	}
	w := httptest.NewRecorder()
	f.app.writePromptAssistantJob(context.Background(), w, completed, 200)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"model":"saved-text"`) || !strings.Contains(w.Body.String(), `"references":[]`) || !strings.Contains(w.Body.String(), f.snapshot.Context) {
		t.Fatal(w.Code, w.Body.String())
	}
	for range 3 {
		f.step(t)
		cached := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+job.ID, nil, f.user)
		var view promptAssistantJobView
		if err := json.Unmarshal(cached.Body.Bytes(), &view); err != nil || cached.Code != 200 || view.State != "completed" ||
			!bytes.Equal(cached.Body.Bytes(), w.Body.Bytes()) {
			t.Fatalf("cached HTTP result missing: status=%d err=%v", cached.Code, err)
		}
	}
	foreign := f.user
	foreign.ID++
	if cached := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+job.ID, nil, foreign); cached.Code != 404 || strings.Contains(cached.Body.String(), "A green vase") {
		t.Fatal("cached response exposed to another user")
	}
	var events, runs, latency int64
	if err := f.db.QueryRow(`SELECT count(*) FROM content_events WHERE correlation_id=$1 AND kind='prompt_assistant'`, job.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT count(*),max(latency_ms) FROM prompt_assistant_runs WHERE correlation_id=$1`, job.ID).Scan(&runs, &latency); err != nil {
		t.Fatal(err)
	}
	if events != 1 || runs != 1 || latency != 2000 || f.calls.Load() != 1 {
		t.Fatalf("events=%d runs=%d latency=%d calls=%d", events, runs, latency, f.calls.Load())
	}
	job.RunToken = "old-worker"
	if changed, err := f.app.store.FinishPromptAssistantJob(context.Background(), job, "cancelled", "late", nil, domain.ContentEventRecord{}, domain.PromptAssistantRunRecord{}); err != nil || changed {
		t.Fatal("stale completion accepted", changed, err)
	}
	if _, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("GPU retained", err)
	}
}

func TestDurableAssistantLostHTTPReceiptRecoversCachedResult(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	accepted, disconnected := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &f.user))
		if r.Method == http.MethodPost {
			receipt := httptest.NewRecorder()
			f.app.handlePromptAssistantJobs(receipt, r)
			if receipt.Code != http.StatusAccepted {
				t.Errorf("acceptance failed: %d", receipt.Code)
			}
			close(accepted)
			<-r.Context().Done()
			close(disconnected)
			return
		}
		f.app.handlePromptAssistantJobs(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	form := url.Values{"csrf": {f.app.csrfSigner.Token("assistant-session")}, "template_id": {"text-to-image"},
		"assistant_template": {"workflow-default"}, "prompt": {f.snapshot.Original}, "context": {f.snapshot.Context}, "request_id": {f.job.ID}}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/generate/prompt-assistant/jobs", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "assistant-session"})
	result := make(chan error, 1)
	go func() {
		response, err := server.Client().Do(r)
		if response != nil {
			response.Body.Close()
		}
		result <- err
	}()
	select {
	case <-accepted:
	case <-ctx.Done():
		t.Fatal("request was not accepted")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected lost receipt, got %v", err)
	}
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not observe socket disconnect")
	}
	f.step(t)
	if worked, err := f.worker.RunOne(context.Background()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	f.restart()
	if job := f.step(t); job.State != "completed" {
		t.Fatalf("not recovered: %s", job.State)
	}
	for range 2 {
		response, err := server.Client().Get(server.URL + "/generate/prompt-assistant/jobs?id=" + f.job.ID)
		if err != nil {
			t.Fatal(err)
		}
		var view promptAssistantJobView
		err = json.NewDecoder(response.Body).Decode(&view)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || view.ID != f.job.ID || view.State != "completed" || !strings.Contains(string(view.Result), "A green vase in daylight.") {
			t.Fatalf("cached response unavailable: status=%d err=%v", response.StatusCode, err)
		}
	}
	if f.calls.Load() != 1 {
		t.Fatalf("model executed %d times", f.calls.Load())
	}
	if _, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("GPU still reserved", err)
	}
}

func TestDurableAssistantAtomicAcceptanceAndCompletion(t *testing.T) {
	for _, stage := range []string{"acceptance", "completion"} {
		t.Run(stage, func(t *testing.T) {
			f := newDurableAssistantFixture(t, nil)
			ctx := context.Background()
			if stage == "acceptance" {
				if _, err := f.db.Exec(`ALTER TABLE prompt_assistant_jobs ADD CONSTRAINT fixture_binding_failure CHECK(inference_work_id IS NULL)`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.app.refreshPromptAssistantJobs(ctx); err == nil {
					t.Fatal("binding failure not surfaced")
				}
				var count int
				var external string
				if err := f.db.QueryRow(`SELECT count(*) FROM gpu_inference_requests`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if err := f.db.QueryRow(`SELECT external_id FROM gpu_work WHERE id=$1`, f.job.ID).Scan(&external); err != nil {
					t.Fatal(err)
				}
				if count != 0 || external != "" {
					t.Fatal("partial acceptance", count, external)
				}
				if _, err := f.db.Exec(`ALTER TABLE prompt_assistant_jobs DROP CONSTRAINT fixture_binding_failure`); err != nil {
					t.Fatal(err)
				}
			}
			f.step(t)
			if _, err := f.worker.RunOne(ctx); err != nil {
				t.Fatal(err)
			}
			if stage == "completion" {
				if _, err := f.db.Exec(`ALTER TABLE prompt_assistant_runs ADD CONSTRAINT fixture_result_failure CHECK(model<>'saved-text')`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Exec(`UPDATE prompt_assistant_jobs SET available_at=now()`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.app.refreshPromptAssistantJobs(ctx); err == nil {
					t.Fatal("metrics failure not surfaced")
				}
				var count int
				if err := f.db.QueryRow(`SELECT count(*) FROM content_events WHERE correlation_id=$1`, f.job.ID).Scan(&count); err != nil || count != 0 {
					t.Fatal("partial content commit", count, err)
				}
				if _, err := f.db.Exec(`ALTER TABLE prompt_assistant_runs DROP CONSTRAINT fixture_result_failure`); err != nil {
					t.Fatal(err)
				}
			}
			if job := f.step(t); job.State != "completed" || f.calls.Load() != 1 {
				t.Fatalf("recovery: %+v calls=%d", job, f.calls.Load())
			}
		})
	}
}

func TestDurableAssistantCancelBeforeAndDuringDispatch(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "during"}[during], func(t *testing.T) {
			entered, reply := make(chan struct{}), make(chan struct{})
			var once sync.Once
			f := newDurableAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-reply
				writeJSON(w, 200, map[string]any{"done": true, "message": map[string]string{"content": `{"prompt":"A green vase.","references":[]}`}})
			})
			t.Cleanup(func() { once.Do(func() { close(reply) }) })
			job := f.step(t)
			done := make(chan error, 1)
			if during {
				go func() { _, err := f.worker.RunOne(context.Background()); done <- err }()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("executor did not enter")
				}
			}
			if err := f.app.store.CancelPromptAssistantJob(context.Background(), f.user.ID, job.ID); err != nil {
				t.Fatal(err)
			}
			cancelled := f.step(t)
			if cancelled.State != "cancelled" || cancelled.ContentEventID != 0 || len(cancelled.ResultCipher) != 0 {
				t.Fatalf("cancel lost: %+v", cancelled)
			}
			if _, err := f.app.refreshManagedGPUWork(context.Background()); err != nil {
				t.Fatal(err)
			}
			if during {
				if _, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); err != nil {
					t.Fatal("running GPU released", err)
				}
				once.Do(func() { close(reply) })
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.worker.RunOne(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.app.refreshManagedGPUWork(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("settled GPU retained", err)
			}
			if final := f.step(t); final.State != "cancelled" || final.ContentEventID != 0 {
				t.Fatal("late answer resurrected source", final)
			}
			want := int32(0)
			if during {
				want = 1
			}
			if f.calls.Load() != want {
				t.Fatal("unexpected calls", f.calls.Load())
			}
		})
	}
}

func TestDurableAssistantUnknownResponseNeverReplays(t *testing.T) {
	f := newDurableAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fixture timeout", 504) })
	job := f.step(t)
	if _, err := f.worker.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.restart()
	for range 4 {
		if current := f.step(t); current.State != "queued" || current.InferenceWorkID != job.ID {
			t.Fatalf("unknown reply lost: %+v", current)
		}
	}
	if err := f.app.store.CancelPromptAssistantJob(context.Background(), f.user.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	if _, err := f.app.refreshManagedGPUWork(context.Background()); err != nil {
		t.Fatal(err)
	}
	if work, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); err != nil || work.ID != job.ID || f.calls.Load() != 1 {
		t.Fatalf("unknown inference released/replayed: %+v calls=%d %v", work, f.calls.Load(), err)
	}
}

func TestDurableAssistantRevokedOwnerFencesOfflineExecutor(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	job := f.step(t)
	if _, err := f.db.Exec(`UPDATE users SET role='user',can_generate_text_to_image=false WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.refreshManagedGPUWork(context.Background()); err != nil {
		t.Fatal(err)
	}
	request, err := f.app.store.GPUInference(context.Background(), job.ID)
	if err != nil || request.State != "not_dispatched" {
		t.Fatal(request, err)
	}
	if _, err := f.worker.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 0 {
		t.Fatal("revoked owner dispatched")
	}
}

func TestDurableAssistantFailedResponseIsTerminal(t *testing.T) {
	f := newDurableAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"done": true, "message": map[string]string{"content": `{"prompt":"","references":[]}`}})
	})
	f.step(t)
	if _, err := f.worker.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	job := f.step(t)
	if job.State != "failed" || job.ContentEventID == 0 {
		t.Fatalf("failure not saved: %+v", job)
	}
	var status string
	if err := f.db.QueryRow(`SELECT status FROM prompt_assistant_runs WHERE correlation_id=$1`, job.ID).Scan(&status); err != nil || status != "error" {
		t.Fatal(status, err)
	}
	if _, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("failed response retains GPU", err)
	}
}

func TestDurableAssistantBlockedResponseDoesNotPoisonQueue(t *testing.T) {
	var calls atomic.Int32
	f := newDurableAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		prompt := "A green vase in daylight."
		if calls.Add(1) == 1 {
			// Synthetic safety fixture, never sent to a real model.
			prompt = "explicit nude schoolgirl portrait"
		}
		payload, err := json.Marshal(map[string]any{"prompt": prompt, "references": []any{}})
		if err != nil {
			t.Fatal(err)
		}
		writeJSON(w, 200, map[string]any{"done": true, "message": map[string]string{"content": string(payload)}})
	})
	ctx := context.Background()
	f.step(t)
	if _, err := f.worker.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	job := f.step(t)
	if job.State != "failed" || job.ContentEventID == 0 || len(job.ResultCipher) != 0 {
		t.Fatalf("blocked response did not become terminal: %+v", job)
	}
	var state, status, code string
	if err := f.db.QueryRow(`SELECT c.generation_state,r.status,r.error_code FROM content_events c JOIN prompt_assistant_runs r ON r.correlation_id=c.correlation_id WHERE c.id=$1`, job.ContentEventID).Scan(&state, &status, &code); err != nil || state != "error" || status != "blocked" || code != "unsafe_model_output" {
		t.Fatal("blocked outcome was not preserved", state, status, code, err)
	}
	if _, err := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("blocked response retains GPU", err)
	}
	if final := f.step(t); final.State != "failed" || f.calls.Load() != 1 {
		t.Fatal("terminal blocked response retried")
	}
	input, err := f.app.makePromptAssistantJob(newRequestID(), f.user.ID, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f.job, err = f.app.store.EnqueuePromptAssistantJob(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	f.step(t)
	if _, err = f.worker.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	if next := f.step(t); next.State != "completed" || f.calls.Load() != 2 {
		t.Fatalf("next safe request could not complete: %+v calls=%d", next, f.calls.Load())
	}
}

func TestDurableAssistantOwnedStoppedIsTerminal(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	f.step(t)
	finishOwnedFixtureInference(t, f.app.store)
	job := f.step(t)
	if job.State != "failed" || job.ContentEventID == 0 || f.calls.Load() != 0 {
		t.Fatalf("stopped assistant: %+v calls=%d", job, f.calls.Load())
	}
	if _, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stopped assistant retains GPU: %v", err)
	}
}

func TestDurableAssistantPreparationRecovery(t *testing.T) {
	for _, phase := range []string{"reserved", "resources_ready", "preparing_resources"} {
		t.Run(phase, func(t *testing.T) {
			f := newDurableAssistantFixture(t, nil)
			ctx := context.Background()
			source, err := f.app.store.ClaimPromptAssistantJob(ctx, "previous-source")
			if err != nil || source.ID != f.job.ID {
				t.Fatal(source, err)
			}
			work, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, domain.PromptAssistantGPUKey(source.ID), &f.user, source.RunToken, source.CreatedAt)
			if err != nil {
				t.Fatal(err)
			}
			if phase != "reserved" {
				if err = f.app.store.SetGPUInferencePreparation(ctx, work.ID, work.LeaseToken, false); err != nil {
					t.Fatal(err)
				}
				if phase == "resources_ready" {
					if err = f.app.store.SetGPUInferencePreparation(ctx, work.ID, work.LeaseToken, true); err != nil {
						t.Fatal(err)
					}
				}
			}
			job := f.step(t)
			if (job.InferenceWorkID != "") != (phase != "preparing_resources") {
				t.Fatalf("wrong preparation recovery: %+v", job)
			}
			metadata, _ := f.app.contentCipher.EncryptBytes([]byte(`{}`))
			if _, err = inference.NewClient(f.app.store, f.app.contentCipher).SubmitAssistant(ctx, work, f.snapshot.Request.Body, time.Duration(f.snapshot.Request.Policy.TimeoutMS)*time.Millisecond, job.ID, source.RunToken, metadata); !errors.Is(err, store.ErrGPUWorkConflict) {
				t.Fatal("stale source dispatched", err)
			}
			if f.calls.Load() != 0 {
				t.Fatal("source worker called model")
			}
		})
	}
}

func TestDurableAssistantPriorityBeforeAdmission(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	var priorityID int64
	if err := f.db.QueryRow(`INSERT INTO users(username,password_hash,role,queue_priority) VALUES('priority-owner','disabled','admin',true) RETURNING id`).Scan(&priorityID); err != nil {
		t.Fatal(err)
	}
	input, err := f.app.makePromptAssistantJob(newRequestID(), priorityID, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	priority, err := f.app.store.EnqueuePromptAssistantJob(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.refreshPromptAssistantJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	work, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource)
	if err != nil || work.ID != priority.ID {
		t.Fatalf("priority source did not get GPU first: %+v %v", work, err)
	}
	ordinary, err := f.app.store.PromptAssistantJob(context.Background(), f.user.ID, f.job.ID)
	if err != nil || ordinary.State != "queued" || ordinary.InferenceWorkID != "" {
		t.Fatalf("ordinary source passed priority: %+v %v", ordinary, err)
	}
}

func TestDurableAssistantCorruptRequestFailsBeforeModel(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	if _, err := f.db.Exec(`UPDATE prompt_assistant_jobs SET request_cipher='broken' WHERE id=$1`, f.job.ID); err != nil {
		t.Fatal(err)
	}
	job := f.step(t)
	if job.State != "failed" || job.ContentEventID == 0 || f.calls.Load() != 0 {
		t.Fatalf("corrupt request was retried/dispatched: %+v", job)
	}
	if _, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("corrupt request retains GPU", err)
	}
}

func TestDurableAssistantQueueIntentAndExpiry(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	ctx := context.Background()
	if _, err := f.db.Exec(`UPDATE gpu_work SET ready_until=now()-interval '1 minute' WHERE id=$1`, f.job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE users SET queue_priority=true WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.app.store.ClaimPromptAssistantJob(ctx, "queue-refresh")
	if err != nil || len(claimed.RequestCipher) != 0 {
		t.Fatal(claimed, err)
	}
	work, err := f.app.store.GPUWork(ctx, f.job.ID)
	if err != nil || !work.Priority || !work.ReadyUntil.After(time.Now()) {
		t.Fatal("intent not refreshed", work, err)
	}
	if changed, err := f.app.store.FinishPromptAssistantJob(ctx, claimed, "completed", "invalid", []byte("result"), domain.ContentEventRecord{}, domain.PromptAssistantRunRecord{}); !errors.Is(err, store.ErrGPUWorkInput) || changed {
		t.Fatal("invalid event accepted", changed, err)
	}
	if _, err := f.db.Exec(`UPDATE prompt_assistant_jobs SET expires_at=now()-interval '1 second',metadata_cipher='broken' WHERE id=$1`, f.job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.refreshPromptAssistantJobs(ctx); err != nil {
		t.Fatal("expiry requires readable prompt", err)
	}
	if n, err := f.app.store.CleanupPromptAssistantJobs(ctx); err != nil || n != 1 {
		t.Fatal("expired source not removed", n, err)
	}
	if _, err := f.app.store.EnqueuePromptAssistantJob(ctx, f.input); !errors.Is(err, store.ErrPromptAssistantConflict) {
		t.Fatal("expired ID can dispatch again", err)
	}
	if _, err := f.app.store.GPUWork(ctx, f.job.ID); err != nil {
		t.Fatal("tombstone removed", err)
	}
}
