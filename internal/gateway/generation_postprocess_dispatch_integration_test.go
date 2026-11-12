package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestPostprocessCommonDispatchIntegration(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint("lost-response-", lost), func(t *testing.T) {
			f := newPostprocessInputFixture(t)
			ctx := context.Background()
			proxy := httputil.NewSingleHostReverseProxy(f.app.cfg.ComfyUIUpstream)
			var graph atomic.Value
			var promptPosts atomic.Int32
			var accepted atomic.Bool
			var outputMode atomic.Value
			outputMode.Store("running")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/view":
					if r.URL.Query().Get("type") == "output" {
						if r.URL.Query().Get("filename") != "final.png" {
							t.Error("intermediate output fetched")
						}
						f.filesMu.Lock()
						defer f.filesMu.Unlock()
						w.Header().Set("Content-Type", "image/png")
						_, _ = w.Write(f.files[f.names[0]])
						return
					}
					proxy.ServeHTTP(w, r)
				case "/prompt":
					promptPosts.Add(1)
					var document struct {
						Prompt    json.RawMessage `json:"prompt"`
						ExtraData map[string]any  `json:"extra_data"`
					}
					if err := json.NewDecoder(r.Body).Decode(&document); err != nil {
						t.Error(err)
					}
					if document.ExtraData["gateway_job_id"] != f.job.PublicID {
						t.Error("common recovery ID missing")
					}
					var marked bool
					if err := f.db.QueryRow(`SELECT submission_started_at IS NOT NULL FROM generation_jobs WHERE id=$1`, f.job.ID).Scan(&marked); err != nil || !marked {
						t.Error("prompt sent before durable marker", err)
					}
					graph.Store(document.Prompt)
					accepted.Store(true)
					if lost {
						http.Error(w, "lost acknowledgement", 504)
						return
					}
					_, _ = w.Write([]byte(`{"prompt_id":"abcdef0123456789"}`))
				case "/queue":
					running := []any{}
					if accepted.Load() && outputMode.Load() == "running" {
						running = append(running, []any{0, "abcdef0123456789", map[string]any{}, map[string]any{"gateway_job_id": f.job.PublicID}, []any{}})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"queue_running": running, "queue_pending": []any{}})
				case "/history", "/history/abcdef0123456789":
					if outputMode.Load() == "running" {
						_, _ = w.Write([]byte(`{}`))
						return
					}
					outputs := map[string]any{"3": map[string]any{"images": []any{map[string]string{"filename": "intermediate.png", "subfolder": "", "type": "temp"}}}}
					if outputMode.Load() != "missing-final" {
						filename, storage := "final.png", "output"
						if outputMode.Load() == "temp-final" {
							storage = "temp"
						}
						if outputMode.Load() == "wrong-type" {
							filename = "final.mp4"
						}
						outputs["6"] = map[string]any{"images": []any{map[string]string{"filename": filename, "subfolder": "", "type": storage}}}
					}
					completed, state := true, "success"
					if outputMode.Load() == "partial" {
						completed, state = false, "running"
					}
					if outputMode.Load() == "failed" {
						state = "error"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"abcdef0123456789": map[string]any{"outputs": outputs, "status": map[string]any{"completed": completed, "status_str": state}}})
				default:
					proxy.ServeHTTP(w, r)
				}
			}))
			t.Cleanup(server.Close)
			f.app.cfg.ComfyUIUpstream, _ = url.Parse(server.URL)
			if _, err := f.db.Exec(`UPDATE content_events SET is_sensitive=true WHERE generation_job_id=$1`, f.quote.Sources[0].Identity.JobID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`TRUNCATE gpu_resources CASCADE`); err != nil {
				t.Fatal(err)
			}
			f.app.gpuCoordinator = &gpuCoordinator{app: f.app}
			other, err := f.app.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, "postprocess-other-assistant", &f.user, "other-token", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := f.app.gpuCoordinator.dispatch(ctx, other); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`UPDATE generation_jobs SET dispatch_token='',dispatch_until=now()-interval '1 second' WHERE id=$1`, f.job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.app.dispatchGenerationJobs(ctx); err != nil {
				t.Fatal(err)
			}
			waiting, err := f.app.store.GenerationJobByID(ctx, f.job.ID)
			if err != nil || promptPosts.Load() != 0 || waiting.SubmissionStartedAt != nil || waiting.QuotaReservedOn == nil || waiting.State != domain.GenerationJobWaitingForResources {
				t.Fatal("postprocess bypassed GPU admission or lost reservation", waiting, err)
			}
			if err := f.app.gpuCoordinator.release(ctx, domain.GPUWorkAssistant, other.JobKey, "executor_terminal"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`UPDATE generation_jobs SET dispatch_until=now()-interval '1 second' WHERE id=$1`, f.job.ID); err != nil {
				t.Fatal(err)
			}
			_, err = f.app.dispatchGenerationJobs(ctx)
			if !lost && err != nil {
				t.Fatal(err)
			}
			if lost && err == nil {
				t.Fatal("unknown submission was not reported")
			}
			if promptPosts.Load() != 1 || f.uploads.Load() != 1 || f.views.Load() < 2 {
				t.Fatal("dispatch/readback count", promptPosts.Load(), f.uploads.Load(), f.views.Load())
			}
			record, err := f.app.store.GenerationExecutionForJob(ctx, f.user.ID, f.job.PublicID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.app.decodeGenerationExecution(f.job, record)
			if err != nil {
				t.Fatal(err)
			}
			if generationEvidenceDigest(graph.Load().(json.RawMessage)) != snapshot.GraphSHA256 || strings.Contains(string(snapshot.Graph), "KSampler") {
				t.Fatal("submitted graph differs from postprocess evidence")
			}
			if lost {
				f.app = &App{store: f.app.store, contentCipher: f.app.contentCipher, cfg: f.app.cfg, proxyCounts: map[string]int64{}}
				f.app.gpuCoordinator = &gpuCoordinator{app: f.app}
				job, err := f.app.store.GenerationJobByID(ctx, f.job.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, found, err := f.app.recoverGenerationJobPrompt(ctx, job); err != nil || !found {
					t.Fatal("restart recovery", err)
				}
			}
			job, err := f.app.store.GenerationJobByID(ctx, f.job.ID)
			if err != nil || job.PromptID != "abcdef0123456789" || job.QuotaCommittedAt == nil {
				t.Fatal("handoff/quota missing", job, err)
			}
			for i := 0; i < 2; i++ {
				if err := f.app.ensureGenerationJobProjections(ctx, job); err != nil {
					t.Fatal("idempotent projection", err)
				}
				if _, err := f.app.dispatchGenerationJobs(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if promptPosts.Load() != 1 {
				t.Fatal("duplicate prompt after recovery")
			}
			work, err := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
			if err != nil || work.Kind != domain.GPUWorkGeneration || work.JobKey != job.PublicID {
				t.Fatal("postprocess did not retain GPU ownership", work, err)
			}
			var events, variants int
			if err := f.db.QueryRow(`SELECT count(*) FROM content_events WHERE generation_job_id=$1`, job.ID).Scan(&events); err != nil || events != 1 {
				t.Fatal("content projection", events, err)
			}
			if err := f.db.QueryRow(`SELECT count(*) FROM quick_generation_variants WHERE job_id=$1`, job.ID).Scan(&variants); err != nil || variants != 1 {
				t.Fatal("typed postprocess projection missing or duplicated", variants, err)
			}
			outputMode.Store("complete")
			status, err := f.app.fetchGenerationStatus(ctx, job.PromptID, f.user.ID)
			if err != nil || status.State != "completed" || len(status.Outputs) != 1 || status.Outputs[0].Filename != "final.png" {
				t.Fatal("intermediate promoted to output", status, err)
			}
			finalStatus := status
			for _, mode := range []string{"missing-final", "temp-final", "wrong-type", "partial", "failed"} {
				outputMode.Store(mode)
				status, err = f.app.fetchGenerationStatus(ctx, job.PromptID, f.user.ID)
				wantState := "error"
				if mode == "partial" {
					wantState = "running"
				}
				if err != nil || status.State != wantState || len(status.Outputs) != 0 {
					t.Fatal("invalid final accepted", mode, status, err)
				}
			}
			if err := f.app.archiveGenerationOutputs(ctx, f.user.ID, finalStatus.Outputs); err != nil {
				t.Fatal("archive final output", err)
			}
			media, err := f.app.store.ListGenerationMediaForPrompts(ctx, f.user.ID, []string{job.PromptID})
			if err != nil || len(media[job.PromptID]) != 1 || media[job.PromptID][0].Filename != "final.png" || !media[job.PromptID][0].Sensitive {
				t.Fatal("final media or inherited sensitivity missing", media, err)
			}
			checkPostprocessProjectedViews(t, f, job)
		})
	}
}

func checkPostprocessProjectedViews(t *testing.T, f *postprocessInputFixture, job domain.GenerationJob) {
	t.Helper()
	ctx := context.Background()
	views, err := f.app.generationLibraryVariantViews(ctx, f.user.ID)
	if err != nil || len(views) != 1 || views[0].Operation != "postprocess" || views[0].Postprocess == nil || views[0].Values != nil {
		t.Fatal("typed library view", views, err)
	}
	encoded, err := json.Marshal(views[0])
	if err != nil || strings.Contains(string(encoded), `"seed":`) {
		t.Fatal("sampler seed fabricated", string(encoded), err)
	}
	gallery := generationGalleryItems(views)
	if len(gallery) != 1 || !gallery[0].HasMedia || gallery[0].Postprocess == nil || !json.Valid([]byte(gallery[0].PostprocessJSON)) || gallery[0].Prompt != "" {
		t.Fatal("gallery projection", gallery)
	}
	if _, err := f.db.Exec(`UPDATE generation_jobs SET state='completed',finished_at=now() WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	scope := domain.TaskScope{UserID: f.user.ID, Generation: true}
	detail, err := f.app.loadTaskDetail(ctx, scope, "generation", job.PublicID)
	if err != nil || detail.Generation == nil || detail.Generation.Retryable || detail.EditURL != "" || !strings.HasPrefix(detail.OpenURL, "/gallery?media=") || detail.Generation.Postprocess.Sources[0].Availability != "available" {
		t.Fatal("task treats postprocess as generation", detail, err)
	}
	page, err := f.app.store.ListTasks(ctx, scope, domain.TaskFilter{}, 40, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range page.Tasks {
		if task.ID == job.PublicID {
			found = true
			if task.Name != "Постобработка изображения" || taskNextAction(task, time.Now()) == "edit_generation" {
				t.Fatal("list invents model/edit action", task)
			}
		}
	}
	if !found {
		t.Fatal("postprocess missing from tasks")
	}
	checkPassport := func(availability string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/generate/jobs/passport?job="+job.PublicID, nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &f.user))
		w := httptest.NewRecorder()
		f.app.handleGenerationPassport(w, r)
		var passport struct {
			Operation   string           `json:"operation"`
			Seed        json.RawMessage  `json:"seed"`
			Postprocess *postprocessView `json:"postprocess"`
			Outputs     []struct {
				MediaID string `json:"media_id"`
			} `json:"outputs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &passport); err != nil || w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, no-store" || passport.Operation != "postprocess" || len(passport.Seed) != 0 || passport.Postprocess == nil || len(passport.Outputs) != 1 {
			t.Fatal("invalid postprocess HTTP passport", w.Code, w.Body.String(), err)
		}
		if passport.Postprocess.Sources[0].Availability != availability || passport.Postprocess.Sources[0].JobID != f.quote.Sources[0].Identity.JobPublicID || passport.Outputs[0].MediaID == "" {
			t.Fatal("postprocess passport provenance", w.Body.String())
		}
	}
	checkPassport("available")
	repeat, err := f.app.prepareGenerationRepeat(ctx, &f.user, job.PublicID)
	if err != nil || repeat.View.State != "blocked" || repeat.View.Seed != "" {
		t.Fatal("ordinary repeat accepted postprocess", repeat.View, err)
	}
	if _, err := f.db.Exec(`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, f.quote.Sources[0].Identity.MediaID); err != nil {
		t.Fatal(err)
	}
	detail, err = f.app.loadTaskDetail(ctx, scope, "generation", job.PublicID)
	if err != nil || detail.Generation.Postprocess.Sources[0].Availability != "unavailable" {
		t.Fatal("hidden source link still available", detail, err)
	}
	checkPassport("unavailable")
	if _, err := f.db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	views, err = f.app.generationLibraryVariantViews(ctx, f.user.ID)
	if err != nil || len(views) != 1 || views[0].JobID != "" || views[0].Postprocess.Sources[0].JobID != f.quote.Sources[0].Identity.JobPublicID || len(views[0].Media) != 1 {
		t.Fatal("job deletion erased retained result provenance", views, err)
	}
	ordinary, err := f.app.contentCipher.Encrypt(`{"version":1,"values":{"positive_prompt":"Ordinary generation","seed":"0"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.InsertGenerationVariant(ctx, f.user.ID, "ordinary-view-regression", "text-to-image", "photoflow-krea2", "Krea2", 0, ordinary); err != nil {
		t.Fatal(err)
	}
	views, err = f.app.generationLibraryVariantViews(ctx, f.user.ID)
	if err != nil || len(views) != 2 {
		t.Fatal("mixed library cannot be read", views, err)
	}
	for _, view := range views {
		if view.Operation == "postprocess" {
			continue
		}
		encoded, err := json.Marshal(view)
		if err != nil || view.Values["seed"] != "0" || !strings.Contains(string(encoded), `"seed":0`) {
			t.Fatal("ordinary zero seed changed", view, err)
		}
	}
}

func TestPostprocessFinalSubmissionGateIntegration(t *testing.T) {
	for _, mode := range []string{"mutated-readback", "schema-offline", "revoke", "cancel", "source-hidden", "unreserved", "expired-lease"} {
		t.Run(mode, func(t *testing.T) {
			f := newPostprocessInputFixture(t)
			ctx := context.Background()
			p, err := f.app.preparePostprocessDispatch(ctx, f.job)
			if err != nil {
				t.Fatal(err)
			}
			attachGenerationOutputPrefixes(p.Prompt, f.job.PublicID)
			f.mode.Store(mode)
			if mode == "revoke" {
				_, err = f.db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, f.user.ID)
			} else if mode == "cancel" {
				_, err = f.db.Exec(`UPDATE generation_jobs SET cancellation_requested_at=now() WHERE id=$1`, f.job.ID)
			} else if mode == "source-hidden" {
				_, err = f.db.Exec(`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, f.quote.Sources[0].Identity.MediaID)
			} else if mode == "unreserved" {
				_, err = f.app.store.ReleaseQuickGenerationForJob(ctx, f.job.ID)
			} else if mode == "expired-lease" {
				_, err = f.db.Exec(`UPDATE generation_jobs SET dispatch_until=now()-interval '1 second' WHERE id=$1`, f.job.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := f.app.refreshPostprocessSubmission(ctx, f.job, &p); err == nil {
				t.Fatal("final gate accepted drift")
			}
			if mode != "mutated-readback" && mode != "schema-offline" {
				if err := f.app.store.BeginGenerationJobSubmissionWithExecution(ctx, f.job.ID, f.job.DispatchToken,
					domain.GenerationExecutionRecord{JobID: f.job.ID, PayloadCipher: []byte("opaque"), PayloadSHA256: strings.Repeat("a", 64)}); err == nil {
					t.Fatal("transaction accepted revoked or cancelled child")
				}
			}
			var marked bool
			if err := f.db.QueryRow(`SELECT submission_started_at IS NOT NULL FROM generation_jobs WHERE id=$1`, f.job.ID).Scan(&marked); err != nil || marked {
				t.Fatal("failed gate persisted submission marker", err)
			}
			f.assertClean(t)
		})
	}
}

func TestPostprocessSubmissionSensitivityIntegration(t *testing.T) {
	f := newPostprocessInputFixture(t)
	ctx := context.Background()
	p, err := f.app.preparePostprocessDispatch(ctx, f.job)
	if err != nil {
		t.Fatal(err)
	}
	attachGenerationOutputPrefixes(p.Prompt, f.job.PublicID)
	record, err := f.app.capturePostprocessExecution(f.job, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.BeginGenerationJobSubmission(ctx, f.job.ID, f.job.DispatchToken); err == nil {
		t.Fatal("postprocess submission accepted without execution evidence")
	}
	if _, err := f.db.Exec(`UPDATE content_events SET is_sensitive=true WHERE generation_job_id=$1`, f.quote.Sources[0].Identity.JobID); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.BeginGenerationJobSubmissionWithExecution(ctx, f.job.ID, f.job.DispatchToken, record); err != nil {
		t.Fatal(err)
	}
	job, err := f.app.store.BindGenerationJobPrompt(ctx, f.job.ID, "abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.ensureGenerationJobProjections(ctx, job); err != nil {
		t.Fatal(err)
	}
	var sensitive bool
	if err := f.db.QueryRow(`SELECT is_sensitive FROM content_events WHERE generation_job_id=$1`, f.job.ID).Scan(&sensitive); err != nil || !sensitive {
		t.Fatal("sensitivity observed in final transaction lost", err)
	}
}

func TestPostprocessDispatchSupersededIntegration(t *testing.T) {
	for _, mode := range []string{"cancel", "new-claim", "expired", "already-bound"} {
		t.Run(mode, func(t *testing.T) {
			f := newPostprocessInputFixture(t)
			ctx := context.Background()
			var err error
			switch mode {
			case "cancel":
				_, err = f.db.Exec(`UPDATE generation_jobs SET cancellation_requested_at=now() WHERE id=$1`, f.job.ID)
			case "new-claim":
				_, err = f.db.Exec(`UPDATE generation_jobs SET dispatch_token='new-attempt' WHERE id=$1`, f.job.ID)
			case "expired":
				_, err = f.db.Exec(`UPDATE generation_jobs SET dispatch_until=now()-interval '1 second' WHERE id=$1`, f.job.ID)
			case "already-bound":
				_, err = f.app.store.BindGenerationJobPrompt(ctx, f.job.ID, "abcdef0123456789")
			}
			if err != nil {
				t.Fatal(err)
			}
			if handled, err := f.app.settlePostprocessDispatchChange(ctx, f.job); !handled || err != nil {
				t.Fatal("late attempt not settled", handled, err)
			}
			current, err := f.app.store.GenerationJobByID(ctx, f.job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" {
				if current.State != domain.GenerationJobCancelled || current.QuotaReservedOn != nil {
					t.Fatal("cancelled preparation was failed or kept quota", current)
				}
			} else if current.State == domain.GenerationJobFailed || current.QuotaReservedOn == nil || current.ResourcesReleasedAt != nil {
				t.Fatal("old attempt changed current work", current)
			}
			if mode == "new-claim" && current.DispatchToken != "new-attempt" {
				t.Fatal("replacement claim modified")
			}
			if mode == "expired" && current.DispatchToken != "" {
				t.Fatal("expired attempt was not postponed")
			}
		})
	}
}
