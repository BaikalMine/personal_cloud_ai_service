package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/store"
)

func TestGPUCaptionWaitingDoesNotExhaustRetries(t *testing.T) {
	f := gpuDispatchFixture(t)
	ctx := context.Background()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Error("caption called model while GPU was busy")
		}
		writeJSON(w, 200, map[string]any{"models": []any{}})
	}))
	defer model.Close()
	base, _ := url.Parse(model.URL)
	f.app.promptAssistant = promptassistant.NewClient(base, "fixture").WithVisionModel("fixture", time.Minute, "0")
	f.app.promptAssistantSlots = make(chan struct{}, 1)
	other, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkTraining, "training-fixture", &f.user, "training", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	asset, err := f.app.persistLoraDatasetImage(ctx, f.user.ID, "photo.png", bytes.NewReader(datasetTestPNG(t, 768, 768)))
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.app.makeLoraCaptionJob("", domain.LoraDatasetImage{ID: "photo", AssetID: asset.ID, CaptionRevision: "first"}, asset, "subject", "character")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := f.app.store.EnqueueLoraCaptions(ctx, f.user.ID, "", 0, []domain.LoraCaptionJob{job})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("enqueue: %+v %v", jobs, err)
	}
	for range domain.LoraCaptionMaxAttempts + 1 {
		run, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		_, err := f.app.refreshLoraCaptionJobs(run)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		waiting, err := f.app.store.LoraCaptionJob(ctx, f.user.ID, job.ID)
		if err != nil || waiting.State != "queued" || waiting.Attempts != 0 {
			t.Fatalf("waiting spent retries: %+v %v", waiting, err)
		}
		if _, err = f.db.Exec(`UPDATE lora_caption_jobs SET available_at=now() WHERE id=$1`, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	active, err := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
	if err != nil || active.ID != other.ID {
		t.Fatalf("waiting released training: %+v %v", active, err)
	}
}

func TestGPUInferenceCompletionReconcilesAfterRestart(t *testing.T) {
	f := gpuDispatchFixture(t)
	ctx := context.Background()
	work, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, "request", &f.user, "token", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.app.gpuCoordinator.dispatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.RecordGPUInferenceCompletion(ctx, work.Kind, work.JobKey, "executor_terminal"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE gpu_work SET state='uncertain',lease_until=now()-interval '1 minute' WHERE id=$1`, work.ID); err != nil {
		t.Fatal(err)
	}
	restarted := &App{store: store.New(f.db)}
	restarted.gpuCoordinator = &gpuCoordinator{app: restarted}
	if n, err := restarted.refreshManagedGPUWork(ctx); err != nil || n != 1 {
		t.Fatalf("reconcile=%d %v", n, err)
	}
	if _, err = f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("completed request still owns GPU", err)
	}
}

func gpuDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	f := newDispatchFixture(t)
	if _, err := f.db.Exec(`TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	f.app.gpuCoordinator = &gpuCoordinator{app: f.app}
	return f
}

func TestGPUCoordinatorGenerationDispatch(t *testing.T) {
	for _, kind := range []domain.GPUWorkKind{domain.GPUWorkCaption, domain.GPUWorkAssistant, domain.GPUWorkTraining} {
		t.Run(string(kind), func(t *testing.T) {
			f := gpuDispatchFixture(t)
			ctx := context.Background()
			g := f.app.gpuCoordinator
			other, err := g.admit(ctx, kind, "fixture-other", &f.user, "other-token", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err = g.dispatch(ctx, other); err != nil {
				t.Fatal(err)
			}
			job := f.enqueue(t)
			if _, err = f.app.dispatchGenerationJobs(ctx); err != nil {
				t.Fatal(err)
			}
			waiting, err := f.app.store.GenerationJobByID(ctx, job.ID)
			if err != nil || f.posts.Load() != 0 || waiting.SubmissionStartedAt != nil || waiting.QuotaReservedOn == nil || waiting.State != domain.GenerationJobWaitingForResources {
				t.Fatalf("queued generation was sent or lost quota: %+v posts=%d err=%v", waiting, f.posts.Load(), err)
			}
			// The fixture executor explicitly completes its work, not just its HTTP request.
			if err = g.release(ctx, kind, other.JobKey, "executor_terminal"); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(`UPDATE generation_jobs SET dispatch_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.app.dispatchGenerationJobs(ctx); err != nil {
				t.Fatal(err)
			}
			work, err := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
			if err != nil || work.Kind != domain.GPUWorkGeneration || work.JobKey != job.PublicID || f.posts.Load() != 1 {
				t.Fatalf("generation did not own GPU: %+v posts=%d err=%v", work, f.posts.Load(), err)
			}
			g.finishInference(domain.GPUWorkAssistant, "late-unknown", promptassistant.ExecutionUnconfirmed)
			if _, err = f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource); err != nil {
				t.Fatal("unknown outcome freed GPU", err)
			}
		})
	}
}

func TestGPUCoordinatorExecutorProbes(t *testing.T) {
	for _, tc := range []struct {
		name, body      string
		status          int
		ollama, allowed bool
	}{
		{"agent idle", `{"protocol_version":1,"state":"idle"}`, 200, false, true},
		{"agent upload", `{"protocol_version":1,"state":"busy","pending_submissions":1}`, 200, false, false},
		{"agent uncertain", `{"protocol_version":1,"state":"uncertain","unconfirmed_jobs":1}`, 200, false, false},
		{"agent old", `{}`, 404, false, false},
		{"agent offline", `{}`, 503, false, false},
		{"ollama empty", `{"models":[]}`, 200, true, true},
		{"ollama loaded", `{"models":[{"name":"model"}]}`, 200, true, false},
		{"vision only loaded", `{"models":[{"name":"vision"}]}`, 200, true, false},
		{"ollama missing inventory", `{}`, 200, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := gpuDispatchFixture(t)
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("probe attempted a mutation")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer host.Close()
			endpoint, _ := url.Parse(host.URL)
			if tc.ollama {
				f.app.promptAssistant = promptassistant.NewClient(endpoint, "fixture")
				if tc.name == "vision only loaded" {
					f.app.promptAssistant = promptassistant.NewClient(endpoint, "").WithVisionModel("vision", time.Minute, "0")
				}
			} else {
				f.app.loraTraining = loratraining.NewClient(endpoint, strings.Repeat("t", 32))
			}
			f.enqueue(t)
			if _, err := f.app.dispatchGenerationJobs(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := f.posts.Load() == 1; got != tc.allowed {
				t.Fatalf("allowed=%v posts=%d", tc.allowed, f.posts.Load())
			}
		})
	}
	t.Run("external Comfy work", func(t *testing.T) {
		f := gpuDispatchFixture(t)
		f.accepted.Store(true)
		f.enqueue(t)
		if _, err := f.app.dispatchGenerationJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.posts.Load() != 0 {
			t.Fatal("external Comfy work ignored")
		}
	})
	t.Run("unconfirmed legacy send", func(t *testing.T) {
		f := gpuDispatchFixture(t)
		ctx := context.Background()
		job := f.enqueue(t)
		if _, err := f.db.Exec(`UPDATE generation_jobs SET submission_started_at=now() WHERE id=$1`, job.ID); err != nil {
			t.Fatal(err)
		}
		_, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, "new-assistant", &f.user, "new", time.Now())
		var wait *gpuAdmissionWait
		if !errors.As(err, &wait) {
			t.Fatalf("legacy send did not block: %v", err)
		}
	})
}

func TestGPUGenerationClaimRecovery(t *testing.T) {
	f := gpuDispatchFixture(t)
	ctx := context.Background()
	job := f.enqueue(t)
	if _, err := f.app.store.ClaimNextGenerationDispatch(ctx, "old", 1); err != nil {
		t.Fatal(err)
	}
	work, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkGeneration, job.PublicID, &f.user, "old", job.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.app.gpuCoordinator.dispatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE generation_jobs SET dispatch_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE gpu_work SET lease_until=now()-interval '1 second',ready_until=now()-interval '1 second' WHERE id=$1`, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.ClaimNextGenerationDispatch(ctx, "new", 1); err != nil {
		t.Fatal(err)
	}
	newWork, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkGeneration, job.PublicID, &f.user, "new", job.CreatedAt)
	if err != nil || newWork.LeaseToken != "new" {
		t.Fatalf("recovery: %+v %v", newWork, err)
	}
	if err = f.app.store.BeginGenerationJobSubmission(ctx, job.ID, "old"); !errors.Is(err, store.ErrGenerationJobStateConflict) {
		t.Fatalf("stale source sender: %v", err)
	}
	if err = f.app.gpuCoordinator.dispatch(ctx, work); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("stale GPU sender: %v", err)
	}
	if err = f.app.gpuCoordinator.release(ctx, domain.GPUWorkGeneration, job.PublicID, "dispatch_fenced"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("unfenced release: %v", err)
	}
	if err = f.app.gpuCoordinator.dispatch(ctx, newWork); err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.BeginGenerationJobSubmission(ctx, job.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.RequeueUnsubmittedGPUWork(ctx, work.ID, "third"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("submitted work rebound: %v", err)
	}
}

func TestGPUTrainingClaimRecovery(t *testing.T) {
	f := gpuDispatchFixture(t)
	ctx := context.Background()
	job, err := f.app.store.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{
		PublicID: newRequestID(), UserID: f.user.ID, UsernameSnapshot: f.user.Username, RequestID: newRequestID(),
		ProfileID: "fixture", Family: "krea2", BaseModel: "fixture", Name: "Fixture", OutputName: "fixture",
		TriggerWord: "fixture", ConceptType: "character", Preset: "quick", Resolution: 512, MaxTrainSteps: 100,
		NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.0001, Seed: 42, SampleCount: 5, DatasetBytes: 15, DatasetPath: "fixture.zip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.ClaimNextLoraTrainingJob(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	work, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkTraining, job.PublicID, &f.user, "old", job.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.app.gpuCoordinator.dispatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	if n, err := f.app.store.RecoverLoraTrainingJobs(ctx); err != nil || n != 1 {
		t.Fatalf("recover=%d %v", n, err)
	}
	if _, err = f.app.store.BeginLoraTrainingSubmission(ctx, job.ID, "old"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("old marker survived recovery", err)
	}
	if _, err = f.app.store.ClaimNextLoraTrainingJob(ctx, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.RequeueUnsubmittedGPUWork(ctx, work.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if err = f.app.gpuCoordinator.release(ctx, domain.GPUWorkTraining, job.PublicID, "dispatch_fenced"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("old sender cancelled a new waiting claim: %v", err)
	}
	newWork, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkTraining, job.PublicID, &f.user, "new", job.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.RequeueLoraTrainingJob(ctx, job.ID, "old", "stale"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("old sender requeued new claim", err)
	}
	if err = f.app.gpuCoordinator.release(ctx, domain.GPUWorkTraining, job.PublicID, "dispatch_fenced"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatal("new claim released by old sender", err)
	}
	if err = f.app.gpuCoordinator.dispatch(ctx, newWork); err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.BeginLoraTrainingSubmission(ctx, job.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.RequeueUnsubmittedGPUWork(ctx, work.ID, "third"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatal("submitted training rebound", err)
	}
}

func TestGPUAdmissionAndMiningGuard(t *testing.T) {
	f := gpuDispatchFixture(t)
	ctx := context.Background()
	err := f.app.store.WithGPUAdmissionGuard(ctx, func() error {
		// A distinct connection/Store simulates a second Gateway process.
		return store.New(f.db).WithGPUAdmissionGuard(ctx, func() error { t.Fatal("concurrent admission entered resume guard"); return nil })
	})
	if !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("guard: %v", err)
	}
	if err = f.app.store.WithGPUAdmissionGuard(ctx, func() error { return nil }); err != nil {
		t.Fatal("guard leaked", err)
	}
}

func TestGPUUnconfirmedGenerationSurvivesRestart(t *testing.T) {
	f := gpuDispatchFixture(t)
	ctx := context.Background()
	f.status, f.body = 504, `{"error":"response lost"}`
	job := f.enqueue(t)
	if _, err := f.app.dispatchGenerationJobs(ctx); err == nil {
		t.Fatal("lost receipt was not reported")
	}
	work, err := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
	if err != nil || work.JobKey != job.PublicID {
		t.Fatalf("lost receipt freed GPU: %+v %v", work, err)
	}
	restarted := &App{store: store.New(f.db), cfg: f.app.cfg}
	restarted.gpuCoordinator = &gpuCoordinator{app: restarted}
	if _, err := restarted.refreshManagedGPUWork(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = restarted.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, "after-restart", &f.user, "next", time.Now())
	var wait *gpuAdmissionWait
	if !errors.As(err, &wait) || f.posts.Load() != 1 {
		t.Fatalf("restart resubmitted or admitted new work: posts=%d err=%v", f.posts.Load(), err)
	}
	retained, err := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
	if err != nil || retained.ID != work.ID {
		t.Fatalf("reservation lost: %+v %v", retained, err)
	}
}
