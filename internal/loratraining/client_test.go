package loratraining

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestStatusByGatewayIDClientPreservesIDAndAuthentication(t *testing.T) {
	id := "opaque +/#?% ID"
	token := strings.Repeat("t", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/gateway-jobs" || r.URL.Query().Get("gateway_job_id") != id || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected lookup request: %s %s", r.Method, r.URL)
		}
		_ = json.NewEncoder(w).Encode(JobStatus{ID: "agent-id", GatewayJobID: id, State: "running"})
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	status, err := NewClient(baseURL, token).StatusByGatewayID(context.Background(), id)
	if err != nil || status.ID != "agent-id" {
		t.Fatalf("lookup: %+v %v", status, err)
	}
}

func TestStatusByGatewayIDClientRejectsUnprovenResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"old agent", 404, "404 page not found"},
		{"missing record", 404, `{"code":"job_not_found"}`},
		{"agent error", 503, `{"message":"unavailable"}`},
		{"invalid JSON", 200, "{"},
		{"wrong job", 200, `{"id":"agent","gateway_job_id":"other"}`},
		{"no agent ID", 200, `{"gateway_job_id":"requested"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			baseURL, _ := url.Parse(server.URL)
			status, err := NewClient(baseURL, strings.Repeat("t", 32)).StatusByGatewayID(context.Background(), "requested")
			if err == nil || status.ID != "" {
				t.Fatalf("unproven response accepted: %+v %v", status, err)
			}
		})
	}
	var client *Client
	if _, err := client.StatusByGatewayID(context.Background(), "requested"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unconfigured client: %v", err)
	}
}

func TestFenceSubmissionRejectsFalseSettlement(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"gateway_job_id":"requested","fenced":false,"settled":true}`,
		`{"gateway_job_id":"other","fenced":true,"settled":true}`,
		`{"gateway_job_id":"requested","fenced":true,"settled":true,"job":{"id":"agent","gateway_job_id":"requested","state":"running"}}`,
		`{"gateway_job_id":"requested","fenced":true,"settled":true,"job":{"id":"agent","gateway_job_id":"requested","state":"failed","execution_unconfirmed":true}}`,
		`{"gateway_job_id":"requested","fenced":true,"settled":true,"job":{"id":"agent","gateway_job_id":"other","state":"completed"}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		base, _ := url.Parse(server.URL)
		result, err := NewClient(base, strings.Repeat("f", 32)).FenceSubmission(context.Background(), "requested")
		server.Close()
		if err == nil || result.Settled {
			t.Fatalf("false settlement accepted: %s: %+v %v", body, result, err)
		}
	}
}

func TestDecodeHTTPErrorPreservesMachineCode(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader(`{"code":"job_not_found","message":"Задание не найдено."}`)),
	}
	err := decodeHTTPError(response)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("decodeHTTPError() = %T, want *HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusNotFound || httpErr.Code != "job_not_found" {
		t.Fatalf("decoded HTTP error = %+v", httpErr)
	}
}
