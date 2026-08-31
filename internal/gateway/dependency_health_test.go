package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/mining"
)

func TestDependencyFreshnessBoundariesUseControlledTime(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	monitor := newDependencyMonitor(10*time.Second, 30*time.Second, 90*time.Second, func() time.Time { return now })
	monitor.success("service", "ok", &now, 25*time.Millisecond)
	spec := dependencySpec{Key: "service", Name: "Service", Configured: true}

	if status := monitor.status(spec); status.State != DependencyOnline || status.LatencyMillis != 25 {
		t.Fatalf("fresh status = %+v", status)
	}
	now = now.Add(30 * time.Second)
	if status := monitor.status(spec); status.State != DependencyOnline {
		t.Fatalf("status at stale boundary = %+v", status)
	}
	now = now.Add(time.Nanosecond)
	if status := monitor.status(spec); status.State != DependencyStale {
		t.Fatalf("status after stale boundary = %+v", status)
	}
	now = now.Add(60 * time.Second)
	if status := monitor.status(spec); status.State != DependencyOffline {
		t.Fatalf("status after offline boundary = %+v", status)
	}
}

func TestMiningPriorityDegradationWarningExplainsRetry(t *testing.T) {
	warning := miningPriorityDegradationWarning(MiningOverview{Agent: DependencyStatus{
		Detail: "Windows-agent не отвечает.", RetryInSeconds: 8,
	}})
	if !strings.Contains(warning, "продолжена в обычном режиме") || !strings.Contains(warning, "через 8 сек") || !strings.Contains(warning, "Windows-agent не отвечает") {
		t.Fatalf("warning = %q", warning)
	}
}

func TestDependencyRequiresFreshDataSeparatelyFromHeartbeat(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	monitor := newDependencyMonitor(10*time.Second, 30*time.Second, 90*time.Second, func() time.Time { return now })
	spec := dependencySpec{Key: "monitor", Name: "Monitor", Configured: true, RequiresData: true}

	monitor.success("monitor", "heartbeat", nil, time.Millisecond)
	if status := monitor.status(spec); status.State != DependencyStale || status.LastSuccessAt == nil || status.LastDataAt != nil {
		t.Fatalf("heartbeat without data = %+v", status)
	}
	monitor.observeData("monitor", now)
	if status := monitor.status(spec); status.State != DependencyOnline || status.LastDataAt == nil {
		t.Fatalf("fresh data status = %+v", status)
	}
	now = now.Add(31 * time.Second)
	monitor.success("monitor", "heartbeat", nil, time.Millisecond)
	if status := monitor.status(spec); status.State != DependencyStale {
		t.Fatalf("fresh heartbeat with stale data = %+v", status)
	}
}

func TestAgentHeartbeatDoesNotInventCollectedData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("unexpected health path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	_, dataAt, misconfigured, err := app.agentDependencyProbe(mining.NewClient(baseURL, "test-token"))(context.Background())
	if err != nil || misconfigured || dataAt != nil {
		t.Fatalf("heartbeat result: data=%v misconfigured=%v err=%v", dataAt, misconfigured, err)
	}
}

func TestDependencyReportsMisconfigurationAndLastError(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	monitor := newDependencyMonitor(10*time.Second, 30*time.Second, 90*time.Second, func() time.Time { return now })

	status := monitor.status(dependencySpec{Key: "missing", Name: "Missing", Configured: false})
	if status.State != DependencyMisconfigured {
		t.Fatalf("unconfigured status = %+v", status)
	}
	monitor.failure("service", "неверный токен", true, 5*time.Millisecond)
	status = monitor.status(dependencySpec{Key: "service", Name: "Service", Configured: true})
	if status.State != DependencyMisconfigured || status.LastError != "неверный токен" || status.LastErrorAt == nil || status.RetryInSeconds != 10 {
		t.Fatalf("misconfigured probe status = %+v", status)
	}
}
