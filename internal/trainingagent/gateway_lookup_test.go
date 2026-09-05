package trainingagent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-access-gateway/internal/loratraining"
)

func TestGatewayJobLookupRecoversAcceptedJobWithoutSubmitting(t *testing.T) {
	id := "gateway-job-012345"
	controller := &Controller{
		jobs: map[string]*jobRecord{"agent-id": {
			Spec:   loratraining.JobSpec{GatewayJobID: id},
			Status: loratraining.JobStatus{ID: "agent-id", GatewayJobID: id, State: "queued", LogTail: []string{"accepted"}},
		}},
		byGateway: map[string]string{id: "agent-id"}, queue: make(chan string, 1),
	}
	token := strings.Repeat("a", 32)
	server, err := NewServer(token, controller)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	baseURL, _ := url.Parse(httpServer.URL)
	client := loratraining.NewClient(baseURL, token)
	for _, state := range []string{"queued", "preparing", "caching", "running", "installing", "completed", "failed", "cancelled"} {
		controller.mu.Lock()
		controller.jobs["agent-id"].Status.State = state
		controller.mu.Unlock()
		status, err := client.StatusByGatewayID(context.Background(), id)
		if err != nil || status.ID != "agent-id" || status.State != state {
			t.Fatalf("lookup %s: %+v, %v", state, status, err)
		}
	}
	if len(controller.queue) != 0 || len(controller.jobs) != 1 {
		t.Fatal("lookup dispatched or created a job")
	}
	status, err := controller.StatusByGatewayID(id)
	if err != nil {
		t.Fatal(err)
	}
	status.LogTail[0] = "caller edit"
	if controller.jobs["agent-id"].Status.LogTail[0] != "accepted" {
		t.Fatal("lookup leaked a mutable log slice")
	}
	_, err = client.StatusByGatewayID(context.Background(), "missing")
	var httpErr *loratraining.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound || httpErr.Code != "job_not_found" {
		t.Fatalf("missing lookup must return coded not found, got %v", err)
	}
}

func TestGatewayJobLookupEndpointContract(t *testing.T) {
	controller := &Controller{jobs: make(map[string]*jobRecord), byGateway: make(map[string]string)}
	token := strings.Repeat("a", 32)
	server, err := NewServer(token, controller)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, query, token string
		want                       int
	}{
		{"no auth", "GET", "?gateway_job_id=some-job", "", 401},
		{"bad auth", "GET", "?gateway_job_id=some-job", strings.Repeat("b", 32), 401},
		{"read only", "POST", "?gateway_job_id=some-job", token, 405},
		{"no ID", "GET", "", token, 400},
		{"blank ID", "GET", "?gateway_job_id=%20", token, 400},
		{"long ID", "GET", "?gateway_job_id=" + strings.Repeat("x", 97), token, 400},
		{"duplicate ID", "GET", "?gateway_job_id=one&gateway_job_id=two", token, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "/v1/gateway-jobs"+tc.query, nil)
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}
	id := "opaque +/#?% ID"
	controller.jobs["agent"] = &jobRecord{Status: loratraining.JobStatus{ID: "agent", GatewayJobID: id, State: "running"}}
	controller.byGateway[id] = "agent"
	request := httptest.NewRequest("GET", "/v1/gateway-jobs?"+url.Values{"gateway_job_id": {id}}.Encode(), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("opaque lookup: code=%d headers=%v", recorder.Code, recorder.Header())
	}
}

func TestGatewayJobLookupSurvivesAgentRecordReload(t *testing.T) {
	root := t.TempDir()
	controller := &Controller{config: Config{RootDir: root}, jobs: make(map[string]*jobRecord), byGateway: make(map[string]string)}
	record := &jobRecord{
		Spec:   loratraining.JobSpec{GatewayJobID: "gateway-job"},
		Status: loratraining.JobStatus{ID: "agent-job", GatewayJobID: "gateway-job", State: "completed"},
	}
	if err := os.MkdirAll(filepath.Join(root, "jobs", record.Status.ID), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := controller.persist(record); err != nil {
		t.Fatal(err)
	}
	if err := controller.loadJobs(); err != nil {
		t.Fatal(err)
	}
	status, err := controller.StatusByGatewayID("gateway-job")
	if err != nil || status.ID != "agent-job" || status.State != "completed" {
		t.Fatalf("reloaded lookup: %+v %v", status, err)
	}
	delete(controller.jobs, "agent-job")
	if _, err := controller.StatusByGatewayID("gateway-job"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale index: %v", err)
	}
}
