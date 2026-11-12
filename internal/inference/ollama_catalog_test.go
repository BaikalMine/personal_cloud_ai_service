package inference

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
)

func TestOllamaCatalogCacheBoundaries(t *testing.T) {
	now := time.Now()
	for _, route := range []string{"/api/tags", "/v1/models"} {
		t.Run(route, func(t *testing.T) {
			var c ollamaCatalogCache
			body := []byte(`{"models":[],"data":[]}`)
			c.put(route, "application/json; charset=utf-8", body, now)
			body[0] = '!'
			for _, method := range []string{"GET", "HEAD"} {
				w := httptest.NewRecorder()
				if !c.serve(w, httptest.NewRequest(method, route, nil), now) || w.Code != 200 || w.Header().Get("X-AI-Gateway-Catalog") != "cached" {
					t.Fatal("cache miss", method)
				}
				if method == "GET" && w.Body.String() != `{"models":[],"data":[]}` {
					t.Fatal("cache body mutated")
				}
				if method == "HEAD" && w.Body.Len() != 0 {
					t.Fatal("HEAD body")
				}
			}
			for _, at := range []time.Time{now.Add(-time.Second), now.Add(ollamaCatalogMaxAge)} {
				if c.serve(httptest.NewRecorder(), httptest.NewRequest("GET", route, nil), at) {
					t.Fatal("invalid cache age accepted")
				}
			}
			if c.serve(httptest.NewRecorder(), httptest.NewRequest("POST", route, nil), now) {
				t.Fatal("cached POST")
			}
		})
	}
	for _, body := range []string{`{`, `null`, `{}`, `{"models":null}`, `{"models":{}}`, `{"models":[]}` + strings.Repeat(" ", ollamaCatalogLimit)} {
		var c ollamaCatalogCache
		c.put("/api/tags", "application/json", []byte(body), now)
		if len(c.entries) != 0 {
			t.Fatal("cached invalid or oversized catalog")
		}
	}
	var c ollamaCatalogCache
	c.put("/api/ps", "application/json", []byte(`{"models":[]}`), now)
	c.put("/api/tags", "text/plain", []byte(`{"models":[]}`), now)
	if len(c.entries) != 0 {
		t.Fatal("cached state or non-JSON")
	}
	var capture ollamaCatalogCapture
	chunk := bytes.Repeat([]byte("x"), ollamaCatalogLimit)
	if n, err := capture.Write(chunk); n != len(chunk) || err != nil || capture.overflow {
		t.Fatal("exact capture limit")
	}
	if n, err := capture.Write([]byte("x")); n != 1 || err != nil || !capture.overflow || capture.body != nil {
		t.Fatal("overflow capture retained data or interrupted response")
	}
}

func TestOllamaCatalogBusyFallback(t *testing.T) {
	x := newOllamaHTTPFixture(t)
	x.runtime.handle = func(ctx context.Context, request modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
		return modelruntime.Result{Status: 200, Dispatched: true, Stopped: true}, consume(200, http.Header{"Content-Type": {"application/json"}}, strings.NewReader(`{"models":[{"model":"fixture"}]}`))
	}
	request := func(method, route, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, route, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		x.api.ServeHTTP(w, r)
		return w
	}
	key := x.api.config.ClientKey
	if w := request("GET", "/api/tags", "", key); w.Code != 200 || w.Header().Get("X-AI-Gateway-Catalog") != "" {
		t.Fatal("catalog did not prime", w.Code)
	}
	x.worker.runMu.Lock()
	func() {
		defer x.worker.runMu.Unlock()
		for _, method := range []string{"GET", "HEAD"} {
			if w := request(method, "/api/tags", "", key); w.Code != 200 || w.Header().Get("X-AI-Gateway-Catalog") != "cached" {
				t.Fatal("busy cache miss", method, w.Code)
			}
		}
		for _, route := range []string{"/api/ps", "/api/version", "/v1/models"} {
			if w := request("GET", route, "", key); w.Code != 409 {
				t.Fatal("invented uncached response", route, w.Code)
			}
		}
		if w := request("GET", "/api/tags", "", "wrong"); w.Code != 401 {
			t.Fatal("cache bypassed auth")
		}
		if w := request("GET", "/api/tags", "unexpected", key); w.Code != 400 {
			t.Fatal("cache bypassed body validation")
		}
		if w := request("GET", "/api/tags?x=1", "", key); w.Code != 404 {
			t.Fatal("cache bypassed route validation")
		}
	}()
	x.admission = func(w http.ResponseWriter, r *http.Request, permit domain.GPUExecutorPermitRequest) {
		w.WriteHeader(409)
		io.WriteString(w, `{"protocol":1,"error":"executor_permit_unavailable"}`)
	}
	if w := request("GET", "/api/tags", "", key); w.Code != 200 || w.Header().Get("X-AI-Gateway-Catalog") != "cached" {
		t.Fatal("external contention lost catalog")
	}
	if x.runtime.httpCalls.Load() != 1 {
		t.Fatal("busy metadata started runtime")
	}
	x.admission = func(w http.ResponseWriter, r *http.Request, permit domain.GPUExecutorPermitRequest) {
		w.WriteHeader(503)
	}
	if w := request("GET", "/api/tags", "", key); w.Code != 503 {
		t.Fatal("cache hid admission failure")
	}
	x.api.Close()
	if w := request("GET", "/api/tags", "", key); w.Code != 503 {
		t.Fatal("cache served closed executor")
	}
}

func TestOllamaCatalogRequiresConfirmedResponse(t *testing.T) {
	for _, mode := range []string{"upstream error", "read error", "not stopped", "receipt failure", "success"} {
		t.Run(mode, func(t *testing.T) {
			x := newOllamaHTTPFixture(t)
			if mode == "receipt failure" {
				_, err := x.db.Exec(`CREATE FUNCTION reject_catalog_stop_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='closed' THEN RAISE EXCEPTION 'synthetic receipt failure'; END IF; RETURN NEW; END $$;
				 CREATE TRIGGER reject_catalog_stop_fixture BEFORE UPDATE ON gpu_executor_permits FOR EACH ROW EXECUTE FUNCTION reject_catalog_stop_fixture()`)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = x.db.Exec(`DROP FUNCTION IF EXISTS reject_catalog_stop_fixture() CASCADE`) })
			}
			x.runtime.handle = func(ctx context.Context, request modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
				status := 200
				if mode == "upstream error" {
					status = 500
				}
				err := consume(status, http.Header{"Content-Type": {"application/json"}}, strings.NewReader(`{"models":[]}`))
				if mode == "read error" {
					err = errors.New("synthetic read failure")
				}
				return modelruntime.Result{Status: status, Dispatched: true, Stopped: mode != "not stopped"}, err
			}
			r := httptest.NewRequest("GET", "/api/tags", nil)
			r.Header.Set("Authorization", "Bearer "+x.api.config.ClientKey)
			func() {
				defer func() {
					if p := recover(); p != nil && p != http.ErrAbortHandler {
						panic(p)
					}
				}()
				x.api.ServeHTTP(httptest.NewRecorder(), r)
			}()
			cached := x.api.catalog.serve(httptest.NewRecorder(), r, time.Now())
			if cached != (mode == "success") {
				t.Fatal("incorrect cache publication", mode)
			}
		})
	}
}
