package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

type postprocessInputFixture struct {
	*dispatchFixture
	job     domain.GenerationJob
	quote   postprocessQuote
	filesMu sync.Mutex
	files   map[string][]byte
	names   []string
	uploads atomic.Int32
	views   atomic.Int32
	mode    atomic.Value
}

func newPostprocessInputFixture(t *testing.T) *postprocessInputFixture {
	t.Helper()
	f := &postprocessInputFixture{dispatchFixture: newDispatchFixture(t), files: map[string][]byte{}}
	f.mode.Store("ok")
	parent := f.enqueue(t)
	mediaID := postprocessRetainedImage(t, f.dispatchFixture, parent, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/object_info":
			if f.mode.Load() == "schema-offline" {
				http.Error(w, "offline", 503)
				return
			}
			_, _ = w.Write([]byte(postprocessImageObjectInfoFixture))
		case "/upload/image":
			f.uploads.Add(1)
			if f.mode.Load() == "reject-upload" {
				http.Error(w, "upstream size limit", http.StatusRequestEntityTooLarge)
				return
			}
			if r.Method != http.MethodPost {
				t.Error("upload was not POST")
				http.Error(w, "method", 400)
				return
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				http.Error(w, "multipart", 400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, header, err := r.FormFile("image")
			if err != nil {
				t.Error(err)
				http.Error(w, "file", 400)
				return
			}
			defer file.Close()
			payload, err := io.ReadAll(io.LimitReader(file, 1<<20))
			if err != nil {
				t.Error(err)
				return
			}
			if r.FormValue("type") != "input" || r.FormValue("overwrite") != "false" {
				t.Error("mutable or non-input upload")
			}
			name := r.FormValue("subfolder") + "/" + header.Filename
			f.filesMu.Lock()
			f.names = append(f.names, name)
			if _, exists := f.files[name]; exists {
				t.Error("same input path uploaded twice")
			}
			if f.mode.Load() != "lost-before-write" {
				f.files[name] = payload
			}
			f.filesMu.Unlock()
			if f.mode.Load() == "revoke" {
				if _, err := f.db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, f.user.ID); err != nil {
					t.Error(err)
				}
			}
			if f.mode.Load() == "cancel" {
				if _, err := f.db.Exec(`UPDATE generation_jobs SET cancellation_requested_at=now() WHERE id=$1`, f.job.ID); err != nil {
					t.Error(err)
				}
			}
			if f.mode.Load() == "lost-response" || f.mode.Load() == "lost-before-write" {
				http.Error(w, "upstream timeout", 504)
				return
			}
			subfolder := r.FormValue("subfolder")
			if f.mode.Load() == "foreign-response" {
				subfolder = "gateway/foreign-user"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"name": header.Filename, "subfolder": subfolder, "type": "input"})
		case "/view":
			f.views.Add(1)
			q := r.URL.Query()
			if r.Method != http.MethodGet || len(q) != 3 || q.Get("type") != "input" || q.Has("preview") || q.Has("channel") {
				t.Error("transformed or non-input readback")
			}
			name := q.Get("subfolder") + "/" + q.Get("filename")
			f.filesMu.Lock()
			data, exists := f.files[name]
			payload := bytes.Clone(data)
			f.filesMu.Unlock()
			if !exists {
				http.NotFound(w, r)
				return
			}
			if f.mode.Load() == "mutated-readback" && len(payload) > 0 {
				payload[0] ^= 1
			}
			_, _ = w.Write(payload)
		default:
			f.posts.Add(1)
			http.Error(w, "preparation cannot submit or run GPU", 500)
		}
	}))
	t.Cleanup(server.Close)
	f.app.cfg.ComfyUIUpstream, _ = url.Parse(server.URL)
	receipt, q, err := f.app.createPostprocessQuote(context.Background(), f.user.ID, postprocessQuoteRequest{RequestID: newRequestID(), SourceJobID: parent.PublicID, SourceMediaID: mediaID, Options: postprocessOptions{Width: 256, Height: 160, Interpolate: 1, RTXQuality: "HIGH"}})
	if err != nil {
		t.Fatal(err)
	}
	f.quote = q
	// Exercise real HTTP consent/publication before the common input/dispatch
	// tests, including their lost acknowledgement and final-output scenarios.
	f.app.csrfSigner = security.NewCSRFSigner("postprocess-dispatch-confirm")
	values := url.Values{"csrf": {f.app.csrfSigner.Token("pp-dispatch-session")}, "request_id": {receipt.RequestID}, "confirmation": {receipt.ConfirmationSHA256}}
	request := httptest.NewRequest(http.MethodPost, "/generate/postprocess/confirm", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "pp-dispatch-session"})
	request = request.WithContext(context.WithValue(request.Context(), userCtxKey, &f.user))
	response := httptest.NewRecorder()
	f.app.handlePostprocessConfirm(response, request)
	confirmed := postprocessHTTPResult(t, response, http.StatusOK)
	accepted, err := f.app.store.GenerationPostprocessByRequest(context.Background(), f.user.ID, receipt.RequestID)
	if err != nil || accepted.JobID == nil || confirmed["job_id"] != accepted.JobPublicID || confirmed["publication_pending"] != false || f.uploads.Load() != 0 || f.posts.Load() != 0 {
		t.Fatal("HTTP confirmation did not publish without upstream execution", accepted, confirmed, err)
	}
	f.job, err = f.app.store.ClaimNextGenerationDispatch(context.Background(), newRequestID(), 2)
	if err != nil || f.job.ID != *accepted.JobID {
		t.Fatal("child not claimed", err)
	}
	return f
}

func (f *postprocessInputFixture) assertClean(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(f.app.cfg.MediaSpoolDir)
	if err != nil || len(entries) != 0 || len(f.app.postprocessDecodeSlots) != 0 {
		t.Fatal("plaintext or decode slot leaked", entries, err)
	}
	if f.posts.Load() != 0 {
		t.Fatal("preparation executed a prompt or GPU action")
	}
}

func TestPostprocessInputsVerifiedReuseIntegration(t *testing.T) {
	f := newPostprocessInputFixture(t)
	ctx := context.Background()
	p, err := f.app.preparePostprocessDispatch(ctx, f.job)
	if err != nil {
		t.Fatal(err)
	}
	f.assertClean(t)
	if len(p.Inputs) != 1 || p.Inputs[0].SHA256 != f.quote.Sources[0].Identity.SHA256 || p.Inputs[0].ReadAt.IsZero() || p.Inputs[0].InputName == f.quote.Sources[0].InputName {
		t.Fatalf("runtime copy is not independently bound: %+v", p.Inputs)
	}
	node := p.Prompt["1"].(map[string]any)["inputs"].(map[string]any)
	if node["image"] != p.Inputs[0].InputName {
		t.Fatal("graph still points at logical quote name")
	}
	if f.uploads.Load() != 1 || f.views.Load() != 1 {
		t.Fatal("upload was not read back")
	}
	stored, err := f.app.store.GenerationPostprocessInputForJob(ctx, f.user.ID, f.job.ID, 0)
	if err != nil || stored.State != "stored" || stored.CleanupBlocked {
		t.Fatal("verified copy not durable", err)
	}
	restarted := &App{store: f.app.store, contentCipher: f.app.contentCipher, cfg: f.app.cfg}
	p2, err := restarted.preparePostprocessDispatch(ctx, f.job)
	if err != nil || len(p2.Inputs) != 1 || p2.Inputs[0].InputName != p.Inputs[0].InputName || f.uploads.Load() != 1 || f.views.Load() != 2 {
		t.Fatal("restart rewrote an immutable copy", err)
	}
	if _, err := f.db.Exec(`UPDATE comfy_input_assets SET expires_at=now()-interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	expired, err := f.app.store.ExpiredComfyInputAssets(ctx, 100)
	if err != nil || len(expired) != 0 {
		t.Fatal("active job input eligible for cleanup", expired, err)
	}
	if _, err := f.db.Exec(`UPDATE generation_jobs SET state='failed',submission_started_at=now(),finished_at=now() WHERE id=$1`, f.job.ID); err != nil {
		t.Fatal(err)
	}
	expired, err = f.app.store.ExpiredComfyInputAssets(ctx, 100)
	if err != nil || len(expired) != 0 {
		t.Fatal("unknown submission input eligible for cleanup", expired, err)
	}
	if _, err := f.db.Exec(`UPDATE generation_jobs SET state='completed',submission_started_at=NULL,finished_at=now() WHERE id=$1`, f.job.ID); err != nil {
		t.Fatal(err)
	}
	expired, err = f.app.store.ExpiredComfyInputAssets(ctx, 100)
	if err != nil || len(expired) != 1 || expired[0].ID != stored.ID {
		t.Fatal("completed input cannot be cleaned", expired, err)
	}
	if _, err := f.app.preparePostprocessDispatch(ctx, f.job); !errors.Is(err, store.ErrGenerationJobStateConflict) {
		t.Fatal("terminal job uploaded again", err)
	}
}

func TestPostprocessInputsLostResponseIntegration(t *testing.T) {
	for _, beforeWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "bytes present", true: "bytes missing"}[beforeWrite], func(t *testing.T) {
			f := newPostprocessInputFixture(t)
			ctx := context.Background()
			mode := "lost-response"
			if beforeWrite {
				mode = "lost-before-write"
			}
			f.mode.Store(mode)
			if _, err := f.app.preparePostprocessDispatch(ctx, f.job); !errors.Is(err, errPostprocessDispatchWaiting) {
				t.Fatal("unknown upload not retained for recovery", err)
			}
			f.assertClean(t)
			input, err := f.app.store.GenerationPostprocessInputForJob(ctx, f.user.ID, f.job.ID, 0)
			if err != nil || input.State != "reserved" || !input.CleanupBlocked {
				t.Fatal("uncertain bytes escaped accounting", err)
			}
			f.mode.Store("ok")
			if beforeWrite {
				if _, err := f.app.preparePostprocessDispatch(ctx, f.job); !errors.Is(err, errPostprocessDispatchWaiting) || f.uploads.Load() != 1 {
					t.Fatal("same attempt uploaded twice", err)
				}
				if _, err := f.db.Exec(`UPDATE generation_jobs SET dispatch_until=now()-interval '1 minute' WHERE id=$1`, f.job.ID); err != nil {
					t.Fatal(err)
				}
				f.job, err = f.app.store.ClaimNextGenerationDispatch(ctx, newRequestID(), 2)
				if err != nil {
					t.Fatal(err)
				}
			}
			p, err := f.app.preparePostprocessDispatch(ctx, f.job)
			if err != nil || len(p.Inputs) != 1 {
				t.Fatal("upload receipt recovery failed", err)
			}
			want := int32(1)
			if beforeWrite {
				want = 2
			}
			if f.uploads.Load() != want {
				t.Fatal("wrong number of uploads", f.uploads.Load())
			}
			if beforeWrite {
				f.filesMu.Lock()
				same := len(f.names) != 2 || f.names[0] == f.names[1]
				f.filesMu.Unlock()
				if same {
					t.Fatal("reclaimed attempt overwrote the previous path")
				}
				if _, err := f.db.Exec(`UPDATE generation_jobs SET state='failed',finished_at=now() WHERE id=$1`, f.job.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Exec(`UPDATE comfy_input_assets SET expires_at=now()-interval '1 day'`); err != nil {
					t.Fatal(err)
				}
				items, err := f.app.store.ExpiredComfyInputAssets(ctx, 100)
				if err != nil || len(items) != 1 || items[0].ID == input.ID {
					t.Fatal("uncertain upload became cleanup-safe after failure", items, err)
				}
				if _, err := f.db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, f.job.ID); err != nil {
					t.Fatal(err)
				}
				items, err = f.app.store.ExpiredComfyInputAssets(ctx, 100)
				if err != nil || len(items) != 1 || items[0].ID == input.ID {
					t.Fatal("uncertain upload hold lost after job deletion", items, err)
				}
			}
			f.assertClean(t)
		})
	}
}

func TestPostprocessInputsFailureGatesIntegration(t *testing.T) {
	for _, mode := range []string{"schema-offline", "mutated-readback", "foreign-response", "revoke", "cancel", "reject-upload"} {
		t.Run(mode, func(t *testing.T) {
			f := newPostprocessInputFixture(t)
			f.mode.Store(mode)
			if _, err := f.app.preparePostprocessDispatch(context.Background(), f.job); err == nil {
				t.Fatal("unsafe preparation accepted")
			} else if mode == "reject-upload" && (!errors.Is(err, errPostprocessUploadRejected) || errors.Is(err, errPostprocessDispatchWaiting)) {
				t.Fatal("permanent upload rejection treated as automatic retry", err)
			}
			f.assertClean(t)
			if mode == "schema-offline" {
				if f.uploads.Load() != 0 {
					t.Fatal("uploaded before live schema check")
				}
				return
			}
			input, err := f.app.store.GenerationPostprocessInputForJob(context.Background(), f.user.ID, f.job.ID, 0)
			if err != nil || !input.CleanupBlocked || input.State != "reserved" {
				t.Fatal("unverified upload declared stored", err)
			}
		})
	}
}

func TestPostprocessInputsDiskQuotaBeforeUploadIntegration(t *testing.T) {
	f := newPostprocessInputFixture(t)
	if _, err := f.db.Exec(`INSERT INTO comfy_input_assets(id,user_id,filename,subfolder,size_bytes,content_hash,state)
	 SELECT repeat('q',32)||i::text,$1,'quota-'||i::text||'.mp4','quota-fixture',536870912,repeat('a',64),'stored' FROM generate_series(1,4) i`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.preparePostprocessDispatch(context.Background(), f.job); !errors.Is(err, store.ErrComfyInputUserQuota) {
		t.Fatal("disk quota bypassed", err)
	}
	if f.uploads.Load() != 0 {
		t.Fatal("disk quota checked after upload")
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM generation_postprocess_inputs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed quota left partial ownership", count, err)
	}
	f.assertClean(t)
}

func TestPostprocessInputsSensitivityObservationIsRetainedIntegration(t *testing.T) {
	f := newPostprocessInputFixture(t)
	ctx := context.Background()
	if _, err := f.db.Exec(`UPDATE content_events SET is_sensitive=true WHERE generation_job_id=$1`, f.quote.Sources[0].Identity.JobID); err != nil {
		t.Fatal(err)
	}
	r, err := f.app.store.CheckGenerationPostprocessDispatch(ctx, f.user.ID, f.job.ID, f.job.DispatchToken)
	if err != nil || !r.Sensitive {
		t.Fatal("current sensitivity not observed", err)
	}
	if _, err := f.db.Exec(`UPDATE content_events SET is_sensitive=false WHERE generation_job_id=$1`, f.quote.Sources[0].Identity.JobID); err != nil {
		t.Fatal(err)
	}
	p, err := f.app.preparePostprocessDispatch(ctx, f.job)
	if err != nil || !p.Sensitive {
		t.Fatal("observed source sensitivity lost", err)
	}
	f.assertClean(t)
}

func TestPostprocessInputsRejectStaleJobSnapshotIntegration(t *testing.T) {
	for _, query := range []string{
		`UPDATE generation_jobs SET payload_cipher='replaced' WHERE id=$1`,
		`UPDATE generation_jobs SET template_id='postprocess-video-v1' WHERE id=$1`,
		`UPDATE generation_jobs SET dispatch_until=now()-interval '1 second' WHERE id=$1`,
	} {
		t.Run(query, func(t *testing.T) {
			f := newPostprocessInputFixture(t)
			if _, err := f.db.Exec(query, f.job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.app.preparePostprocessDispatch(context.Background(), f.job); !errors.Is(err, store.ErrGenerationJobStateConflict) {
				t.Fatal("stale job authorized preparation", err)
			}
			if f.uploads.Load() != 0 || f.views.Load() != 0 {
				t.Fatal("stale claim accessed remote files")
			}
			f.assertClean(t)
		})
	}
}
