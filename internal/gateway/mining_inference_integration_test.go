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
			writeJSON(w, 200, mining.State{Running: running.Load(), ProcessName: "test.exe"})
		case "/v1/stop":
			running.Store(false)
			writeJSON(w, 200, mining.State{Running: false, ProcessName: "test.exe"})
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
			writeJSON(w, 200, mining.State{Running: true, ProcessName: "test.exe"})
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
		return &App{store: repository, mining: mining.NewClient(agentURL, "test"), contentCipher: cipher,
			cfg:        Config{MediaSpoolDir: t.TempDir(), MediaInFlightLimitBytes: 256 << 20},
			csrfSigner: security.NewCSRFSigner("inference-test"), promptAssistantSlots: make(chan struct{}, 1)}
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
				running.Store(true)
				starts.Store(0)
				entered, reply := make(chan struct{}), make(chan struct{})
				model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				callCtx, callCancel := context.WithCancel(traceContext(ctx, newRequestID(), 0, ""))
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
						if outcome == "done" && w.Code != 200 {
							return fmt.Errorf("assistant: %d %s", w.Code, w.Body.String())
						}
						if outcome != "done" && w.Code == 200 {
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
				if outcome == "done" {
					assertLeases(0, false)
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
}
