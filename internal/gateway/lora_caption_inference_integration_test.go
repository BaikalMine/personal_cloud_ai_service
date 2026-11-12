package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/inference"
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/store"
)

type durableCaptionFixture struct {
	*dispatchFixture
	job    domain.LoraCaptionJob
	worker *inference.Worker
	base   *url.URL
	calls  atomic.Int32
}

func newDurableCaptionFixture(t *testing.T, respond http.HandlerFunc) *durableCaptionFixture {
	t.Helper()
	f := &durableCaptionFixture{dispatchFixture: gpuDispatchFixture(t)}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			writeJSON(w, 200, map[string]any{"models": []any{}})
			return
		}
		f.calls.Add(1)
		var payload struct {
			Model    string                    `json:"model"`
			Messages []promptassistant.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		instruction, _ := promptassistant.LoraCaptionInstructionSnapshot()
		if payload.Model != "saved-vision" || len(payload.Messages) != 2 || payload.Messages[0].Content != instruction || len(payload.Messages[1].Images) != 1 || !strings.Contains(payload.Messages[1].Content, "subject_x") {
			t.Errorf("lost caption snapshot: %+v", payload)
		}
		if respond != nil {
			respond(w, r)
			return
		}
		writeJSON(w, 200, map[string]any{"done": true, "message": map[string]string{"content": `{"caption":"subject_x, subject_x, a green vase on a desk"}`}})
	}))
	t.Cleanup(model.Close)
	f.base, _ = url.Parse(model.URL)
	f.app.promptAssistant = promptassistant.NewClient(f.base, "text").WithVisionModel("saved-vision", time.Minute, "0")
	f.app.promptAssistantSlots = make(chan struct{}, 1)
	f.app.cfg.MediaInFlightLimitBytes = 256 << 20
	f.app.cfg.LoraCaptionDurableInference = true
	var err error
	f.worker, err = inference.NewWorker(f.app.store, f.app.contentCipher, f.base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.worker.Close)
	asset, err := f.app.persistLoraDatasetImage(context.Background(), f.user.ID, "photo.png", bytes.NewReader(datasetTestPNG(t, 768, 768)))
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.app.makeLoraCaptionJob("", domain.LoraDatasetImage{ID: "photo", AssetID: asset.ID, CaptionRevision: "original"}, asset, "subject_x", "object")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := f.app.store.EnqueueLoraCaptions(context.Background(), f.user.ID, "", 0, []domain.LoraCaptionJob{job})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("enqueue: %+v %v", jobs, err)
	}
	f.job = jobs[0]
	return f
}

func (f *durableCaptionFixture) step(t *testing.T) domain.LoraCaptionJob {
	t.Helper()
	if _, err := f.db.Exec(`UPDATE lora_caption_jobs SET available_at=now() WHERE id=$1`, f.job.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := f.app.refreshLoraCaptionJobs(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := f.app.store.LoraCaptionJob(ctx, f.user.ID, f.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func (f *durableCaptionFixture) restart() {
	previous := f.app
	f.app = &App{cfg: previous.cfg, store: store.New(f.db), contentCipher: previous.contentCipher,
		promptAssistant: promptassistant.NewClient(f.base, "changed-text").WithVisionModel("changed-vision", time.Minute, "0"), promptAssistantSlots: make(chan struct{}, 1)}
	f.app.gpuCoordinator = &gpuCoordinator{app: f.app}
	// An accepted request must recover even after turning off new submissions,
	// and must not acquire image buffers or local model slots a second time.
	f.app.cfg.LoraCaptionDurableInference = false
	f.app.cfg.MediaInFlightLimitBytes = 1
	f.app.promptAssistantSlots <- struct{}{}
}

func TestDurableCaptionRestoresPersistedResponse(t *testing.T) {
	f := newDurableCaptionFixture(t, nil)
	job := f.step(t)
	if job.InferenceWorkID == "" || job.State != "queued" || job.Attempts != 0 || f.calls.Load() != 0 {
		t.Fatalf("HTTP worker did not hand off: %+v calls=%d", job, f.calls.Load())
	}
	if len(job.InferenceMetadataCipher) == 0 || bytes.Contains(job.InferenceMetadataCipher, []byte("saved-vision")) {
		t.Fatal("missing or plaintext metadata")
	}
	for range 5 {
		waiting := f.step(t)
		if waiting.InferenceWorkID != job.InferenceWorkID || waiting.Attempts != 0 || waiting.State != "queued" {
			t.Fatalf("waiting consumed attempts or changed request: %+v", waiting)
		}
	}
	if worked, err := f.worker.RunOne(context.Background()); err != nil || !worked {
		t.Fatalf("executor: %v %v", worked, err)
	}
	if _, err := f.db.Exec(`UPDATE lora_caption_jobs SET state='running',run_token='stopped-handler',attempts=3 WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	f.restart()
	completed := f.step(t)
	if completed.State != "completed" || completed.InferenceWorkID != job.InferenceWorkID {
		t.Fatalf("restart did not restore: %+v", completed)
	}
	view, err := f.app.loraCaptionJobView(completed)
	if err != nil || view.Model != "saved-vision" || view.Caption != "subject_x, a green vase on a desk" || view.Source.Image.CaptionRevision != "original" {
		t.Fatalf("saved context lost: %+v %v", view, err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("caption repeated after restart: %d", f.calls.Load())
	}
	if accepted, err := f.app.store.FinishLoraCaptionJob(context.Background(), job.ID, "stopped-handler", "completed", "late", []byte("late"), 0); err != nil || accepted {
		t.Fatalf("stale caption handler wrote result: %v %v", accepted, err)
	}
	if _, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("completed inference retained GPU: %v", err)
	}
}

func finishOwnedFixtureInference(t *testing.T, repository *store.Store) {
	t.Helper()
	ctx := context.Background()
	owner := domain.GPUInferenceExecutor{ID: strings.Repeat("e", 64), Generation: 1, Token: strings.Repeat("f", 64)}
	if err := repository.RegisterOwnedInferenceExecutor(ctx, owner); err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimOwnedGPUInference(ctx, "stopped-fixture", owner)
	if err != nil || claimed.ID == "" {
		t.Fatalf("owned claim: %+v %v", claimed, err)
	}
	if err = repository.FinishGPUInference(ctx, claimed.ID, claimed.RunToken, "executor_stopped", 0, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDurableCaptionOwnedStoppedIsTerminal(t *testing.T) {
	f := newDurableCaptionFixture(t, nil)
	job := f.step(t)
	finishOwnedFixtureInference(t, f.app.store)
	f.restart()
	failed := f.step(t)
	if failed.State != "failed" || failed.InferenceWorkID != job.InferenceWorkID || f.calls.Load() != 0 {
		t.Fatalf("stopped caption: %+v calls=%d", failed, f.calls.Load())
	}
	if _, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stopped caption retains GPU: %v", err)
	}
	if _, err := f.app.store.RetryLoraCaption(context.Background(), f.user.ID, job.ID); err != nil {
		t.Fatalf("explicit retry rejected: %v", err)
	}
}

func TestDurableCaptionDoesNotReplayUnknownResponse(t *testing.T) {
	f := newDurableCaptionFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fixture gateway timeout", 504) })
	job := f.step(t)
	if _, err := f.worker.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.restart()
	for range 5 {
		pending := f.step(t)
		if pending.State != "queued" || pending.Attempts != 0 || pending.InferenceWorkID != job.InferenceWorkID {
			t.Fatalf("unknown response lost linkage: %+v", pending)
		}
	}
	if _, err := f.app.store.CancelLoraCaptions(context.Background(), f.user.ID, "", job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.RetryLoraCaption(context.Background(), f.user.ID, job.ID); !errors.Is(err, store.ErrLoraCaptionInferencePending) {
		t.Fatalf("unknown call retried: %v", err)
	}
	w := httptest.NewRecorder()
	writeLoraDatasetError(w, store.ErrLoraCaptionInferencePending)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "Предыдущее описание") {
		t.Fatalf("unclear retry rejection: %d %s", w.Code, w.Body.String())
	}
	if f.calls.Load() != 1 {
		t.Fatalf("model called %d times", f.calls.Load())
	}
	active, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource)
	if err != nil || active.ID != job.InferenceWorkID {
		t.Fatalf("unknown work released: %+v %v", active, err)
	}
}

func TestDurableCaptionCancellationBeforeAndDuringDispatch(t *testing.T) {
	for _, started := range []bool{false, true} {
		name := "before"
		if started {
			name = "during"
		}
		t.Run(name, func(t *testing.T) {
			entered, reply := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(reply) })
			f := newDurableCaptionFixture(t, func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-reply
				writeJSON(w, 200, map[string]any{"done": true, "message": map[string]string{"content": `{"caption":"subject_x, a vase"}`}})
			})
			job := f.step(t)
			done := make(chan error, 1)
			if started {
				go func() { _, err := f.worker.RunOne(context.Background()); done <- err }()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("executor did not start")
				}
			}
			if _, err := f.app.store.CancelLoraCaptions(context.Background(), f.user.ID, "", job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.app.store.RetryLoraCaption(context.Background(), f.user.ID, job.ID); !errors.Is(err, store.ErrLoraCaptionInferencePending) {
				t.Fatalf("unconfirmed cancellation retried: %v", err)
			}
			if started {
				once.Do(func() { close(reply) })
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.app.refreshManagedGPUWork(context.Background()); err != nil {
					t.Fatal(err)
				}
				if worked, err := f.worker.RunOne(context.Background()); err != nil || worked {
					t.Fatalf("offline executor cancellation did not fence queued work: %v %v", worked, err)
				}
			}
			cancelled := f.step(t)
			if cancelled.State != "cancelled" || len(cancelled.ResultCipher) != 0 {
				t.Fatalf("cancelled result applied: %+v", cancelled)
			}
			if _, err := f.app.refreshManagedGPUWork(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("confirmed cancellation retained GPU", err)
			}
			want := int32(0)
			if started {
				want = 1
			}
			if f.calls.Load() != want {
				t.Fatalf("cancelled dispatch calls=%d", f.calls.Load())
			}
			retry, err := f.app.store.RetryLoraCaption(context.Background(), f.user.ID, job.ID)
			if err != nil || retry.State != "queued" || retry.InferenceWorkID != "" || len(retry.InferenceMetadataCipher) != 0 {
				t.Fatalf("explicit retry not reset: %+v %v", retry, err)
			}
		})
	}
}

func TestDurableCaptionChecksRevokedTrainingPermission(t *testing.T) {
	f := newDurableCaptionFixture(t, nil)
	job := f.step(t)
	if _, err := f.db.Exec(`UPDATE users SET role='user',can_train_image_lora=false WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.worker.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	request, err := f.app.store.GPUInference(context.Background(), job.InferenceWorkID)
	if err != nil || request.State != "not_dispatched" || f.calls.Load() != 0 {
		t.Fatalf("revoked permission ignored: %+v %v", request, err)
	}
	if _, err := f.app.refreshManagedGPUWork(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDurableCaptionRecoversWithVisionDisabled(t *testing.T) {
	f := newDurableCaptionFixture(t, nil)
	job := f.step(t)
	if _, err := f.worker.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.restart()
	f.app.promptAssistant = nil
	completed := f.step(t)
	if completed.State != "completed" || completed.InferenceWorkID != job.InferenceWorkID || f.calls.Load() != 1 {
		t.Fatalf("disabled vision lost saved result: %+v", completed)
	}
	if _, err := f.app.store.RetryLoraCaption(context.Background(), f.user.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE lora_caption_jobs SET state='queued',inference_work_id=NULL,inference_metadata_cipher='' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	waiting := f.step(t)
	if waiting.State != "queued" || waiting.RunToken != "" || f.calls.Load() != 1 {
		t.Fatalf("disabled vision started a new request: %+v", waiting)
	}
}

func TestDurableCaptionBindingIsAtomic(t *testing.T) {
	f := newDurableCaptionFixture(t, nil)
	ctx := context.Background()
	var source domain.LoraCaptionJob
	if _, err := f.app.store.WithLoraCaptionWorker(ctx, func(*sql.Conn) error {
		var err error
		source, err = f.app.store.ClaimLoraCaptionJob(ctx, "source-token")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	work, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkCaption, source.ID+":"+source.RunToken, &f.user, "gpu-token", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	client := inference.NewClient(f.app.store, f.app.contentCipher)
	body := []byte(`{"model":"fixture","stream":false}`)
	if _, err = client.SubmitCaption(ctx, work, body, time.Minute, source.ID, "stale-token", []byte("metadata")); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("stale source accepted: %v", err)
	}
	_, err = f.db.Exec(`CREATE FUNCTION caption_binding_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture binding failure'; END $$;
		CREATE TRIGGER caption_binding_fail BEFORE INSERT ON gpu_inference_requests FOR EACH ROW EXECUTE FUNCTION caption_binding_fail()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.db.Exec(`DROP TRIGGER IF EXISTS caption_binding_fail ON gpu_inference_requests; DROP FUNCTION IF EXISTS caption_binding_fail()`)
	})
	if _, err = client.SubmitCaption(ctx, work, body, time.Minute, source.ID, source.RunToken, []byte("metadata")); err == nil {
		t.Fatal("expected transaction failure")
	}
	saved, err := f.app.store.LoraCaptionJob(ctx, f.user.ID, source.ID)
	if err != nil || saved.InferenceWorkID != "" || len(saved.InferenceMetadataCipher) != 0 {
		t.Fatalf("partial caption binding %+v %v", saved, err)
	}
	savedWork, err := f.app.store.GPUWork(ctx, work.ID)
	if err != nil || savedWork.Phase != "reserved" || savedWork.ExternalID != "" {
		t.Fatalf("partial GPU binding %+v %v", savedWork, err)
	}
	if _, err = f.db.Exec(`DROP TRIGGER caption_binding_fail ON gpu_inference_requests`); err != nil {
		t.Fatal(err)
	}
	if _, err = client.SubmitCaption(ctx, work, body, time.Minute, source.ID, source.RunToken, []byte("metadata")); err != nil {
		t.Fatal(err)
	}
}
