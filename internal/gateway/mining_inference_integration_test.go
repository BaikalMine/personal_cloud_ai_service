package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestInferenceMiningCompletionIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	if _, err := db.ExecContext(ctx, `TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	repository := store.New(db)
	var userID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,pause_mining_for_quick_generation) VALUES('inference-owner','hash','admin',true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	minerID, err := repository.CreateMiner(ctx, store.CreateMinerParams{Name: "Inference miner", ScriptPath: `C:\Mining\test.bat`, ProcessName: "test.exe", Enabled: true, Default: true, CreatedByUserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	var running, unavailable, loseStartReply atomic.Bool
	var starts atomic.Int32
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		switch r.URL.Path {
		case "/v1/state":
			writeJSON(w, 200, mining.State{Available: true, Running: running.Load(), ProcessName: "test.exe"})
		case "/v1/stop":
			running.Store(false)
			writeJSON(w, 200, mining.State{Available: true, Running: false, ProcessName: "test.exe"})
		case "/v1/start":
			// Verify durability at the exact network boundary, not only afterward.
			leases, err := repository.ListQuickGenerationMiningLeases(ctx)
			if err != nil || len(leases) != 1 || !leases[0].ResumeReady {
				t.Errorf("start without durable completion: %+v err=%v", leases, err)
			}
			starts.Add(1)
			running.Store(true)
			if loseStartReply.Load() {
				http.Error(w, "lost reply", 504)
				return
			}
			writeJSON(w, 200, mining.State{Available: true, Running: true, ProcessName: "test.exe"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()
	agentURL, _ := url.Parse(agent.URL)
	cipher, err := contentcrypto.NewCipher("inference-test-secret-32-bytes-long")
	if err != nil {
		t.Fatal(err)
	}
	newApp := func() *App {
		app := &App{store: repository, mining: mining.NewClient(agentURL, "test"), contentCipher: cipher,
			cfg:        Config{MediaSpoolDir: t.TempDir(), MediaInFlightLimitBytes: 256 << 20},
			csrfSigner: security.NewCSRFSigner("inference-test"), promptAssistantSlots: make(chan struct{}, 1)}
		app.gpuCoordinator = &gpuCoordinator{app: app}
		return app
	}
	assertLeases := func(count int, ready bool) []domain.QuickGenerationMiningLease {
		t.Helper()
		leases, err := repository.ListQuickGenerationMiningLeases(ctx)
		if err != nil || len(leases) != count {
			t.Fatalf("leases=%+v count=%d err=%v", leases, count, err)
		}
		for _, lease := range leases {
			if lease.ResumeReady != ready {
				t.Fatalf("lease readiness %+v want=%v", lease, ready)
			}
		}
		return leases
	}
	for _, kind := range []string{"assistant", "caption"} {
		for _, outcome := range []string{"done", "proxy_timeout", "cancelled"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				completed := outcome == "done" || (kind == "assistant" && outcome == "cancelled")
				running.Store(true)
				starts.Store(0)
				entered, reply := make(chan struct{}), make(chan struct{})
				model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/ps" {
						writeJSON(w, 200, map[string]any{"models": []any{}})
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					close(entered)
					select {
					case <-reply:
					case <-r.Context().Done():
						return
					}
					if outcome == "proxy_timeout" {
						http.Error(w, "timed out upstream", 504)
						return
					}
					writeJSON(w, 200, map[string]any{"done": true, "message": map[string]string{"content": `{"prompt":"A landscape in daylight","references":[],"caption":"sample, landscape in daylight"}`}})
				}))
				defer model.Close()
				defer func() {
					select {
					case <-reply:
					default:
						close(reply)
					}
				}()
				base, _ := url.Parse(model.URL)
				app := newApp()
				app.promptAssistant = promptassistant.NewClient(base, "test")
				correlation := newRequestID()
				callCtx, callCancel := context.WithCancel(traceContext(ctx, correlation, 0, ""))
				defer callCancel()
				user := &User{ID: userID, Role: "admin", Username: "inference-owner", PauseMiningForQuickGeneration: true}
				var call func() error
				if kind == "assistant" {
					form := url.Values{"csrf": {app.csrfSigner.Token("session")}, "template_id": {string(promptassistant.ModeTextToImage)}, "prompt": {"A landscape"}}
					r := httptest.NewRequest(http.MethodPost, "/api/generate/prompt-assistant", strings.NewReader(form.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session"})
					r = r.WithContext(context.WithValue(callCtx, userCtxKey, user))
					call = func() error {
						w := httptest.NewRecorder()
						app.handlePromptAssistant(w, r)
						if completed && w.Code != 200 {
							return fmt.Errorf("assistant: %d %s", w.Code, w.Body.String())
						}
						if !completed && w.Code == 200 {
							return fmt.Errorf("uncertain assistant succeeded")
						}
						return nil
					}
				} else {
					asset, err := app.persistLoraDatasetImage(ctx, userID, "photo.png", bytes.NewReader(datasetTestPNG(t, 768, 768)))
					if err != nil {
						t.Fatal(err)
					}
					job, err := app.makeLoraCaptionJob("dataset", domain.LoraDatasetImage{ID: "photo", AssetID: asset.ID, CaptionRevision: "first"}, asset, "sample", "character")
					if err != nil {
						t.Fatal(err)
					}
					job.UserID = userID
					call = func() error {
						_, err := app.executeLoraCaption(callCtx, job)
						if outcome == "done" {
							return err
						}
						if err == nil {
							return fmt.Errorf("uncertain caption succeeded")
						}
						return nil
					}
				}
				done := make(chan error, 1)
				go func() { done <- call() }()
				select {
				case <-entered:
				case err := <-done:
					t.Fatalf("call returned before model: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				leases := assertLeases(1, false)
				work, err := repository.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
				if err != nil || work.Phase != "dispatching" {
					t.Fatalf("inference lacks GPU ownership: %+v %v", work, err)
				}
				if leases[0].GPUWorkID != work.ID {
					t.Fatalf("inference mining lease is anonymous: %+v", leases[0])
				}
				if _, err := db.ExecContext(ctx, `UPDATE quick_generation_mining_leases SET created_at=now()-interval '1 day' WHERE id=$1`, leases[0].ID); err != nil {
					t.Fatal(err)
				}
				// A fresh App models loss of all in-memory request knowledge.
				if _, err := newApp().refreshQuickGenerationMiningLeases(ctx); err != nil {
					t.Fatal(err)
				}
				assertLeases(1, false)
				if starts.Load() != 0 || running.Load() {
					t.Fatal("mining resumed while inference was active")
				}
				if outcome == "cancelled" {
					callCancel()
					if kind == "assistant" {
						select {
						case err := <-done:
							t.Fatalf("browser disconnect discarded pending model response: %v", err)
						case <-time.After(50 * time.Millisecond):
						}
						assertLeases(1, false)
						if starts.Load() != 0 || running.Load() {
							t.Fatal("browser disconnect resumed mining before model completion")
						}
						close(reply)
					}
				} else {
					close(reply)
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if completed {
					assertLeases(0, false)
					if kind == "assistant" {
						var events, runs int
						err := db.QueryRowContext(ctx, `SELECT
						 (SELECT count(*) FROM content_events WHERE correlation_id=$1 AND generation_state='completed'),
						 (SELECT count(*) FROM prompt_assistant_runs WHERE correlation_id=$1 AND status='completed')`, correlation).Scan(&events, &runs)
						if err != nil || events != 1 || runs != 1 {
							t.Fatalf("completion audit missing: events=%d runs=%d err=%v", events, runs, err)
						}
					}
					if starts.Load() != 1 {
						t.Fatalf("completed start count=%d", starts.Load())
					}
				} else {
					assertLeases(1, false)
					if _, err := newApp().refreshQuickGenerationMiningLeases(ctx); err != nil {
						t.Fatal(err)
					}
					if starts.Load() != 0 {
						t.Fatal("uncertain response resumed mining")
					}
					// The fake executor is now known stopped by this test, not by age.
					if _, err := newApp().refreshManagedGPUWork(ctx); err != nil {
						t.Fatal(err)
					}
					retained, err := repository.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
					if err != nil || retained.ID != work.ID || retained.InferenceCompletion != "" {
						t.Fatalf("unknown inference freed or completed: %+v %v", retained, err)
					}
					app.gpuCoordinator.finishInference(work.Kind, work.JobKey, promptassistant.ExecutionCompleted)
					app.finishInferenceMiningPause(&leases[0], promptassistant.ExecutionCompleted)
					assertLeases(0, false)
				}
			})
		}
	}
	t.Run("resume_retries_survive_restart_and_lost_reply", func(t *testing.T) {
		running.Store(false)
		starts.Store(0)
		lease := domain.QuickGenerationMiningLease{ID: "retry-resume", UserID: userID, MinerID: minerID, ScriptPath: `C:\Mining\test.bat`, ProcessName: "test.exe", ResumeMining: true}
		if err := repository.CreateQuickGenerationMiningLease(ctx, lease); err != nil {
			t.Fatal(err)
		}
		unavailable.Store(true)
		app := newApp()
		app.finishInferenceMiningPause(&lease, promptassistant.ExecutionNotDispatched)
		assertLeases(1, true)
		unavailable.Store(false)
		loseStartReply.Store(true)
		if _, err := newApp().refreshQuickGenerationMiningLeases(ctx); err != nil {
			t.Fatal(err)
		}
		assertLeases(1, true)
		if starts.Load() != 1 || !running.Load() {
			t.Fatal("fake miner was not started")
		}
		loseStartReply.Store(false)
		if _, err := newApp().refreshQuickGenerationMiningLeases(ctx); err != nil {
			t.Fatal(err)
		}
		assertLeases(0, false)
		if starts.Load() != 1 {
			t.Fatal("restart sent duplicate start instead of checking state")
		}
	})
	t.Run("only_final_confirmed_work_resumes", func(t *testing.T) {
		running.Store(false)
		starts.Store(0)
		app := newApp()
		for _, id := range []string{"one", "two"} {
			if err := repository.CreateQuickGenerationMiningLease(ctx, domain.QuickGenerationMiningLease{ID: id, UserID: userID, MinerID: minerID, ScriptPath: `C:\Mining\test.bat`, ProcessName: "test.exe", ResumeMining: true}); err != nil {
				t.Fatal(err)
			}
		}
		if !app.releaseMiningPause(ctx, "one") {
			t.Fatal("first release failed")
		}
		assertLeases(1, false)
		if starts.Load() != 0 {
			t.Fatal("first completion resumed miner")
		}
		if !app.releaseMiningPause(ctx, "two") {
			t.Fatal("final release failed")
		}
		assertLeases(0, false)
		if starts.Load() != 1 {
			t.Fatal("final completion did not resume miner")
		}
	})
	t.Run("external_Comfy_work_defers_mining_resume", func(t *testing.T) {
		running.Store(false)
		starts.Store(0)
		var busy atomic.Bool
		busy.Store(true)
		comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			running := []any{}
			if busy.Load() {
				running = append(running, []any{0, "manual-job", map[string]any{}, map[string]any{}, []any{}})
			}
			writeJSON(w, 200, map[string]any{"queue_running": running, "queue_pending": []any{}})
		}))
		defer comfy.Close()
		endpoint, _ := url.Parse(comfy.URL)
		app := newApp()
		app.cfg.ComfyUIUpstream = endpoint
		if err := repository.CreateQuickGenerationMiningLease(ctx, domain.QuickGenerationMiningLease{ID: "external-work", UserID: userID, MinerID: minerID, ScriptPath: `C:\Mining\test.bat`, ProcessName: "test.exe", ResumeMining: true}); err != nil {
			t.Fatal(err)
		}
		if app.releaseMiningPause(ctx, "external-work") || starts.Load() != 0 {
			t.Fatal("miner resumed over manual Comfy work")
		}
		assertLeases(1, true)
		busy.Store(false)
		if !app.releaseMiningPause(ctx, "external-work") || starts.Load() != 1 {
			t.Fatal("miner did not resume after external work")
		}
		assertLeases(0, false)
	})
	t.Run("abandoned_ready_reservation_recovers_mining", func(t *testing.T) {
		running.Store(true)
		starts.Store(0)
		app := newApp()
		user := &User{ID: userID, Role: "admin", PauseMiningForQuickGeneration: true}
		work, err := app.gpuCoordinator.admit(ctx, domain.GPUWorkAssistant, "abandoned-before-model", user, "old", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		lease, _, err := app.pauseMiningForInference(ctx, user, work)
		if err != nil || lease == nil || lease.GPUWorkID != work.ID || running.Load() {
			t.Fatalf("prepare: %+v %v", lease, err)
		}
		if _, err = db.ExecContext(ctx, `UPDATE gpu_work SET lease_until=now()-interval '1 minute' WHERE id=$1`, work.ID); err != nil {
			t.Fatal(err)
		}
		if n, err := newApp().refreshManagedGPUWork(ctx); err != nil || n != 1 {
			t.Fatalf("recover=%d %v", n, err)
		}
		assertLeases(1, true)
		if _, err = newApp().refreshQuickGenerationMiningLeases(ctx); err != nil {
			t.Fatal(err)
		}
		assertLeases(0, false)
		if starts.Load() != 1 || !running.Load() {
			t.Fatal("abandoned reservation did not resume mining")
		}
		if err := app.gpuCoordinator.dispatch(ctx, work); err == nil {
			t.Fatal("old request entered model after recovery")
		}
	})
}
