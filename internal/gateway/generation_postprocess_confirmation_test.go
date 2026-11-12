package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

type postprocessConfirmationFixture struct {
	f             *dispatchFixture
	receipt       domain.GenerationPostprocessReceipt
	quote         postprocessQuote
	reads, writes atomic.Int32
	offline       atomic.Bool
	catalog, hook atomic.Value
}

func newPostprocessConfirmationFixture(t *testing.T) *postprocessConfirmationFixture {
	t.Helper()
	f := newDispatchFixture(t)
	f.app.csrfSigner = security.NewCSRFSigner("postprocess-confirm-test")
	parent := f.enqueue(t)
	media := postprocessRetainedImage(t, f, parent, false)
	p := &postprocessConfirmationFixture{f: f}
	p.catalog.Store(postprocessImageObjectInfoFixture)
	p.hook.Store(func() {})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/object_info" {
			p.writes.Add(1)
			http.Error(w, "unexpected upstream action", 500)
			return
		}
		p.reads.Add(1)
		p.hook.Load().(func())()
		if p.offline.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(p.catalog.Load().(string)))
	}))
	t.Cleanup(server.Close)
	f.app.cfg.ComfyUIUpstream, _ = url.Parse(server.URL)
	request := postprocessQuoteRequest{RequestID: "postprocess-confirm-request-0001", SourceJobID: parent.PublicID, SourceMediaID: media,
		Options: postprocessOptions{Width: 256, Height: 160, Interpolate: 1, RTXQuality: "HIGH"}}
	var err error
	p.receipt, p.quote, err = f.app.createPostprocessQuote(context.Background(), f.user.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		entries, err := os.ReadDir(f.app.cfg.MediaSpoolDir)
		if err != nil || len(entries) != 0 || f.app.postprocessQuoteBusy || p.writes.Load() != 0 {
			t.Error("spool/slot leak or upstream action", entries, err, p.writes.Load())
		}
	})
	return p
}

func (p *postprocessConfirmationFixture) values() url.Values {
	return url.Values{"csrf": {p.f.app.csrfSigner.Token("pp-confirm-session")}, "request_id": {p.receipt.RequestID}, "confirmation": {p.receipt.ConfirmationSHA256}}
}

func (p *postprocessConfirmationFixture) call(method, endpoint string, values url.Values) *httptest.ResponseRecorder {
	body := values.Encode()
	if method == http.MethodGet {
		endpoint += "?" + body
		body = ""
	}
	r := httptest.NewRequest(method, endpoint, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "pp-confirm-session"})
	r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &p.f.user))
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	p.f.app.registerPostprocessQuoteRoutes(mux, func(h http.Handler) http.Handler { return h })
	mux.ServeHTTP(w, r)
	return w
}

func postprocessHTTPResult(t *testing.T, w *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HTTP %d want %d: %s", w.Code, status, w.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"seed", "sampler", "payload_cipher", "input_name", "namespace", "schemas", "graph_sha256"} {
		if strings.Contains(w.Body.String(), `"`+key+`"`) {
			t.Fatal("internal data exposed", key)
		}
	}
	return result
}

func (p *postprocessConfirmationFixture) accept(t *testing.T) domain.GenerationPostprocessReceipt {
	t.Helper()
	r, err := p.f.app.confirmPostprocessQuote(context.Background(), p.f.user.ID, p.receipt.RequestID, p.receipt.ConfirmationSHA256)
	if err != nil || r.JobID == nil || r.State != "accepted" {
		t.Fatal(r, err)
	}
	return r
}

func (p *postprocessConfirmationFixture) restartedApp() *App {
	return &App{store: store.New(p.f.db), contentCipher: p.f.app.contentCipher, cfg: p.f.app.cfg}
}

func TestPostprocessConfirmationDigest(t *testing.T) {
	if !postprocessConfirmationValid(strings.Repeat("ab", 32)) {
		t.Fatal("valid digest rejected")
	}
	for _, value := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("z", 64)} {
		if postprocessConfirmationValid(value) {
			t.Fatal("invalid digest accepted", value)
		}
	}
}

func TestPostprocessConfirmationHTTPIntegration(t *testing.T) {
	p := newPostprocessConfirmationFixture(t)
	v := p.values()
	for _, tc := range []struct {
		field, value string
		status       int
	}{
		{"csrf", "bad", 403}, {"confirmation", "", 422}, {"confirmation", strings.Repeat("c", 64), 409},
		{"request_id", "short", 422}, {"seed", "42", 400},
	} {
		bad := maps.Clone(v)
		bad.Set(tc.field, tc.value)
		postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/confirm", bad), tc.status)
	}
	bad := maps.Clone(v)
	bad["confirmation"] = []string{v.Get("confirmation"), v.Get("confirmation")}
	postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/confirm", bad), 400)
	postprocessHTTPResult(t, p.call("GET", "/generate/postprocess/confirm", v), 405)
	result := postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/confirm", v), 200)
	jobID, _ := result["job_id"].(string)
	if jobID == "" || result["accepted"] != true || result["publication_pending"] != false || result["job"].(map[string]any)["operation"] != "postprocess" {
		t.Fatal(result)
	}
	reads := p.reads.Load()
	responses := make(chan *httptest.ResponseRecorder, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); responses <- p.call("POST", "/generate/postprocess/confirm", v) }()
	}
	wg.Wait()
	close(responses)
	for response := range responses {
		if postprocessHTTPResult(t, response, 200)["job_id"] != jobID {
			t.Fatal("retry changed child")
		}
	}
	if p.reads.Load() != reads {
		t.Fatal("accepted retry repeated inspection")
	}
	var children, reservations, queued int
	if err := p.f.db.QueryRow(`SELECT count(*),count(*) FILTER(WHERE quota_reserved_on IS NOT NULL),count(*) FILTER(WHERE dispatch_queued_at IS NOT NULL) FROM generation_jobs WHERE template_id=$1`, domain.PostprocessImageTemplate).Scan(&children, &reservations, &queued); err != nil || children != 1 || reservations != 1 || queued != 1 {
		t.Fatal(children, reservations, queued, err)
	}
	if _, err := p.f.db.Exec(`UPDATE content_media SET profile_hidden_at=now()`); err != nil {
		t.Fatal(err)
	}
	if postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/confirm", v), 200)["job_id"] != jobID {
		t.Fatal("hidden source recreated accepted job")
	}
	if _, err := p.f.db.Exec(`DELETE FROM generation_jobs WHERE public_id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	removed := postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/confirm", v), 200)
	if removed["removed"] != true || removed["job_id"] != jobID {
		t.Fatal("deleted child recreated", removed)
	}
}

func TestPostprocessConfirmationRevalidationIntegration(t *testing.T) {
	for _, name := range []string{"offline", "schema changed", "hidden source", "changed source", "disabled", "expired"} {
		t.Run(name, func(t *testing.T) {
			p := newPostprocessConfirmationFixture(t)
			status := 404
			switch name {
			case "offline":
				p.offline.Store(true)
				status = 503
			case "schema changed":
				p.catalog.Store(strings.Replace(postprocessImageObjectInfoFixture, `"output":["IMAGE","MASK"]`, `"output":["MASK","IMAGE"]`, 1))
			case "hidden source":
				if _, err := p.f.db.Exec(`UPDATE content_media SET profile_hidden_at=now()`); err != nil {
					t.Fatal(err)
				}
			case "changed source":
				if _, err := p.f.db.Exec(`UPDATE content_media SET content_hash=repeat('c',64)`); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if _, err := p.f.db.Exec(`UPDATE users SET disabled=true`); err != nil {
					t.Fatal(err)
				}
				status = 403
			case "expired":
				q := p.quote
				q.CreatedAt = q.CreatedAt.Add(-10 * time.Minute)
				q.ExpiresAt = q.ExpiresAt.Add(-10 * time.Minute)
				for i := range q.Sources {
					q.Sources[i].Measurement.MeasuredAt = q.Sources[i].Measurement.MeasuredAt.Add(-10 * time.Minute)
				}
				cipher, digest, err := p.f.app.encodePostprocessQuote(q)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.f.db.Exec(`UPDATE generation_postprocess_receipts SET payload_cipher=$1,confirmation_sha256=$2,expires_at=$3`, cipher, digest, q.ExpiresAt); err != nil {
					t.Fatal(err)
				}
				p.receipt.ConfirmationSHA256 = digest
				status = 410
			}
			postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/confirm", p.values()), status)
			r, err := p.f.app.store.GenerationPostprocessByRequest(context.Background(), p.f.user.ID, p.receipt.RequestID)
			if err != nil || r.State != "quoted" || r.JobID != nil {
				t.Fatal("invalid plan accepted", r, err)
			}
		})
	}
}

func TestPostprocessConfirmationDiscardRaceIntegration(t *testing.T) {
	p := newPostprocessConfirmationFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	p.hook.Store(func() { once.Do(func() { close(entered) }); <-release })
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- p.call("POST", "/generate/postprocess/confirm", p.values()) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		<-response
		t.Fatal("confirmation did not inspect live schema")
	}
	discard := p.values()
	discard.Del("confirmation")
	result := p.call("POST", "/generate/postprocess/discard", discard)
	close(release)
	postprocessHTTPResult(t, result, 200)
	postprocessHTTPResult(t, <-response, 409)
	r, err := p.f.app.store.GenerationPostprocessByRequest(context.Background(), p.f.user.ID, p.receipt.RequestID)
	if err != nil || r.State != "cancelled" || r.JobID != nil {
		t.Fatal("discard fence lost", r, err)
	}
}

func TestPostprocessPublicationRestartIntegration(t *testing.T) {
	p := newPostprocessConfirmationFixture(t)
	r := p.accept(t)
	status := postprocessHTTPResult(t, p.call("GET", "/generate/postprocess/status", url.Values{"request_id": {r.RequestID}}), 200)
	if status["publication_pending"] != true {
		t.Fatal("lost-response state missing", status)
	}
	if _, err := p.f.db.Exec(`UPDATE generation_jobs SET state_changed_at=now()-interval '1 hour' WHERE id=$1`, *r.JobID); err != nil {
		t.Fatal(err)
	}
	reads := p.reads.Load()
	app := p.restartedApp()
	for range 2 {
		if n, err := app.refreshTrackedGenerationStatuses(context.Background()); err != nil || n != 1 {
			t.Fatal("restart reconciliation", n, err)
		}
	}
	job, err := app.store.GenerationJobByPublicID(context.Background(), p.f.user.ID, r.JobPublicID)
	if err != nil || job.ID != *r.JobID || job.State != domain.GenerationJobWaitingForResources || job.DispatchQueuedAt == nil || job.QuotaReservedOn == nil {
		t.Fatal("accepted child not recovered", job, err)
	}
	if p.reads.Load() != reads {
		t.Fatal("recovery decoded/replanned accepted quote")
	}
	if got := postprocessHTTPResult(t, p.call("GET", "/generate/postprocess/status", url.Values{"request_id": {r.RequestID}}), 200); got["publication_pending"] != false || got["job_id"] != r.JobPublicID {
		t.Fatal(got)
	}
}

func TestPostprocessConfirmationPublicationFailureRecoveryIntegration(t *testing.T) {
	p := newPostprocessConfirmationFixture(t)
	ctx := context.Background()
	if _, err := p.f.db.Exec(`CREATE FUNCTION fail_confirm_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.template_id='postprocess-image-v1' AND NEW.dispatch_queued_at IS NOT NULL THEN RAISE EXCEPTION 'injected publication failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := p.f.db.Exec(`DROP TRIGGER IF EXISTS fail_confirm_publication ON generation_jobs`); err != nil {
			t.Error(err)
		}
		if _, err := p.f.db.Exec(`DROP FUNCTION IF EXISTS fail_confirm_publication()`); err != nil {
			t.Error(err)
		}
	})
	if _, err := p.f.db.Exec(`CREATE TRIGGER fail_confirm_publication BEFORE UPDATE ON generation_jobs FOR EACH ROW EXECUTE FUNCTION fail_confirm_publication()`); err != nil {
		t.Fatal(err)
	}
	result := postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/confirm", p.values()), 200)
	if result["accepted"] != true || result["publication_pending"] != true {
		t.Fatal("durable acceptance hidden by publication error", result)
	}
	r, err := p.f.app.store.GenerationPostprocessByRequest(ctx, p.f.user.ID, p.receipt.RequestID)
	if err != nil || r.JobID == nil || result["job_id"] != r.JobPublicID {
		t.Fatal(r, err)
	}
	job, err := p.f.app.store.GenerationJobByPublicID(ctx, p.f.user.ID, r.JobPublicID)
	if err != nil || job.QuotaReservedOn != nil || job.DispatchQueuedAt != nil {
		t.Fatal("partial publication", job, err)
	}
	// Observing the receipt must remain read-only even while publication fails.
	postprocessHTTPResult(t, p.call("GET", "/generate/postprocess/status", url.Values{"request_id": {r.RequestID}}), 200)
	discard := p.values()
	discard.Del("confirmation")
	postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/discard", discard), 200)
	if _, err := p.f.db.Exec(`DROP TRIGGER fail_confirm_publication ON generation_jobs`); err != nil {
		t.Fatal(err)
	}
	app := p.restartedApp()
	if _, err := app.refreshTrackedGenerationStatuses(ctx); err != nil {
		t.Fatal(err)
	}
	job, err = app.store.GenerationJobByPublicID(ctx, p.f.user.ID, r.JobPublicID)
	if err != nil || job.ID != *r.JobID || job.QuotaReservedOn == nil || job.DispatchQueuedAt == nil {
		t.Fatal("publication not recovered", job, err)
	}
	if result := postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/confirm", p.values()), 200); result["job_id"] != r.JobPublicID || result["publication_pending"] != false {
		t.Fatal(result)
	}
}

func TestPostprocessPublicationPermanentFailureIntegration(t *testing.T) {
	for name, statement := range map[string]string{
		"quota":    `UPDATE users SET generation_total_limit=1,generation_total_used=1`,
		"source":   `UPDATE content_media SET profile_hidden_at=now()`,
		"account":  `UPDATE users SET disabled=true`,
		"payload":  `UPDATE generation_jobs SET payload_cipher='changed' WHERE template_id='postprocess-image-v1'`,
		"workflow": `UPDATE generation_jobs SET workflow_id='changed' WHERE template_id='postprocess-image-v1'`,
	} {
		t.Run(name, func(t *testing.T) {
			p := newPostprocessConfirmationFixture(t)
			r := p.accept(t)
			if _, err := p.f.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			app := p.restartedApp()
			if _, err := app.refreshTrackedGenerationStatuses(context.Background()); err != nil {
				t.Fatal(err)
			}
			job, err := app.store.GenerationJobByPublicID(context.Background(), p.f.user.ID, r.JobPublicID)
			if err != nil || job.State != domain.GenerationJobFailed || job.ResourcesReleasedAt == nil || job.QuotaReservedOn != nil || job.DispatchQueuedAt != nil || !strings.HasPrefix(job.ErrorCode, "postprocess_publish_") {
				t.Fatal("failed publication not settled", job, err)
			}
			if _, err := app.resumePostprocessPublication(context.Background(), p.f.user.ID, r.RequestID); err != nil {
				t.Fatal("terminal retry", err)
			}
			if _, err := app.store.ClaimNextGenerationDispatch(context.Background(), "postprocess-failed-claim", 1); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("failed work claimed", err)
			}
		})
	}
}
