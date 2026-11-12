package gateway

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
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
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/miningagent"
	"ai-access-gateway/internal/modelruntime"
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/store"
)

type ollamaProtocolRuntime struct {
	generation atomic.Int64
	calls      atomic.Int64
	stop       <-chan struct{}
}

func (r *ollamaProtocolRuntime) Advance(context.Context) (domain.GPUInferenceExecutor, error) {
	generation := r.generation.Add(1)
	return domain.GPUInferenceExecutor{ID: strings.Repeat("9", 64), Generation: generation, Token: fmt.Sprintf("%064x", generation)}, nil
}
func (*ollamaProtocolRuntime) Execute(context.Context, []byte) (modelruntime.Result, error) {
	return modelruntime.Result{}, errors.New("unexpected managed call")
}
func (*ollamaProtocolRuntime) Close() error { return nil }
func (r *ollamaProtocolRuntime) ExecuteHTTP(ctx context.Context, request modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
	r.calls.Add(1)
	err := consume(200, http.Header{"Content-Type": {"application/x-ndjson"}}, strings.NewReader("{\"done\":true}\n"))
	select {
	case <-r.stop:
	case <-ctx.Done():
	}
	return modelruntime.Result{Status: 200, Dispatched: true, Stopped: true}, err
}

func TestOllamaGPUHTTPProtocolIntegration(t *testing.T) {
	testOwnedGPUHTTPProtocol(t, false)
}

func TestHarnessGPUHTTPProtocolIntegration(t *testing.T) {
	testOwnedGPUHTTPProtocol(t, true)
}

func testOwnedGPUHTTPProtocol(t *testing.T, harness bool) {
	f := gpuDispatchFixture(t)
	if _, err := f.db.Exec(`TRUNCATE gpu_inference_executors CASCADE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f.app.cfg.OllamaGPUGuardRequired = true
	f.app.cfg.OllamaGPUGuardToken = strings.Repeat("g", 64)
	f.app.cfg.MiningFencedCommands = true
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	f.app.cfg.AdminAllowedNetworks = []*net.IPNet{loopback}
	permitHandler, permitPath := f.app.handleOllamaGPUPermit, "/internal/gpu/ollama/permit"
	var miner *fencedTestMiner
	if harness {
		f.app.cfg.OllamaGPUGuardRequired = false
		f.app.cfg.Harness.Upstream, _ = url.Parse("http://harness.test")
		f.app.cfg.Harness.AdmissionKey = f.app.cfg.OllamaGPUGuardToken
		permitHandler, permitPath = f.app.handleHarnessGPUPermit, "/internal/gpu/harness/permit"
		if _, err := f.db.Exec(`UPDATE users SET role='admin' WHERE id=$1`, f.user.ID); err != nil {
			t.Fatal(err)
		}
		f.app.cfg.Harness.PauseMining, f.app.cfg.Harness.MiningUserID = true, f.user.ID
		miner = &fencedTestMiner{}
		miner.running.Store(true)
		const minerToken = "fixture-miner-token-for-harness-not-production"
		agent, err := miningagent.NewFencedServer(minerToken, miner)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(agent.Handler())
		defer server.Close()
		base, _ := url.Parse(server.URL)
		f.app.mining = mining.NewClient(base, minerToken)
		if _, err := f.app.store.CreateMiner(ctx, store.CreateMinerParams{Name: "Harness fixture", ScriptPath: `C:\Mining\test.bat`, ProcessName: "test.exe", Enabled: true, Default: true, CreatedByUserID: f.user.ID}); err != nil {
			t.Fatal(err)
		}
	}
	callback := httptest.NewServer(f.app.adminLANOnly(http.HandlerFunc(permitHandler)))
	defer callback.Close()
	stop := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(stop) })
	runtime := &ollamaProtocolRuntime{stop: stop}
	worker, err := inference.NewOwnedWorker(f.app.store, f.app.contentCipher, runtime, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	api, err := inference.NewOllamaAPI(worker, inference.OllamaAPIConfig{ClientKey: strings.Repeat("c", 64), AdmissionKey: f.app.cfg.OllamaGPUGuardToken, AdmissionURL: callback.URL + permitPath})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	server := httptest.NewServer(api)
	defer func() { once.Do(func() { close(stop) }); server.Close() }()
	apiURL, _ := url.Parse(server.URL)
	f.app.promptAssistant = promptassistant.NewClient(apiURL, "fixture")
	if harness {
		// Harness has its own protected API; the existing assistant still checks
		// the ordinary Ollama independently rather than recursively probing itself.
		ordinary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"models":[]}`) }))
		defer ordinary.Close()
		ordinaryURL, _ := url.Parse(ordinary.URL)
		f.app.promptAssistant = promptassistant.NewClient(ordinaryURL, "fixture")
	}
	health := httptest.NewServer(worker.ReadinessHandler(f.db.PingContext))
	defer health.Close()
	f.app.cfg.InferenceWorkerURL, _ = url.Parse(health.URL)
	run, stopWorker := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- worker.Run(run) }()
	defer func() {
		stopWorker()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	}()
	for {
		if err = inference.ProbeOwnedOllamaReadiness(ctx, f.app.cfg.InferenceWorkerURL); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("owned adapter never became ready", err)
		case err := <-done:
			t.Fatal("worker exited", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	request, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/chat", strings.NewReader(`{"model":"fixture","messages":[],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("c", 64))
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("admission roundtrip: status=%d body=%s", response.StatusCode, body)
	}
	if _, err = bufio.NewReader(response.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	permit, err := f.app.store.ActiveGPUExecutorPermit(ctx, domain.GPUPrimaryResource)
	if err != nil || permit.State != "running" || permit.Executor != domain.GPUExecutorOllama {
		t.Fatalf("stream has no permit: %+v %v", permit, err)
	}
	if harness {
		leases, err := f.app.store.ListQuickGenerationMiningLeases(ctx)
		if err != nil || len(leases) != 1 || leases[0].ExecutorPermitID != permit.ID || leases[0].ResumeReady || miner.running.Load() {
			t.Fatalf("Harness did not retain a stopped miner: leases=%+v err=%v", leases, err)
		}
		if _, err := f.app.refreshQuickGenerationMiningLeases(ctx); err != nil || miner.running.Load() {
			t.Fatal("miner resumed while native model active", err)
		}
		if _, err := f.db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, f.user.ID); err != nil {
			t.Fatal(err)
		}
	}
	_, err = f.app.gpuCoordinator.admit(ctx, domain.GPUWorkTraining, "after-ollama", &f.user, "training", time.Now())
	var wait *gpuAdmissionWait
	if !errors.As(err, &wait) {
		t.Fatalf("training overlapped external stream: %v", err)
	}
	once.Do(func() { close(stop) })
	if _, err = io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.ActiveGPUExecutorPermit(ctx, domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("finished external stream retained permit", err)
	}
	if harness {
		if _, err := f.app.refreshQuickGenerationMiningLeases(ctx); err != nil || !miner.running.Load() || miner.starts.Load() != 1 {
			t.Fatal("Harness failed to resume miner", err)
		}
		leases, err := f.app.store.ListQuickGenerationMiningLeases(ctx)
		if err != nil || len(leases) != 0 {
			t.Fatal("completed Harness reservation retained", err)
		}
		miner.running.Store(false)
		if _, err := f.db.Exec(`UPDATE users SET disabled=false WHERE id=$1`, f.user.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.app.gpuCoordinator.admit(ctx, domain.GPUWorkTraining, "after-ollama", &f.user, "training", time.Now()); err != nil {
		t.Fatal("training did not resume after external stop", err)
	}
	if runtime.calls.Load() != 1 {
		t.Fatal("protocol replayed model request")
	}
}

func TestOllamaGPUCoordinatorUsesOwnedReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		allowed    bool
	}{
		{"owned adapter ready", `{"service":"inference-worker","protocol":2,"ready":true,"owned":true,"ollama_admission":true}`, 200, true},
		{"direct worker", `{"service":"inference-worker","protocol":2,"ready":true,"owned":false,"ollama_admission":true}`, 200, false},
		{"adapter absent", `{"service":"inference-worker","protocol":2,"ready":true,"owned":true}`, 200, false},
		{"owner recovery", `{"service":"inference-worker","protocol":2,"ready":false,"owned":true,"ollama_admission":true}`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := gpuDispatchFixture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" || r.Method != "GET" {
					t.Errorf("coordinator called guarded data API: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			f.app.cfg.OllamaGPUGuardRequired = true
			f.app.cfg.InferenceWorkerURL = endpoint
			f.app.promptAssistant = promptassistant.NewClient(endpoint, "fixture")
			f.enqueue(t)
			if _, err := f.app.dispatchGenerationJobs(context.Background()); err != nil {
				t.Fatal(err)
			}
			if (f.posts.Load() == 1) != tc.allowed {
				t.Fatalf("allowed=%v posts=%d", tc.allowed, f.posts.Load())
			}
		})
	}
}
