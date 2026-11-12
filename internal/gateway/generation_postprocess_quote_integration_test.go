package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

const postprocessImageObjectInfoFixture = `{
 "LoadImage":{"input":{"required":{"image":[["existing.png"],{"default":"existing.png"}]}},"output":["IMAGE","MASK"]},
 "SaveImage":{"input":{"required":{"images":["IMAGE"],"filename_prefix":["STRING"]}},"output":[]},
 "LCColorMatch":{"input":{"required":{"image":["IMAGE"],"reference":["IMAGE"],"method":[["adain","mean_std"]],"strength":["FLOAT"],"skin_protect":["FLOAT"]}},"output":["IMAGE"]},
 "RTXVideoSuperResolution":{"input":{"required":{"images":["IMAGE"],"quality":[["LOW","MEDIUM","HIGH","ULTRA"]],"resize_type":["COMFY_DYNAMICCOMBO_V3",{"options":[{"key":"target dimensions","inputs":{"required":{"width":["INT"],"height":["INT"]}}}]}]}},"output":["IMAGE"]}
}`

func postprocessRetainedImage(t *testing.T, f *dispatchFixture, job domain.GenerationJob, sensitive bool) int64 {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 128, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 128; x++ {
			img.Set(x, y, color.RGBA{R: byte(x), G: byte(y), B: 50, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	cipher, err := f.app.contentCipher.EncryptBytes(data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var eventID, mediaID int64
	if err := f.db.QueryRow(`INSERT INTO content_events(user_id,generation_job_id,service,kind,prompt_cipher,response_cipher,metadata_cipher,is_sensitive) VALUES($1,$2,'comfyui','comfyui_prompt','x','x','x',$3) RETURNING id`, f.user.ID, job.ID, sensitive).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,payload_cipher,size_bytes,content_hash) VALUES($1,'image','image/png','source.png',$2,$3,$4) RETURNING id`, eventID, cipher, data.Len(), generationEvidenceDigest(data.Bytes())).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE generation_jobs SET state='completed',finished_at=now() WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	return mediaID
}

func TestPostprocessQuoteProducerIntegration(t *testing.T) {
	if !json.Valid([]byte(postprocessImageObjectInfoFixture)) {
		t.Fatal("invalid object_info fixture")
	}
	f := newDispatchFixture(t)
	ctx := context.Background()
	job := f.enqueue(t)
	mediaID := postprocessRetainedImage(t, f, job, false)
	referenceJob := f.enqueue(t)
	referenceID := postprocessRetainedImage(t, f, referenceJob, true)
	var requests, writes atomic.Int32
	var offline atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/object_info" {
			writes.Add(1)
			http.Error(w, "no execution allowed", 500)
			return
		}
		if offline.Load() {
			http.Error(w, "offline", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(postprocessImageObjectInfoFixture))
	}))
	defer server.Close()
	f.app.cfg.ComfyUIUpstream, _ = url.Parse(server.URL)
	request := postprocessQuoteRequest{RequestID: newRequestID(), SourceJobID: job.PublicID, SourceMediaID: mediaID,
		ReferenceJobID: referenceJob.PublicID, ReferenceMediaID: referenceID, Options: postprocessOptions{Width: 256, Height: 160, Interpolate: 1, RTXQuality: "HIGH", ColorMethod: "adain", ColorStrength: 0.7}}
	assertClean := func() {
		t.Helper()
		entries, err := os.ReadDir(f.app.cfg.MediaSpoolDir)
		if err != nil || len(entries) != 0 || len(f.app.postprocessDecodeSlots) != 0 {
			t.Fatal("source spool or slot leaked", entries, err)
		}
		if writes.Load() != 0 || f.posts.Load() != 0 {
			t.Fatal("quote uploaded or executed work")
		}
	}
	receipt, q, err := f.app.createPostprocessQuote(ctx, f.user.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	assertClean()
	if receipt.State != "quoted" || !receipt.Sensitive || receipt.JobID != nil || len(receipt.Sources) != 2 || q.Sources[0].Measurement.Width != 128 || q.Sources[0].Measurement.Frames != 1 || q.Plan.Options.Width != 256 || q.Components != nil || q.FilesSHA256 != "" {
		t.Fatalf("invalid measured quote: %+v %+v", receipt, q)
	}
	stored, err := f.app.store.GenerationPostprocessByRequest(ctx, f.user.ID, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := f.app.decodePostprocessQuote(stored); err != nil || decoded.GraphSHA256 != q.GraphSHA256 {
		t.Fatal("stored quote cannot be recovered", err)
	}
	var jobs int
	if err := f.db.QueryRow(`SELECT count(*) FROM generation_jobs WHERE user_id=$1`, f.user.ID).Scan(&jobs); err != nil || jobs != 2 {
		t.Fatal("quote created a child before confirmation", jobs, err)
	}
	count := requests.Load()
	offline.Store(true)
	// A new App reconstructs the same plan from persistence without a live
	// decoder/catalog; retry cannot extend expiry or silently re-plan.
	restarted := &App{store: f.app.store, contentCipher: f.app.contentCipher, cfg: f.app.cfg}
	retry, old, err := restarted.createPostprocessQuote(ctx, f.user.ID, request)
	if err != nil || retry.ConfirmationSHA256 != receipt.ConfirmationSHA256 || !bytes.Equal(retry.PayloadCipher, receipt.PayloadCipher) || !old.CreatedAt.Equal(q.CreatedAt) || requests.Load() != count {
		t.Fatal("restart retry changed quote", err)
	}
	changed := request
	changed.Options.RTXQuality = "LOW"
	if _, _, err := f.app.createPostprocessQuote(ctx, f.user.ID, changed); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal("same ID changed intent", err)
	}
	newRequest := request
	newRequest.RequestID = newRequestID()
	if _, _, err := f.app.createPostprocessQuote(ctx, f.user.ID, newRequest); !errors.Is(err, errPostprocessComponentsUnavailable) {
		t.Fatal("last-known-good catalog accepted as live", err)
	}
	assertClean()
	offline.Store(false)
	if _, err := f.db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.app.createPostprocessQuote(ctx, f.user.ID, request); !errors.Is(err, store.ErrQuickGenerationForbidden) {
		t.Fatal("disabled owner replayed quote", err)
	}
	if _, err := f.db.Exec(`UPDATE users SET disabled=false WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	accepted, created, err := f.app.store.ConfirmGenerationPostprocess(ctx, f.user.ID, request.RequestID, receipt.ConfirmationSHA256, newRequestID())
	if err != nil || !created || accepted.JobID == nil {
		t.Fatal("producer quote cannot create child", err)
	}
	child, err := f.app.store.GenerationJobByPublicID(ctx, f.user.ID, accepted.JobPublicID)
	if err != nil || child.TemplateID != domain.PostprocessImageTemplate || child.WorkflowID != domain.PostprocessWorkflow || !bytes.Equal(child.PayloadCipher, receipt.PayloadCipher) {
		t.Fatal("child does not retain own operation and exact envelope", err)
	}
	var queued bool
	if err := f.db.QueryRow(`SELECT dispatch_queued_at IS NOT NULL FROM generation_jobs WHERE id=$1`, child.ID).Scan(&queued); err != nil || queued {
		t.Fatal("unimplemented dispatch lane was enqueued", err)
	}
	if _, err := f.db.Exec(`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, mediaID); err != nil {
		t.Fatal(err)
	}
	newRequest.RequestID = newRequestID()
	if _, _, err := f.app.createPostprocessQuote(ctx, f.user.ID, newRequest); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("hidden source quoted again", err)
	}
	assertClean()
}

func TestPostprocessQuoteLatePreparationFencedIntegration(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "rights revoked", true: "discarded"}[cancel], func(t *testing.T) {
			f := newDispatchFixture(t)
			ctx := context.Background()
			job := f.enqueue(t)
			mediaID := postprocessRetainedImage(t, f, job, false)
			request := postprocessQuoteRequest{RequestID: newRequestID(), SourceJobID: job.PublicID, SourceMediaID: mediaID, Options: postprocessOptions{Width: 256, Height: 160, Interpolate: 1, RTXQuality: "HIGH"}}
			hookErrors := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var hookErr error
				entries, spoolErr := os.ReadDir(f.app.cfg.MediaSpoolDir)
				if spoolErr != nil || len(entries) == 0 {
					t.Error("fixture did not retain decoded source during catalog inspection", spoolErr)
				}
				if release, ok := f.app.acquirePostprocessQuote(); ok {
					release()
					t.Error("second preparation admitted while source spool retained")
				}
				if cancel {
					_, hookErr = f.app.store.DiscardGenerationPostprocess(ctx, f.user.ID, request.RequestID)
				} else {
					_, hookErr = f.db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, f.user.ID)
				}
				hookErrors <- hookErr
				_, _ = w.Write([]byte(postprocessImageObjectInfoFixture))
			}))
			defer server.Close()
			f.app.cfg.ComfyUIUpstream, _ = url.Parse(server.URL)
			_, _, err := f.app.createPostprocessQuote(ctx, f.user.ID, request)
			select {
			case hookErr := <-hookErrors:
				if hookErr != nil {
					t.Fatal(hookErr)
				}
			default:
				t.Fatal("preparation did not reach the live schema gate", err)
			}
			want := store.ErrQuickGenerationForbidden
			if cancel {
				want = store.ErrPostprocessConflict
			}
			if !errors.Is(err, want) {
				t.Fatal("late preparation resurrected cancelled or revoked request", err)
			}
			entries, _ := os.ReadDir(f.app.cfg.MediaSpoolDir)
			if len(entries) != 0 || len(f.app.postprocessDecodeSlots) != 0 {
				t.Fatal("late rejection leaked plaintext")
			}
			receipt, err := f.app.store.GenerationPostprocessByRequest(ctx, f.user.ID, request.RequestID)
			if cancel {
				if err != nil || receipt.State != "cancelled" {
					t.Fatal("discard fence lost", err)
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("revoked quote persisted", err)
			}
		})
	}
}
