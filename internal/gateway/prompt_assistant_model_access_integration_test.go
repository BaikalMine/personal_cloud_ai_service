package gateway

import (
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
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/store"
)

func assistantModelGrantFixture(t *testing.T, respond http.HandlerFunc) (*durableAssistantFixture, int64) {
	t.Helper()
	f := newDurableAssistantFixture(t, respond)
	ctx := context.Background()
	if err := f.app.store.CancelPromptAssistantJob(ctx, f.user.ID, f.job.ID); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	if _, err := f.db.Exec(`UPDATE users SET role='user',can_use_quick_generation=true,can_generate_text_to_image=true WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	var err error
	f.user, err = f.app.store.UserByID(ctx, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var adminID int64
	if err = f.db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('model-grant-admin','disabled','admin') RETURNING id`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	f.snapshot.ModelID = "gemma-31b"
	f.app.cfg.PromptAssistantModels = []domain.PromptAssistantModel{{ID: "gemma-31b", Label: "Fixture model", Model: "saved-text"}}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"service": "inference-worker", "protocol": 2, "ready": true, "assistant_model_access": 1})
	}))
	t.Cleanup(health.Close)
	f.app.cfg.InferenceWorkerURL, _ = url.Parse(health.URL)
	f.input, err = f.app.makePromptAssistantJob(newRequestID(), f.user.ID, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return f, adminID
}

func acceptGrantedAssistant(t *testing.T, f *durableAssistantFixture, admin int64) {
	t.Helper()
	ctx := context.Background()
	if err := f.app.store.SetPromptAssistantModelGrant(ctx, admin, f.user.ID, f.input.ModelID, true); err != nil {
		t.Fatal(err)
	}
	var err error
	f.job, err = f.app.store.EnqueuePromptAssistantJob(ctx, f.input)
	if err != nil || f.job.ModelID != f.input.ModelID {
		t.Fatal("choice not saved", f.job.ModelID, err)
	}
}

func TestDurableAssistantModelGrantDefaultsAndAudit(t *testing.T) {
	f, admin := assistantModelGrantFixture(t, nil)
	ctx := context.Background()
	if allowed, err := f.app.store.PromptAssistantModelAllowed(ctx, f.user.ID, "gemma-31b"); err != nil || allowed {
		t.Fatal("missing grant allowed", allowed, err)
	}
	if _, err := f.app.store.EnqueuePromptAssistantJob(ctx, f.input); !errors.Is(err, store.ErrPromptAssistantConflict) {
		t.Fatal("ungranted alternative enqueued", err)
	}
	if err := f.app.store.SetPromptAssistantModelGrant(ctx, f.user.ID, f.user.ID, "gemma-31b", true); !errors.Is(err, store.ErrPromptAssistantModelAccess) {
		t.Fatal("self grant accepted", err)
	}
	for _, state := range []string{"disabled=true", "disabled=false,account_expires_at=now()-interval '1 hour'"} {
		if _, err := f.db.Exec(`UPDATE users SET `+state+` WHERE id=$1`, admin); err != nil {
			t.Fatal(err)
		}
		if err := f.app.store.SetPromptAssistantModelGrant(ctx, admin, f.user.ID, "gemma-31b", true); !errors.Is(err, store.ErrPromptAssistantModelAccess) {
			t.Fatal("inactive admin granted model", err)
		}
	}
	if _, err := f.db.Exec(`UPDATE users SET disabled=false,account_expires_at=NULL WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.SetPromptAssistantModelGrant(ctx, admin, f.user.ID, "arbitrary:model", true); !errors.Is(err, store.ErrGPUWorkInput) {
		t.Fatal("raw model accepted as catalog ID", err)
	}
	acceptGrantedAssistant(t, f, admin)
	grants, err := f.app.store.PromptAssistantModelGrants(ctx, f.user.ID)
	if err != nil || len(grants) != 1 || grants[0] != "gemma-31b" {
		t.Fatal(grants, err)
	}
	if allowed, err := f.app.store.PromptAssistantModelAllowed(ctx, f.user.ID, "qwen-27b"); err != nil || allowed {
		t.Fatal("grant broadened to another model", allowed, err)
	}
	tampered := f.input
	tampered.ModelID = "qwen-27b"
	if _, err = f.app.store.EnqueuePromptAssistantJob(ctx, tampered); !errors.Is(err, store.ErrPromptAssistantConflict) {
		t.Fatal("saved model identity changed", err)
	}
	if err = f.app.store.SetPromptAssistantModelGrant(ctx, admin, f.user.ID, "gemma-31b", false); err != nil {
		t.Fatal(err)
	}
	var audits int
	if err = f.db.QueryRow(`SELECT count(*) FROM audit_log WHERE actor_user_id=$1 AND target_id=$2 AND action='prompt_assistant_model_access_updated' AND metadata->>'model_id'='gemma-31b'`, admin, f.user.ID).Scan(&audits); err != nil || audits != 2 {
		t.Fatal("grant/revoke audit missing or unauthorized audit committed", audits, err)
	}
	defaultSnapshot := f.snapshot
	defaultSnapshot.ModelID = ""
	defaultInput, err := f.app.makePromptAssistantJob(newRequestID(), f.user.ID, defaultSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.EnqueuePromptAssistantJob(ctx, defaultInput); err != nil {
		t.Fatal("default model requires an extra grant", err)
	}
}

func TestDurableAssistantModelGrantRevokedBeforePreparation(t *testing.T) {
	f, admin := assistantModelGrantFixture(t, nil)
	acceptGrantedAssistant(t, f, admin)
	if err := f.app.store.SetPromptAssistantModelGrant(context.Background(), admin, f.user.ID, f.input.ModelID, false); err != nil {
		t.Fatal(err)
	}
	job := f.step(t)
	if job.State != "cancelled" || job.InferenceWorkID != "" || f.calls.Load() != 0 {
		t.Fatalf("revoked source dispatched: %+v calls=%d", job, f.calls.Load())
	}
}

func TestDurableAssistantModelGrantRevokedBeforeExecutor(t *testing.T) {
	f, admin := assistantModelGrantFixture(t, nil)
	acceptGrantedAssistant(t, f, admin)
	job := f.step(t)
	if job.InferenceWorkID == "" {
		t.Fatal("fixture was not handed off")
	}
	if err := f.app.store.SetPromptAssistantModelGrant(context.Background(), admin, f.user.ID, f.input.ModelID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.worker.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	request, err := f.app.store.GPUInference(context.Background(), job.InferenceWorkID)
	if err != nil || request.State != "not_dispatched" || f.calls.Load() != 0 {
		t.Fatalf("executor ignored revocation: %+v calls=%d err=%v", request, f.calls.Load(), err)
	}
	job = f.step(t)
	if !job.Terminal() {
		t.Fatal("revoked request stuck", job.State)
	}
	if _, err = f.app.store.ActiveGPUWork(context.Background(), domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unsent request retained GPU", err)
	}
}

func TestDurableAssistantModelRevocationPreservesRunningResponse(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	f, admin := assistantModelGrantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			writeJSON(w, 200, map[string]any{"done": true, "message": map[string]string{"content": `{"prompt":"A green vase in daylight.","references":[]}`}})
		case <-r.Context().Done():
		}
	})
	acceptGrantedAssistant(t, f, admin)
	f.step(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.worker.RunOne(ctx); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("executor did not start")
	}
	if err := f.app.store.SetPromptAssistantModelGrant(ctx, admin, f.user.ID, f.input.ModelID, false); err != nil {
		t.Fatal(err)
	}
	if request, err := f.app.store.CancelInactiveAssistantInference(ctx, f.job.ID); err != nil || request.State != "running" {
		t.Fatal("running executor was falsely completed", request.State, err)
	}
	if work, err := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource); err != nil || work.ID != f.job.ID {
		t.Fatal("GPU released before executor completion", work.ID, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	job := f.step(t)
	if job.State != "completed" || f.calls.Load() != 1 {
		t.Fatal("running result lost", job.State, f.calls.Load())
	}
	f.restart()
	w := f.request(t, http.MethodGet, "/generate/prompt-assistant/jobs?id="+job.ID, nil, f.user)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "A green vase in daylight.") {
		t.Fatal("cached result lost after revocation", w.Code, w.Body.String())
	}
	if _, err := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("completed request retained GPU", err)
	}
}

func TestDurableAssistantModelRevocationFencesOfflineExecutor(t *testing.T) {
	f, admin := assistantModelGrantFixture(t, nil)
	acceptGrantedAssistant(t, f, admin)
	job := f.step(t)
	ctx := context.Background()
	if err := f.app.store.SetPromptAssistantModelGrant(ctx, admin, f.user.ID, f.input.ModelID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.refreshManagedGPUWork(ctx); err != nil {
		t.Fatal(err)
	}
	request, err := f.app.store.GPUInference(ctx, job.InferenceWorkID)
	if err != nil || request.State != "not_dispatched" || f.calls.Load() != 0 {
		t.Fatalf("offline queued request not fenced: %+v err=%v", request, err)
	}
	job = f.step(t)
	if !job.Terminal() {
		t.Fatal("offline request stuck", job.State)
	}
	if _, err = f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("offline executor retained GPU", err)
	}
}

func TestDurableAssistantModelCatalogHTTPSelection(t *testing.T) {
	f, admin := assistantModelGrantFixture(t, nil)
	ctx := context.Background()
	form := url.Values{"csrf": {f.app.csrfSigner.Token("assistant-session")}, "template_id": {"text-to-image"},
		"assistant_template": {"workflow-default"}, "prompt": {f.snapshot.Original}, "context": {f.snapshot.Context}, "request_id": {f.input.ID}}
	check := func(path, model string, want int) {
		t.Helper()
		form.Set("assistant_model", model)
		w := f.request(t, http.MethodPost, path, form, f.user)
		if w.Code != want {
			t.Fatalf("%s model=%s: %d want=%d %s", path, model, w.Code, want, w.Body.String())
		}
	}
	path := "/generate/prompt-assistant/jobs"
	check(path, "arbitrary/model:tag", 400)
	check(path, "gemma-31b", 403)
	if err := f.app.store.SetPromptAssistantModelGrant(ctx, admin, f.user.ID, "gemma-31b", true); err != nil {
		t.Fatal(err)
	}
	check("/generate/prompt-assistant", "gemma-31b", 503)
	endpoint := f.app.cfg.InferenceWorkerURL
	f.app.cfg.InferenceWorkerURL = nil
	check(path, "gemma-31b", 202)
	f.job, _ = f.app.store.PromptAssistantJob(ctx, f.user.ID, f.input.ID)
	if job := f.step(t); job.State != "queued" || job.InferenceWorkID != "" || f.calls.Load() != 0 {
		t.Fatal("offline submission was not safely queued", job.State, job.InferenceWorkID)
	}
	if _, err := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("offline submission held GPU", err)
	}
	f.app.cfg.InferenceWorkerURL = endpoint
	// The selected request must not fall back to the shared default model.
	f.app.promptAssistant = promptassistant.NewClient(f.base, "different-default")
	check(path, "gemma-31b", 202)
	check(path, "gemma-31b", 202)
	var err error
	f.job, err = f.app.store.PromptAssistantJob(ctx, f.user.ID, f.input.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.app.promptAssistantSnapshot(f.job)
	if err != nil || snapshot.ModelID != "gemma-31b" || snapshot.Request.Model != "saved-text" {
		t.Fatal("HTTP selection not saved", snapshot.ModelID, snapshot.Request.Model, err)
	}
	f.step(t)
	if _, err = f.worker.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	f.app.cfg.PromptAssistantModels = nil
	if job := f.step(t); job.State != "completed" || f.calls.Load() != 1 {
		t.Fatal("catalog removal lost dispatched result", job.State, f.calls.Load())
	}
}

func TestAssistantCatalogChoicesRespectPermissions(t *testing.T) {
	f, admin := assistantModelGrantFixture(t, nil)
	ctx := context.Background()
	f.app.cfg.PromptAssistantModels = append(f.app.cfg.PromptAssistantModels, domain.PromptAssistantModel{ID: "vision", Label: "Vision", Model: "private-text-tag", VisionModel: "private-vision-tag"})
	choices, err := f.app.assistantModelChoices(ctx, f.user.ID)
	if err != nil || len(choices) != 0 {
		t.Fatal("ungranted catalog", choices, err)
	}
	if err := f.app.store.SetPromptAssistantModelGrant(ctx, admin, f.user.ID, "vision", true); err != nil {
		t.Fatal(err)
	}
	choices, err = f.app.assistantModelChoices(ctx, f.user.ID)
	if err != nil || len(choices) != 1 || choices[0].ID != "vision" || !choices[0].Vision {
		t.Fatal("filtered catalog", choices, err)
	}
	choices, err = f.app.assistantModelChoices(ctx, admin)
	if err != nil || len(choices) != 2 {
		t.Fatal("admin catalog", choices, err)
	}
	f.app.cfg.PromptAssistantDurableJobs = false
	choices, err = f.app.assistantModelChoices(ctx, admin)
	if err != nil || len(choices) != 0 {
		t.Fatal("legacy catalog exposed alternatives", choices, err)
	}
	f.app.cfg.PromptAssistantDurableJobs = true
	if _, err := f.db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	choices, err = f.app.assistantModelChoices(ctx, f.user.ID)
	if err != nil || len(choices) != 0 {
		t.Fatal("disabled account choices", choices, err)
	}
}

func TestDurableAssistantModelCatalogChangeCancelsUnsent(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "removed"}[removed], func(t *testing.T) {
			f, admin := assistantModelGrantFixture(t, nil)
			acceptGrantedAssistant(t, f, admin)
			if removed {
				f.app.cfg.PromptAssistantModels = nil
			} else {
				f.app.cfg.PromptAssistantModels[0].Model = "replacement-model"
			}
			if job := f.step(t); job.State != "cancelled" || f.calls.Load() != 0 || job.InferenceWorkID != "" {
				t.Fatal("changed catalog executed saved request", job.State, f.calls.Load())
			}
		})
	}
}

func TestDurableAssistantModelWaitsForCompatibleExecutor(t *testing.T) {
	f, admin := assistantModelGrantFixture(t, nil)
	acceptGrantedAssistant(t, f, admin)
	compatible := f.app.cfg.InferenceWorkerURL
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"service": "inference-worker", "protocol": 2, "ready": true})
	}))
	defer legacy.Close()
	f.app.cfg.InferenceWorkerURL, _ = url.Parse(legacy.URL)
	job := f.step(t)
	if job.State != "queued" || job.InferenceWorkID != "" || f.calls.Load() != 0 {
		t.Fatal("legacy worker accepted named model", job.State, job.InferenceWorkID, f.calls.Load())
	}
	work, err := f.app.store.GPUWork(context.Background(), job.ID)
	if err != nil || work.State != domain.GPUWorkWaiting {
		t.Fatal("compatibility wait reserved GPU", work.State, err)
	}
	f.app.cfg.InferenceWorkerURL = compatible
	job = f.step(t)
	if job.InferenceWorkID != job.ID {
		t.Fatal("compatible worker did not receive saved request")
	}
	if _, err = f.worker.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if job = f.step(t); job.State != "completed" || f.calls.Load() != 1 {
		t.Fatal("recovered executor did not complete exactly once", job.State, f.calls.Load())
	}
}

func TestDurableAssistantModelOfflineFencesUnsentPreparation(t *testing.T) {
	for _, phase := range []string{"reserved", "resources_ready", "preparing_resources"} {
		t.Run(phase, func(t *testing.T) {
			f, admin := assistantModelGrantFixture(t, nil)
			acceptGrantedAssistant(t, f, admin)
			ctx := context.Background()
			source, err := f.app.store.ClaimPromptAssistantJob(ctx, "previous-source")
			if err != nil {
				t.Fatal(err)
			}
			work, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, domain.PromptAssistantGPUKey(source.ID), &f.user, source.RunToken, source.CreatedAt)
			if err != nil {
				t.Fatal(err)
			}
			if phase != "reserved" {
				if err := f.app.store.SetGPUInferencePreparation(ctx, work.ID, work.LeaseToken, false); err != nil {
					t.Fatal(err)
				}
				lease := domain.QuickGenerationMiningLease{ID: "offline-prepare", UserID: f.user.ID,
					MinerID: inferenceRecoveryMiner(t, f.dispatchFixture), GPUWorkID: work.ID,
					ScriptPath: "fixture.cmd", ProcessName: "fixture.exe", ResumeMining: true}
				if err := f.app.store.CreateGPUInferenceMiningLease(ctx, lease, work.LeaseToken); err != nil {
					t.Fatal(err)
				}
				if phase == "resources_ready" {
					if err := f.app.store.SetGPUInferencePreparation(ctx, work.ID, work.LeaseToken, true); err != nil {
						t.Fatal(err)
					}
				}
			}
			f.app.cfg.InferenceWorkerURL = nil
			if job := f.step(t); job.State != "queued" || job.InferenceWorkID != "" || f.calls.Load() != 0 {
				t.Fatal("offline dispatch", job.State)
			}
			_, activeErr := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
			if phase == "preparing_resources" {
				if activeErr != nil {
					t.Fatal("unconfirmed preparation released", activeErr)
				}
			} else if !errors.Is(activeErr, sql.ErrNoRows) {
				t.Fatal("unsent preparation retained GPU", activeErr)
			}
			if phase != "reserved" {
				leases, err := f.app.store.ListQuickGenerationMiningLeases(ctx)
				if err != nil || len(leases) != 1 || leases[0].ResumeReady != (phase == "resources_ready") {
					t.Fatal("mining resume evidence", leases, err)
				}
			}
		})
	}
}

func TestDurableAssistantDisabledQueueRejectsWithoutPendingJob(t *testing.T) {
	f, admin := assistantModelGrantFixture(t, nil)
	if err := f.app.store.SetPromptAssistantModelGrant(context.Background(), admin, f.user.ID, "gemma-31b", true); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf": {f.app.csrfSigner.Token("assistant-session")}, "template_id": {"text-to-image"},
		"assistant_template": {"workflow-default"}, "assistant_model": {"gemma-31b"}, "prompt": {f.snapshot.Original}, "request_id": {f.input.ID}}
	f.app.cfg.PromptAssistantDurableJobs = false
	if w := f.request(t, http.MethodPost, "/generate/prompt-assistant/jobs", form, f.user); w.Code != 409 {
		t.Fatal("disabled queue", w.Code)
	}
	f.app.cfg.PromptAssistantDurableJobs = true
	f.app.promptAssistant = nil
	if w := f.request(t, http.MethodPost, "/generate/prompt-assistant/jobs", form, f.user); w.Code != 409 {
		t.Fatal("unconfigured model", w.Code)
	}
	if _, err := f.app.store.PromptAssistantJob(context.Background(), f.user.ID, f.input.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("rejected job persisted", err)
	}
}
