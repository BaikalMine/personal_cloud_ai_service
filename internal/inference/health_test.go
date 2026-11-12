package inference

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadinessBoundsUnresponsiveStorageAndSharesProbe(t *testing.T) {
	worker := &Worker{journal: testJournal(t)}
	worker.running.Store(true)
	release, exited := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	handler := worker.ReadinessHandler(func(context.Context) error {
		calls.Add(1)
		<-release
		close(exited)
		return nil
	})
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(ctx))
			if response.Code != 503 {
				t.Errorf("unresponsive probe returned %d", response.Code)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wait.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("health handlers ignored their deadlines")
	}
	close(release)
	<-exited
	if calls.Load() != 1 {
		t.Fatal("unbounded storage probes", calls.Load())
	}
}

func TestReadinessRequiresLoopAndStorage(t *testing.T) {
	worker := &Worker{journal: testJournal(t)}
	var storageErr error
	probes := 0
	handler := worker.ReadinessHandler(func(ctx context.Context) error {
		probes++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("unbounded storage probe")
		}
		return storageErr
	})
	check := func(code int) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if response.Code != code || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "secret") {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	check(503)
	if probes != 0 {
		t.Fatal("stopped worker queried storage")
	}
	worker.running.Store(true)
	check(200)
	worker.journalBlocked.Store(true)
	check(503)
	worker.journalBlocked.Store(false)
	check(200)
	journal := worker.journal
	worker.journal = nil
	check(503)
	worker.journal = journal
	storageErr = errors.New("database secret must not leak")
	check(503)
	storageErr = nil
	worker.running.Store(false)
	check(503)
	for _, item := range []struct {
		method, path string
		code         int
	}{{"POST", "/healthz", 405}, {"GET", "/jobs", 404}, {"HEAD", "/healthz", 503}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(item.method, item.path, nil))
		if response.Code != item.code {
			t.Fatal(item, response.Code)
		}
		if item.method == "HEAD" && response.Body.Len() != 0 {
			t.Fatal("HEAD has a body")
		}
	}
}

func TestReadinessProbeValidatesExecutorProtocol(t *testing.T) {
	for _, item := range []struct {
		name, body string
		code       int
		valid      bool
	}{
		{"ready", `{"service":"inference-worker","protocol":2,"ready":true}`, 200, true},
		{"offline", `{"service":"inference-worker","protocol":2,"ready":true}`, 503, false},
		{"wrong-service", `{"service":"gateway","protocol":2,"ready":true}`, 200, false},
		{"legacy-without-journal", `{"service":"inference-worker","protocol":1,"ready":true}`, 200, false},
		{"wrong-version", `{"service":"inference-worker","protocol":3,"ready":true}`, 200, false},
		{"starting", `{"service":"inference-worker","protocol":2,"ready":false}`, 200, false},
		{"html", `<html>ready</html>`, 200, false},
		{"trailing", `{"service":"inference-worker","protocol":2,"ready":true} {}`, 200, false},
		{"redirect", "", 302, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/worker/healthz" || r.URL.RawQuery != "" {
					t.Error(r.URL)
				}
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(item.code)
				_, _ = w.Write([]byte(item.body))
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL + "/worker?discard=true")
			if err := ProbeReadiness(context.Background(), endpoint); (err == nil) != item.valid {
				t.Fatal(err)
			}
		})
	}
	if ProbeReadiness(context.Background(), nil) == nil {
		t.Fatal("nil endpoint accepted")
	}
}

func TestAssistantModelReadinessRequiresAccessCapability(t *testing.T) {
	for _, capability := range []string{"", `,"assistant_model_access":0`, `,"assistant_model_access":1`, `,"assistant_model_access":2`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"service":"inference-worker","protocol":2,"ready":true` + capability + `}`))
		}))
		endpoint, _ := url.Parse(server.URL)
		err := ProbeAssistantModelReadiness(context.Background(), endpoint)
		server.Close()
		if (err == nil) != (capability == `,"assistant_model_access":1`) {
			t.Fatal("incorrect model access readiness", capability, err)
		}
	}
	worker := &Worker{journal: testJournal(t)}
	worker.running.Store(true)
	server := httptest.NewServer(worker.ReadinessHandler(func(context.Context) error { return nil }))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	if err := ProbeAssistantModelReadiness(context.Background(), endpoint); err != nil {
		t.Fatal("current worker did not advertise its capability", err)
	}
}

func TestReadinessStaysOnlineDuringModelCallAndStopsWithWorker(t *testing.T) {
	f := newFixture(t)
	f.submit(t)
	arrived, release := make(chan struct{}), make(chan struct{})
	worker := f.worker(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		_, _ = w.Write(testResponse)
	})
	worker.journal = testJournal(t)
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	server := httptest.NewServer(worker.ReadinessHandler(f.db.PingContext))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	if err := ProbeReadiness(context.Background(), endpoint); err != nil {
		t.Fatal("busy worker not ready", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("worker did not stop")
	}
	if err := ProbeReadiness(context.Background(), endpoint); err == nil {
		t.Fatal("stopped loop appears ready")
	}
	work, err := f.store.GPUWork(context.Background(), f.work.ID)
	if err != nil || work.State == "completed" || work.State == "cancelled" {
		t.Fatal("health/shutdown released unconfirmed model work", work, err)
	}
}
