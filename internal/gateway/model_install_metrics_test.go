package gateway

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/modelmanager"
	"ai-access-gateway/internal/store"
)

func TestModelInstallViewsReadRealTelemetryAndKeepDurableJobs(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/api/lm/download-progress/") {
			t.Error("unexpected command")
		}
		if strings.HasSuffix(r.URL.Path, "lost") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"progress":62,"bytes_downloaded":6200,"total_bytes":10000,"bytes_per_second":240}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	jobs := []store.ModelInstall{{ID: "live", State: "running", Progress: 50}, {ID: "lost", State: "running", Progress: 25}, {ID: "queued", State: "queued"}, {ID: "done", State: "completed", Progress: 100}}
	views := modelInstallViews(context.Background(), &modelmanager.Client{Base: base}, jobs)
	if len(views) != 4 || calls != 2 || views[0].Progress != 62 || views[0].BytesPerSecond == nil || *views[0].BytesPerSecond != 240 || *views[0].BytesDownloaded != 6200 || *views[0].TotalBytes != 10000 {
		t.Fatal(views, calls)
	}
	if views[1].MetricsAvailable || views[1].Progress != 25 || views[1].State != "running" || jobs[0].Progress != 50 {
		t.Fatal("durable state changed or lost")
	}
}

func TestModelInstallMetricsRejectInvalidValuesAndVerificationSpeed(t *testing.T) {
	negative := int64(-1)
	invalid := math.Inf(1)
	view := modelInstallView{ModelInstall: store.ModelInstall{State: "running", Progress: 20}}
	applyModelInstallMetrics(&view, modelmanager.Result{Progress: math.NaN(), TotalBytes: &negative, BytesDownloaded: &negative, BytesPerSecond: &invalid})
	if view.MetricsAvailable || view.BytesDownloaded != nil || view.TotalBytes != nil || view.BytesPerSecond != nil || view.Progress != 20 {
		t.Fatal(view)
	}
	total, downloaded, speed := int64(100), int64(101), float64(50)
	applyModelInstallMetrics(&view, modelmanager.Result{Progress: 10, TotalBytes: &total, BytesDownloaded: &downloaded, BytesPerSecond: &speed})
	if view.Progress != 20 || view.BytesDownloaded != nil {
		t.Fatal(view)
	}
	view = modelInstallView{ModelInstall: store.ModelInstall{State: "reconciling", Progress: 100}}
	applyModelInstallMetrics(&view, modelmanager.Result{Progress: 100, BytesPerSecond: &speed})
	if view.BytesPerSecond != nil {
		t.Fatal("old transfer speed shown during verification")
	}
	view = modelInstallView{ModelInstall: store.ModelInstall{State: "running", Progress: 20}}
	applyModelInstallMetrics(&view, modelmanager.Result{Progress: 20, Status: "cancelled", BytesPerSecond: &speed})
	if view.BytesPerSecond != nil {
		t.Fatal("old transfer speed shown after upstream cancellation")
	}
}

func TestModelInstallViewKeepsPrivateFieldsOutOfJSON(t *testing.T) {
	view := modelInstallView{ModelInstall: store.ModelInstall{ID: "fixture", OwnerID: 999, Request: json.RawMessage(`{"quote":"private-install-request"}`), State: "queued"}}
	data, err := json.Marshal(view)
	if err != nil || strings.Contains(string(data), "private-install-request") || strings.Contains(string(data), "owner") {
		t.Fatal("private receipt fields leaked")
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil || decoded["id"] != "fixture" || decoded["metrics_available"] != false {
		t.Fatal("durable receipt contract changed", err)
	}
}
