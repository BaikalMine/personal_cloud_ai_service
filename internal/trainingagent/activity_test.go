package trainingagent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"ai-access-gateway/internal/loratraining"
)

func TestControllerActivity(t *testing.T) {
	c := &Controller{jobs: map[string]*jobRecord{}}
	if got := c.Activity(); got.State != "idle" {
		t.Fatal(got)
	}
	c.pendingSubmissions = 1
	if got := c.Activity(); got.State != "busy" || got.PendingSubmissions != 1 {
		t.Fatal(got)
	}
	c.pendingSubmissions = 0
	c.jobs["training"] = &jobRecord{Status: loratraining.JobStatus{State: "running"}}
	if got := c.Activity(); got.State != "busy" || got.ActiveJobs != 1 {
		t.Fatal(got)
	}
	c.jobs["training"].Status.State = "failed"
	c.jobs["training"].Status.ExecutionUnconfirmed = true
	if got := c.Activity(); got.State != "uncertain" || got.ActiveJobs != 0 || got.UnconfirmedJobs != 1 {
		t.Fatal(got)
	}
	c.jobs["training"].Status.ExecutionUnconfirmed = false
	if got := c.Activity(); got.State != "idle" {
		t.Fatal(got)
	}
}

func TestActivityHTTP(t *testing.T) {
	const token = "activity-test-token-at-least-32-characters"
	server, err := NewServer(token, &Controller{jobs: map[string]*jobRecord{}})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	endpoint, _ := url.Parse(host.URL)
	result, err := loratraining.NewClient(endpoint, token).Activity(context.Background())
	if err != nil || result.State != "idle" {
		t.Fatalf("%+v %v", result, err)
	}
	for _, tc := range []struct {
		method, auth string
		want         int
	}{
		{"GET", "", 401}, {"POST", token, 405}, {"GET", token, 200},
	} {
		r := httptest.NewRequest(tc.method, "/v1/activity", nil)
		r.Header.Set("Authorization", "Bearer "+tc.auth)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s status=%d", tc.method, w.Code)
		}
		if tc.want == http.StatusOK && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("activity may be cached")
		}
	}
}
