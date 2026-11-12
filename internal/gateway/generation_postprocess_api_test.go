package gateway

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/security"
)

func TestPostprocessHTTPParsing(t *testing.T) {
	base := url.Values{"request_id": {"postprocess-request-0001"}, "source_job_id": {"source-public-id-0001"}, "source_media_id": {"9007199254740993"}, "width": {"256"}, "height": {"160"}, "interpolate": {"1"}, "rtx_quality": {"HIGH"}}
	request, err := parsePostprocessQuoteRequest(base)
	if err != nil || request.SourceMediaID != 9007199254740993 {
		t.Fatal("ID lost precision", request, err)
	}
	for key, invalid := range map[string][]string{
		"request_id":      {"", "short", " bad-request-id-0001"},
		"source_media_id": {"0", "01", "+1", "1.0", "9e1", "9223372036854775808"},
		"width":           {"63", "4097", "0256", "Infinity"},
		"interpolate":     {"0", "5"}, "rtx_quality": {"highest"},
		"reference_media_id": {"1"}, "color_strength": {"NaN", "0.5"},
		"rife_checkpoint": {"rife47.pth", "../rife47.pth"},
	} {
		for _, value := range invalid {
			v := maps.Clone(base)
			v.Set(key, value)
			if _, err := parsePostprocessQuoteRequest(v); err == nil {
				t.Errorf("accepted %s=%q", key, value)
			}
		}
	}
	for _, tc := range []struct{ method, target, body, contentType string }{
		{"POST", "/?request_id=x", "request_id=y", "application/x-www-form-urlencoded"},
		{"POST", "/", "request_id=x&request_id=y", "application/x-www-form-urlencoded"},
		{"POST", "/", "request_id=%zz", "application/x-www-form-urlencoded"},
		{"POST", "/", "graph=x", "application/x-www-form-urlencoded"},
		{"POST", "/", "request_id=" + strings.Repeat("x", postprocessHTTPMaxBytes), "application/x-www-form-urlencoded"},
		{"POST", "/", "{}", "application/json"},
		{"GET", "/?request_id=x&request_id=y", "", ""},
		{"GET", "/?request_id=%zz", "", ""},
	} {
		r := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		if _, ok := postprocessHTTPValues(w, r, tc.method, "request_id"); ok || w.Code != 400 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("malformed request accepted", tc, w.Code)
		}
	}
}

func TestPostprocessQuoteAdmission(t *testing.T) {
	a := &App{}
	release, ok := a.acquirePostprocessQuote()
	if !ok {
		t.Fatal("first slot rejected")
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := a.acquirePostprocessQuote(); ok {
				t.Error("concurrent quote admitted")
			}
		}()
	}
	wg.Wait()
	release()
	release()
	second, ok := a.acquirePostprocessQuote()
	if !ok {
		t.Fatal("slot not released")
	}
	second()
	now := time.Now()
	if !a.allowNewPostprocessQuote(now) || a.allowNewPostprocessQuote(now.Add(time.Second)) || !a.allowNewPostprocessQuote(now.Add(3*time.Second)) {
		t.Fatal("rate bound failed")
	}
}

func TestPostprocessQuoteReceiptHTTPStates(t *testing.T) {
	a, q := postprocessQuoteFixture(t, false)
	q.CreatedAt = q.CreatedAt.Add(-10 * time.Minute)
	q.ExpiresAt = q.ExpiresAt.Add(-10 * time.Minute)
	q.Sources[0].Measurement.MeasuredAt = q.Sources[0].Measurement.MeasuredAt.Add(-10 * time.Minute)
	receipt := postprocessTestReceipt(t, a, q)
	user := &User{ID: q.UserID, Role: "admin"}
	w := httptest.NewRecorder()
	a.writePostprocessQuoteReceipt(w, context.Background(), user, receipt)
	if w.Code != 410 || !strings.Contains(w.Body.String(), `"code":"expired"`) {
		t.Fatal("expired quote exposed", w.Code, w.Body.String())
	}
	// An already accepted receipt does not expire back into a new submission.
	receipt.State, receipt.JobPublicID = "accepted", "accepted-postprocess-child"
	w = httptest.NewRecorder()
	a.writePostprocessQuoteReceipt(w, context.Background(), user, receipt)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"removed":true`) || strings.Contains(w.Body.String(), `"confirmation"`) {
		t.Fatal("accepted receipt lost identity", w.Body.String())
	}
	w = httptest.NewRecorder()
	a.writePostprocessQuoteReceipt(w, context.Background(), &User{ID: user.ID + 1, Role: "admin"}, receipt)
	if w.Code != 403 {
		t.Fatal("foreign receipt disclosed", w.Code)
	}
}

func TestPostprocessQuoteHTTPIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	f.app.csrfSigner = security.NewCSRFSigner("postprocess-test")
	job := f.enqueue(t)
	mediaID := postprocessRetainedImage(t, f, job, false)
	// JavaScript must not round this identity in either direction.
	if _, err := f.db.Exec(`UPDATE content_media SET id=9007199254740993 WHERE id=$1`, mediaID); err != nil {
		t.Fatal(err)
	}
	mediaID = 9007199254740993
	var reads, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/object_info" {
			writes.Add(1)
			w.WriteHeader(500)
			return
		}
		reads.Add(1)
		_, _ = w.Write([]byte(postprocessImageObjectInfoFixture))
	}))
	defer server.Close()
	f.app.cfg.ComfyUIUpstream, _ = url.Parse(server.URL)
	mux := http.NewServeMux()
	f.app.registerPostprocessQuoteRoutes(mux, func(h http.Handler) http.Handler { return h })
	call := func(method, endpoint string, values url.Values, want int) map[string]any {
		t.Helper()
		body := values.Encode()
		if method == "GET" {
			endpoint += "?" + body
			body = ""
		}
		r := httptest.NewRequest(method, endpoint, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "pp-session"})
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &f.user))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s status %d want %d: %s", endpoint, w.Code, want, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable response")
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"payload_cipher", "namespace", "input_name", "schemas", "graph_sha256", "sampler", "seed"} {
			if strings.Contains(w.Body.String(), `"`+forbidden+`"`) {
				t.Fatal("internal/generation data leaked", forbidden)
			}
		}
		return result
	}
	id := "postprocess-http-request-0001"
	v := url.Values{"csrf": {f.app.csrfSigner.Token("pp-session")}, "request_id": {id}, "source_job_id": {job.PublicID}, "source_media_id": {strconv.FormatInt(mediaID, 10)}, "width": {"256"}, "height": {"160"}, "interpolate": {"1"}, "rtx_quality": {"HIGH"}}
	bad := maps.Clone(v)
	bad.Set("csrf", "bad")
	call("POST", "/generate/postprocess/quote", bad, 403)
	view := call("POST", "/generate/postprocess/quote", v, 200)
	sources := view["sources"].([]any)
	if sources[0].(map[string]any)["media_id"] != "9007199254740993" || view["accepted"] != false || view["cost"] != float64(1) {
		t.Fatal("invalid quote", view)
	}
	count := reads.Load()
	retry := call("POST", "/generate/postprocess/quote", v, 200)
	status := call("GET", "/generate/postprocess/status", url.Values{"request_id": {id}}, 200)
	if retry["confirmation"] != view["confirmation"] || status["confirmation"] != view["confirmation"] || reads.Load() != count {
		t.Fatal("retry re-planned")
	}
	changed := maps.Clone(v)
	changed.Set("rtx_quality", "LOW")
	call("POST", "/generate/postprocess/quote", changed, 409)
	f.app.postprocessQuoteMu.Lock()
	f.app.postprocessQuoteNext = time.Now().Add(time.Minute)
	f.app.postprocessQuoteMu.Unlock()
	fresh := maps.Clone(v)
	fresh.Set("request_id", "postprocess-http-request-0002")
	call("POST", "/generate/postprocess/quote", fresh, 429)
	if _, err := f.db.Exec(`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, mediaID); err != nil {
		t.Fatal(err)
	}
	call("GET", "/generate/postprocess/status", url.Values{"request_id": {id}}, 404)
	call("POST", "/generate/postprocess/quote", v, 404)
	if _, err := f.db.Exec(`UPDATE content_media SET profile_hidden_at=NULL WHERE id=$1`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	call("GET", "/generate/postprocess/status", url.Values{"request_id": {id}}, 403)
	if _, err := f.db.Exec(`UPDATE users SET disabled=false WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	discard := url.Values{"csrf": {v.Get("csrf")}, "request_id": {id}}
	if result := call("POST", "/generate/postprocess/discard", discard, 200); result["state"] != "cancelled" {
		t.Fatal(result)
	}
	call("POST", "/generate/postprocess/quote", v, 409)
	if result := call("GET", "/generate/postprocess/status", url.Values{"request_id": {id}}, 200); result["state"] != "cancelled" {
		t.Fatal(result)
	}
	discard.Set("request_id", fresh.Get("request_id"))
	call("POST", "/generate/postprocess/discard", discard, 200)
	call("POST", "/generate/postprocess/quote", fresh, 409)
	// Accepted receipts are observable after source removal, but these read-only
	// routes cannot reserve quota, enqueue work or recreate a deleted child.
	request, err := parsePostprocessQuoteRequest(v)
	if err != nil {
		t.Fatal(err)
	}
	request.RequestID = "postprocess-http-accepted-0001"
	receipt, _, err := f.app.createPostprocessQuote(context.Background(), f.user.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _, err := f.app.store.ConfirmGenerationPostprocess(context.Background(), f.user.ID, request.RequestID, receipt.ConfirmationSHA256, "postprocess-http-child-0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, mediaID); err != nil {
		t.Fatal(err)
	}
	acceptedStatus := url.Values{"request_id": {request.RequestID}}
	result := call("GET", "/generate/postprocess/status", acceptedStatus, 200)
	if result["accepted"] != true || result["job_id"] != accepted.JobPublicID || result["removed"] != false {
		t.Fatal(result)
	}
	acceptedStatus.Set("csrf", v.Get("csrf"))
	call("POST", "/generate/postprocess/discard", acceptedStatus, 200)
	var queuedChild bool
	if err := f.db.QueryRow(`SELECT dispatch_queued_at IS NOT NULL OR quota_reserved_on IS NOT NULL FROM generation_jobs WHERE id=$1`, *accepted.JobID).Scan(&queuedChild); err != nil || queuedChild {
		t.Fatal("read published accepted child", queuedChild, err)
	}
	if _, err := f.db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, *accepted.JobID); err != nil {
		t.Fatal(err)
	}
	acceptedStatus.Del("csrf")
	if result := call("GET", "/generate/postprocess/status", acceptedStatus, 200); result["removed"] != true || result["job_id"] != accepted.JobPublicID {
		t.Fatal("deleted identity lost", result)
	}
	owner := f.user
	var otherID int64
	if err := f.db.QueryRow(`INSERT INTO users(username,password_hash,role,can_use_quick_generation) VALUES('pp-http-other','disabled','admin',true) RETURNING id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	f.user, err = f.app.store.UserByID(context.Background(), otherID)
	if err != nil {
		t.Fatal(err)
	}
	call("GET", "/generate/postprocess/status", acceptedStatus, 404)
	f.user = owner
	var jobs, queued int
	if err := f.db.QueryRow(`SELECT count(*),count(*) FILTER(WHERE template_id LIKE 'postprocess%' AND dispatch_queued_at IS NOT NULL) FROM generation_jobs`).Scan(&jobs, &queued); err != nil || jobs != 1 || queued != 0 {
		t.Fatal("quote API created work", jobs, queued, err)
	}
	entries, err := os.ReadDir(f.app.cfg.MediaSpoolDir)
	if err != nil || len(entries) != 0 || writes.Load() != 0 || f.app.postprocessQuoteBusy {
		t.Fatal("leak or upstream write", entries, err)
	}
}
