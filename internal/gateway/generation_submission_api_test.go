package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
)

func submissionRequest(f *dispatchFixture, method, path string, values url.Values) *http.Request {
	var body string
	if method == http.MethodGet {
		path += "?" + values.Encode()
	} else {
		body = values.Encode()
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "fixture-session"})
	return r.WithContext(context.WithValue(r.Context(), userCtxKey, &f.user))
}

func TestGenerationSubmissionHTTPIntegration(t *testing.T) {
	for _, kind := range []string{"single", "batch"} {
		t.Run(kind, func(t *testing.T) {
			f := newDispatchFixture(t)
			f.app.csrfSigner = security.NewCSRFSigner("submission-csrf-test-secret")
			requestID := newRequestID()
			values := url.Values{"kind": {kind}, "request_id": {requestID}}
			call := func(method, path string, data url.Values, status int) map[string]any {
				t.Helper()
				w := httptest.NewRecorder()
				f.app.handleGenerationSubmission(w, submissionRequest(f, method, path, data))
				if w.Code != status || w.Header().Get("Cache-Control") != "private, no-store" {
					t.Fatal(w.Code, w.Body.String())
				}
				var payload map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				return payload
			}
			unknown := call(http.MethodGet, "/generate/submission", values, 404)
			if unknown["request_closed"] != false || unknown["state"] != "unknown" {
				t.Fatal(unknown)
			}
			call(http.MethodPost, "/generate/submission/close", values, 403)
			values.Set("csrf", f.app.csrfSigner.Token("fixture-session"))
			closed := call(http.MethodPost, "/generate/submission/close", values, 200)
			if closed["request_closed"] != true || closed["request_id"] != requestID || closed["kind"] != kind {
				t.Fatal(closed)
			}
			values.Del("csrf")
			if got := call(http.MethodGet, "/generate/submission", values, 200); got["request_closed"] != true {
				t.Fatal(got)
			}
			f.form.Set("csrf", f.app.csrfSigner.Token("fixture-session"))
			f.form.Set("client_request_id", requestID)
			f.form.Set("correlation_id", newRequestID())
			handler, path := f.app.handleGenerateRun, "/generate/run"
			if kind == "batch" {
				handler, path = f.app.handleGenerationBatches, "/generate/batches"
				f.form.Set("batch_count", "2")
				f.form.Set("batch_mode", string(domain.GenerationBatchSeeds))
			}
			w := httptest.NewRecorder()
			handler(w, submissionRequest(f, http.MethodPost, path, f.form))
			if w.Code != 410 || !strings.Contains(w.Body.String(), `"request_closed":true`) {
				t.Fatal("late POST was not fenced", w.Code, w.Body.String())
			}
			var count int
			if err := f.db.QueryRow(`SELECT (SELECT count(*) FROM generation_jobs)+(SELECT count(*) FROM generation_batches)`).Scan(&count); err != nil || count != 0 || f.posts.Load() != 0 {
				t.Fatal("late request created work", count, f.posts.Load(), err)
			}
		})
	}
}

func TestGenerationSubmissionBoundAndExpiredHTTPIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	f.app.csrfSigner = security.NewCSRFSigner("submission-csrf-test-secret")
	job := f.enqueue(t)
	values := url.Values{"kind": {"single"}, "request_id": {job.RequestID}, "csrf": {f.app.csrfSigner.Token("fixture-session")}}
	w := httptest.NewRecorder()
	f.app.handleGenerationSubmission(w, submissionRequest(f, http.MethodPost, "/generate/submission/close", values))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"job_id":"`+job.PublicID+`"`) || !strings.Contains(w.Body.String(), `"request_closed":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	var requested bool
	if err := f.db.QueryRow(`SELECT cancellation_requested_at IS NOT NULL FROM generation_jobs WHERE id=$1`, job.ID).Scan(&requested); err != nil || requested {
		t.Fatal("creation closure impersonated executor cancellation", requested, err)
	}
	if _, err := f.db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	values.Del("csrf")
	w = httptest.NewRecorder()
	f.app.handleGenerationSubmission(w, submissionRequest(f, http.MethodGet, "/generate/submission", values))
	if w.Code != 410 || !strings.Contains(w.Body.String(), `"request_closed":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestGenerationSubmissionValidationHTTPIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	f.app.csrfSigner = security.NewCSRFSigner("submission-csrf-test-secret")
	for _, tc := range []struct {
		name, method, path, allow string
		values                    url.Values
		status                    int
	}{
		{"get rejects post", "POST", "/generate/submission", "GET", nil, 405},
		{"close rejects get", "GET", "/generate/submission/close", "POST", nil, 405},
		{"close rejects delete", "DELETE", "/generate/submission/close", "POST", nil, 405},
		{"missing", "GET", "/generate/submission", "", nil, 400},
		{"invalid kind", "GET", "/generate/submission", "", url.Values{"kind": {"other"}, "request_id": {newRequestID()}}, 400},
		{"short id", "GET", "/generate/submission", "", url.Values{"kind": {"single"}, "request_id": {"short"}}, 400},
		{"duplicate kind", "GET", "/generate/submission", "", url.Values{"kind": {"single", "batch"}, "request_id": {newRequestID()}}, 400},
		{"duplicate id", "GET", "/generate/submission", "", url.Values{"kind": {"single"}, "request_id": {newRequestID(), newRequestID()}}, 400},
		{"extra field", "GET", "/generate/submission", "", url.Values{"kind": {"single"}, "request_id": {newRequestID()}, "user_id": {"2"}}, 400},
		{"close extra field", "POST", "/generate/submission/close", "", url.Values{"kind": {"single"}, "request_id": {newRequestID()}, "user_id": {"2"}}, 400},
		{"close duplicate id", "POST", "/generate/submission/close", "", url.Values{"kind": {"single"}, "request_id": {newRequestID(), newRequestID()}}, 400},
		{"oversized", "POST", "/generate/submission/close", "", url.Values{"kind": {"single"}, "request_id": {newRequestID()}, "padding": {strings.Repeat("x", 4096)}}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.method == "POST" && tc.values != nil {
				tc.values.Set("csrf", f.app.csrfSigner.Token("fixture-session"))
			}
			w := httptest.NewRecorder()
			f.app.handleGenerationSubmission(w, submissionRequest(f, tc.method, tc.path, tc.values))
			if w.Code != tc.status || w.Header().Get("Allow") != tc.allow || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(w.Code, w.Header(), w.Body.String())
			}
		})
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM generation_submissions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid request persisted a receipt", count, err)
	}
}

func TestGenerationSubmissionForeignOwnerHTTPIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	f.app.csrfSigner = security.NewCSRFSigner("submission-csrf-test-secret")
	job := f.enqueue(t)
	other := f.user
	if err := f.db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('submission-http-other','hash','user') RETURNING id`).Scan(&other.ID); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"kind": {"single"}, "request_id": {job.RequestID}}
	for _, method := range []string{"GET", "POST"} {
		path, status := "/generate/submission", 404
		if method == "POST" {
			path, status = "/generate/submission/close", 200
			values.Set("csrf", f.app.csrfSigner.Token("fixture-session"))
		}
		r := submissionRequest(f, method, path, values)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &other))
		w := httptest.NewRecorder()
		f.app.handleGenerationSubmission(w, r)
		if w.Code != status || strings.Contains(w.Body.String(), job.PublicID) || strings.Contains(w.Body.String(), `"job":`) {
			t.Fatal("foreign job leaked", w.Code, w.Body.String())
		}
	}
	var closed, cancelled bool
	if err := f.db.QueryRow(`SELECT s.closed,j.cancellation_requested_at IS NOT NULL FROM generation_submissions s
		JOIN generation_jobs j ON j.public_id=s.public_id WHERE s.user_id=$1 AND s.kind='single' AND s.request_id=$2`, f.user.ID, job.RequestID).Scan(&closed, &cancelled); err != nil || closed || cancelled {
		t.Fatal("foreign owner changed original submission", closed, cancelled, err)
	}
	if f.posts.Load() != 0 {
		t.Fatal("read or close contacted the executor", f.posts.Load())
	}
}
