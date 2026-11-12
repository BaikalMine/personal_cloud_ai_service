package inference

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
	"ai-access-gateway/internal/store"
)

type ollamaHTTPFixture struct {
	*fixture
	runtime   *ollamaRuntimeFixture
	worker    *Worker
	api       *OllamaAPI
	server    *httptest.Server
	completed chan struct{}
	admission func(http.ResponseWriter, *http.Request, domain.GPUExecutorPermitRequest)
}

func newOllamaHTTPFixture(t *testing.T) *ollamaHTTPFixture {
	runtime := &ollamaRuntimeFixture{}
	x := newOllamaHTTPFixtureWithRuntime(t, runtime)
	x.runtime = runtime
	return x
}

func newOllamaHTTPFixtureWithRuntime(t *testing.T, runtime OwnedModelRuntime) *ollamaHTTPFixture {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	resource, err := f.store.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReleaseGPUWork(ctx, f.work.ID, f.work.LeaseToken, domain.GPUReleaseEvidence{Code: "not_dispatched", ResourceRevision: resource.Revision}); err != nil {
		t.Fatal(err)
	}
	x := &ollamaHTTPFixture{fixture: f, completed: make(chan struct{}, 16)}
	x.worker, err = NewOwnedWorker(f.store, f.cipher, runtime, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.worker.Close)
	x.worker.running.Store(true)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path != "/internal/gpu/ollama/permit" && r.URL.Path != "/internal/gpu/harness/permit") || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("g", 64) {
			t.Error("wrong internal callback")
		}
		var input struct {
			Action string `json:"action"`
			domain.GPUExecutorPermitRequest
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Action != "reserve" {
			t.Error("invalid callback body")
			w.WriteHeader(400)
			return
		}
		if x.admission != nil {
			x.admission(w, r, input.GPUExecutorPermitRequest)
			return
		}
		permit, err := x.reserve(r.Context(), input.GPUExecutorPermitRequest)
		if errors.Is(err, store.ErrGPUExecutorBusy) {
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"protocol":1,"error":"executor_permit_unavailable"}`)
			return
		}
		if err != nil {
			t.Error(err)
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"protocol": 1, "id": permit.ID, "state": permit.State, "started": false})
	}))
	t.Cleanup(callback.Close)
	x.api, err = NewOllamaAPI(x.worker, ollamaTestConfig(callback.URL))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.api.Close)
	x.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { x.completed <- struct{}{} }()
		x.api.ServeHTTP(w, r)
	}))
	t.Cleanup(x.server.Close)
	return x
}

func (x *ollamaHTTPFixture) reserve(ctx context.Context, input domain.GPUExecutorPermitRequest) (domain.GPUExecutorPermit, error) {
	var permit domain.GPUExecutorPermit
	err := x.store.WithGPUAdmissionGuard(ctx, func() error {
		resource, err := x.store.GPUResource(ctx, domain.GPUPrimaryResource)
		if err != nil {
			return err
		}
		if _, err = x.store.ObserveGPUResource(ctx, resource.ID, resource.Revision, "idle", "synthetic executors", 10*time.Second); err != nil {
			return err
		}
		permit, err = x.store.ReserveGPUExecutorPermit(ctx, input)
		return err
	})
	return permit, err
}

func (x *ollamaHTTPFixture) request(t *testing.T, ctx context.Context, route string) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, "POST", x.server.URL+route, strings.NewReader(`{"model":"fixture","stream":true,"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+x.api.config.ClientKey)
	r.Header.Set("X-AI-Gateway-GPU-Key", "client-forgery")
	response, err := x.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func (x *ollamaHTTPFixture) assertRunning(t *testing.T) domain.GPUExecutorPermit {
	t.Helper()
	permit, err := x.store.ActiveGPUExecutorPermit(context.Background(), domain.GPUPrimaryResource)
	if err != nil || permit.Executor != domain.GPUExecutorOllama || permit.State != "running" || permit.OwnerGeneration != 1 {
		t.Fatalf("runtime lacked owned running permit: %+v %v", permit, err)
	}
	return permit
}

func TestOllamaHTTPStreamingPermitLifetime(t *testing.T) {
	x := newOllamaHTTPFixture(t)
	stop := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(stop) })
	frame := "{\"done\":true,\"response\":\"fixture\"}\n"
	x.runtime.handle = func(ctx context.Context, request modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
		x.assertRunning(t)
		if request.Path != "/api/generate" || request.Method != "POST" || request.ResponseLimit != modelruntime.MaxHTTPResponseBytes || !strings.Contains(string(request.Body), `"model":"fixture"`) {
			t.Error("native request changed")
		}
		err := consume(200, http.Header{"Content-Type": {"application/x-ndjson"}, "Set-Cookie": {"private=secret"}, "Authorization": {"private"}, "Location": {"http://forbidden.test"}}, strings.NewReader(frame))
		select {
		case <-stop:
		case <-ctx.Done():
		}
		return modelruntime.Result{Status: 200, Dispatched: true, Stopped: true}, err
	}
	response := x.request(t, context.Background(), "/api/generate")
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != frame || response.StatusCode != 200 {
		t.Fatalf("first frame: %q %v status=%d", line, err, response.StatusCode)
	}
	if response.Header.Get("Set-Cookie") != "" || response.Header.Get("Authorization") != "" || response.Header.Get("Location") != "" || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("response headers crossed boundary")
	}
	x.assertRunning(t)
	if worked, err := x.worker.RunOne(context.Background()); err != nil || worked {
		t.Fatalf("managed overlap: %v %v", worked, err)
	}
	second := x.request(t, context.Background(), "/api/chat")
	_, _ = io.Copy(io.Discard, second.Body)
	if second.StatusCode != 409 {
		t.Fatalf("external overlap: %d", second.StatusCode)
	}
	await(t, x.completed)
	work, err := x.store.EnqueueGPUWork(context.Background(), domain.GPUWorkRequest{ID: "waiting-training", Kind: domain.GPUWorkTraining, JobKey: "waiting-training", UserID: *x.work.UserID, ResourceID: domain.GPUPrimaryResource})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := x.store.AcquireGPUWork(context.Background(), work.ID, "managed-training", time.Minute)
	if err != nil || admission.Granted || admission.WaitCode != "external_work" {
		t.Fatalf("managed admission during postprocessing: %+v %v", admission, err)
	}
	once.Do(func() { close(stop) })
	if _, err = io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	await(t, x.completed)
	if _, err = x.store.ActiveGPUExecutorPermit(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stopped permit retained: %v", err)
	}
	third := x.request(t, context.Background(), "/api/chat")
	_, _ = io.Copy(io.Discard, third.Body)
	if third.StatusCode != 409 {
		t.Fatalf("external overtook waiting managed work: %d", third.StatusCode)
	}
	if x.runtime.httpCalls.Load() != 1 || x.runtime.managedCalls.Load() != 0 {
		t.Fatal("duplicate native execution")
	}
}

func TestOllamaHTTPCompletionWaitsForPermitClosure(t *testing.T) {
	x := newOllamaHTTPFixture(t)
	stop := make(chan struct{})
	consumed := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(stop) })
	text := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"
	end := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	x.runtime.handle = func(ctx context.Context, request modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
		err := consume(200, http.Header{"Content-Type": {"text/event-stream"}}, strings.NewReader(text+end))
		close(consumed)
		select {
		case <-stop:
		case <-ctx.Done():
		}
		return modelruntime.Result{Dispatched: true, Stopped: true}, err
	}
	response := x.request(t, context.Background(), "/v1/chat/completions")
	reader := bufio.NewReader(response.Body)
	if line, err := reader.ReadString('\n'); err != nil || line+"\n" != text {
		t.Fatal("text streaming was delayed", line, err)
	}
	await(t, consumed)
	x.assertRunning(t)
	finished := make(chan string, 1)
	go func() { rest, _ := io.ReadAll(reader); finished <- string(rest) }()
	select {
	case <-finished:
		t.Fatal("terminal events escaped before physical stop")
	case <-time.After(30 * time.Millisecond):
	}
	once.Do(func() { close(stop) })
	select {
	case rest := <-finished:
		if rest != "\n"+end {
			t.Fatal("completion changed", rest)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion never released")
	}
	if _, err := x.store.ActiveGPUExecutorPermit(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("terminal event arrived before permit closure", err)
	}
}

func TestOllamaHTTPLostAdmissionRecovery(t *testing.T) {
	for _, kind := range []string{"lost reserve response", "consumed start"} {
		t.Run(kind, func(t *testing.T) {
			x := newOllamaHTTPFixture(t)
			x.admission = func(w http.ResponseWriter, r *http.Request, input domain.GPUExecutorPermitRequest) {
				p, err := x.reserve(r.Context(), input)
				if err != nil {
					t.Error(err)
					w.WriteHeader(503)
					return
				}
				if kind == "consumed start" {
					if started, err := x.store.StartGPUExecutorPermit(r.Context(), input); err != nil || !started {
						t.Error("fixture start", err)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"protocol": 1, "id": p.ID, "state": "reserved", "started": false})
				} else {
					w.WriteHeader(503)
				}
			}
			response := x.request(t, context.Background(), "/api/chat")
			_, _ = io.Copy(io.Discard, response.Body)
			await(t, x.completed)
			if response.StatusCode != 503 || x.worker.ownedReady() || x.runtime.httpCalls.Load() != 0 {
				t.Fatal("ambiguous admission dispatched")
			}
			old, err := x.store.ActiveGPUExecutorPermit(context.Background(), domain.GPUPrimaryResource)
			if err != nil || old.OwnerGeneration != 1 {
				t.Fatalf("lost durable reservation: %+v %v", old, err)
			}
			if worked, err := x.worker.RunOne(context.Background()); err != nil || worked {
				t.Fatalf("recovery: %v %v", worked, err)
			}
			if _, err = x.store.ActiveGPUExecutorPermit(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("reservation not recovered", err)
			}
			if x.runtime.advances.Load() != 2 || !x.worker.ownedReady() || x.runtime.httpCalls.Load() != 0 {
				t.Fatal("recovery replayed HTTP")
			}
		})
	}
}

func TestOllamaHTTPLateAdmissionCannotOutliveGeneration(t *testing.T) {
	x := newOllamaHTTPFixture(t)
	allowCommit, callbackDone := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(allowCommit) })
	x.api.client.Timeout = 100 * time.Millisecond
	x.admission = func(w http.ResponseWriter, r *http.Request, input domain.GPUExecutorPermitRequest) {
		<-allowCommit
		_, err := x.reserve(context.Background(), input)
		if !errors.Is(err, store.ErrGPUWorkConflict) {
			t.Errorf("late reservation escaped generation fence: %v", err)
		}
		close(callbackDone)
		w.WriteHeader(503)
	}
	response := x.request(t, context.Background(), "/api/chat")
	_, _ = io.Copy(io.Discard, response.Body)
	await(t, x.completed)
	if response.StatusCode != 503 || x.worker.ownedReady() {
		t.Fatal("lost request did not invalidate owner")
	}
	if worked, err := x.worker.RunOne(context.Background()); err != nil || worked {
		t.Fatalf("advance before late callback: %v %v", worked, err)
	}
	once.Do(func() { close(allowCommit) })
	await(t, callbackDone)
	if _, err := x.store.ActiveGPUExecutorPermit(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("late permit exists", err)
	}
	if x.runtime.httpCalls.Load() != 0 || x.runtime.advances.Load() != 2 {
		t.Fatal("late request executed")
	}
}

func TestOllamaHTTPCancellationWaitsForStop(t *testing.T) {
	x := newOllamaHTTPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopping, stopped := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(stopped) })
	x.runtime.handle = func(ctx context.Context, request modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
		x.assertRunning(t)
		reader, writer := io.Pipe()
		defer reader.Close()
		go func() {
			_, _ = io.WriteString(writer, "data: fixture\n\n")
			<-ctx.Done()
			_ = writer.CloseWithError(ctx.Err())
		}()
		err := consume(200, http.Header{"Content-Type": {"text/event-stream"}}, reader)
		close(stopping)
		<-stopped
		return modelruntime.Result{Dispatched: true, Stopped: true}, err
	}
	response := x.request(t, ctx, "/v1/chat/completions")
	if line, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil || line != "data: fixture\n" {
		t.Fatal("missing SSE frame", err)
	}
	cancel()
	await(t, stopping)
	x.assertRunning(t)
	once.Do(func() { close(stopped) })
	await(t, x.completed)
	if _, err := x.store.ActiveGPUExecutorPermit(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("client cancellation canceled stop receipt", err)
	}
	if !x.worker.ownedReady() || x.runtime.httpCalls.Load() != 1 {
		t.Fatal("confirmed cancellation invalidated owner or replayed")
	}
}

func TestOllamaHTTPUnconfirmedStopAndPersistence(t *testing.T) {
	for _, kind := range []string{"stop unconfirmed", "receipt rejected"} {
		t.Run(kind, func(t *testing.T) {
			x := newOllamaHTTPFixture(t)
			if kind == "receipt rejected" {
				_, err := x.db.Exec(`CREATE FUNCTION reject_ollama_stop_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='closed' THEN RAISE EXCEPTION 'synthetic receipt failure'; END IF; RETURN NEW; END $$;
				 CREATE TRIGGER reject_ollama_stop_fixture BEFORE UPDATE ON gpu_executor_permits FOR EACH ROW EXECUTE FUNCTION reject_ollama_stop_fixture()`)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = x.db.Exec(`DROP FUNCTION IF EXISTS reject_ollama_stop_fixture() CASCADE`) })
			}
			x.runtime.handle = func(ctx context.Context, request modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
				x.assertRunning(t)
				err := consume(200, http.Header{"Content-Type": {"application/x-ndjson"}}, strings.NewReader("{\"done\":true}\n"))
				return modelruntime.Result{Dispatched: true, Stopped: kind == "receipt rejected"}, err
			}
			response := x.request(t, context.Background(), "/api/chat")
			if _, err := io.ReadAll(response.Body); err == nil {
				t.Fatal("uncertain execution returned a clean HTTP end")
			}
			await(t, x.completed)
			x.assertRunning(t)
			if x.worker.ownedReady() {
				t.Fatal("uncertain owner stayed ready")
			}
			if kind == "stop unconfirmed" {
				x.runtime.advanceErr = errors.New("drain failed")
			}
			if worked, err := x.worker.RunOne(context.Background()); err == nil || worked {
				t.Fatal("unconfirmed recovery advanced")
			}
			x.assertRunning(t)
			x.runtime.advanceErr = nil
			if kind == "receipt rejected" {
				if _, err := x.db.Exec(`DROP FUNCTION reject_ollama_stop_fixture() CASCADE`); err != nil {
					t.Fatal(err)
				}
			}
			if worked, err := x.worker.RunOne(context.Background()); err != nil || worked {
				t.Fatalf("confirmed recovery: %v %v", worked, err)
			}
			if _, err := x.store.ActiveGPUExecutorPermit(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("recovery retained old permit", err)
			}
			if x.runtime.httpCalls.Load() != 1 {
				t.Fatal("recovery replayed external request")
			}
		})
	}
}

func TestOllamaHTTPPanicInvalidatesOwner(t *testing.T) {
	x := newOllamaHTTPFixture(t)
	x.runtime.handle = func(context.Context, modelruntime.HTTPRequest, modelruntime.HTTPConsumer) (modelruntime.Result, error) {
		panic("synthetic consumer panic")
	}
	r := httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"model":"fixture"}`))
	r.Header.Set("Authorization", "Bearer "+x.api.config.ClientKey)
	func() {
		defer func() {
			if recovered := recover(); recovered != "synthetic consumer panic" {
				t.Fatalf("panic was changed: %v", recovered)
			}
		}()
		x.api.ServeHTTP(httptest.NewRecorder(), r)
	}()
	x.assertRunning(t)
	if x.worker.ownedReady() {
		t.Fatal("panic left owner ready")
	}
	if worked, err := x.worker.RunOne(context.Background()); err != nil || worked {
		t.Fatalf("panic recovery: %v %v", worked, err)
	}
	if _, err := x.store.ActiveGPUExecutorPermit(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("panic recovery retained permit", err)
	}
	if x.runtime.httpCalls.Load() != 1 {
		t.Fatal("panic request replayed")
	}
}

func TestOllamaHTTPModelPathStaysPathData(t *testing.T) {
	x := newOllamaHTTPFixture(t)
	const route = "/v1/models/model%3Fquery%23fragment"
	x.runtime.handle = func(_ context.Context, input modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
		if input.Path != route || input.Method != "GET" || len(input.Body) != 0 {
			t.Errorf("model ID changed URL structure: %+v", input)
		}
		err := consume(200, http.Header{"Content-Type": {"application/json"}}, strings.NewReader(`{"id":"fixture"}`))
		return modelruntime.Result{Status: 200, Dispatched: true, Stopped: true}, err
	}
	r, err := http.NewRequest("GET", x.server.URL+route, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+x.api.config.ClientKey)
	response, err := x.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil || response.StatusCode != 200 {
		t.Fatalf("model lookup: %d %v", response.StatusCode, err)
	}
	await(t, x.completed)
	if x.runtime.httpCalls.Load() != 1 {
		t.Fatal("model lookup not executed exactly once")
	}
}
