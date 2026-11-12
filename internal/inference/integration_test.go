package inference

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

var testRequest = []byte(`{"model":"fixture-vision","messages":[{"role":"user","content":"Describe the reference","images":["ZmFrZQ=="]}],"stream":false,"keep_alive":"0"}`)
var testResponse = []byte(`{"done":true,"message":{"role":"assistant","content":"A green vase on a desk."}}`)

type fixture struct {
	db     *sql.DB
	store  *store.Store
	cipher *contentcrypto.Cipher
	work   domain.GPUWork
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `TRUNCATE gpu_resources,users,gpu_inference_executors CASCADE`); err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, store: store.New(db)}
	f.cipher, err = contentcrypto.NewCipher("inference-fixture-secret-never-production")
	if err != nil {
		t.Fatal(err)
	}
	var user int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('inference-fixture','disabled','admin') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	f.work, err = f.store.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "fixture-inference", Kind: domain.GPUWorkAssistant, JobKey: "fixture", UserID: user, ResourceID: domain.GPUPrimaryResource})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := f.store.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ObserveGPUResource(ctx, resource.ID, resource.Revision, "idle", "fixture", time.Second*10); err != nil {
		t.Fatal(err)
	}
	permit, err := f.store.AcquireGPUWork(ctx, f.work.ID, "gateway-permit", time.Minute)
	if err != nil || !permit.Granted {
		t.Fatalf("permit: %+v %v", permit, err)
	}
	f.work = permit.Work
	return f
}

func (f *fixture) submit(t *testing.T) domain.GPUInference {
	t.Helper()
	job, err := NewClient(f.store, f.cipher).Submit(context.Background(), f.work, testRequest, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func (f *fixture) worker(t *testing.T, handler http.HandlerFunc) *Worker {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	w, err := NewWorker(f.store, f.cipher, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	return w
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fixture")
	}
}

func TestDurableInferenceReconnect(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	minerID, err := f.store.CreateMiner(ctx, store.CreateMinerParams{Name: "Inference fixture", ScriptPath: "fixture.cmd", ProcessName: "fixture.exe", CreatedByUserID: *f.work.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SetGPUInferencePreparation(ctx, f.work.ID, f.work.LeaseToken, false); err != nil {
		t.Fatal(err)
	}
	if err = f.store.CreateGPUInferenceMiningLease(ctx, domain.QuickGenerationMiningLease{ID: "inference-mining", GPUWorkID: f.work.ID, UserID: *f.work.UserID, MinerID: minerID, ScriptPath: "fixture.cmd", ProcessName: "fixture.exe", ResumeMining: true}, f.work.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SetGPUInferencePreparation(ctx, f.work.ID, f.work.LeaseToken, true); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	started, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(finish) })
	w := f.worker(t, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, testRequest) || r.URL.Path != "/api/chat" {
			t.Error("model request changed")
		}
		close(started)
		<-finish
		rw.Write(testResponse)
	})
	job := f.submit(t)
	var storedCipher []byte
	if err = f.db.QueryRow(`SELECT request_cipher FROM gpu_inference_requests WHERE id=$1`, job.ID).Scan(&storedCipher); err != nil {
		t.Fatal(err)
	}
	if len(storedCipher) == 0 || bytes.Contains(storedCipher, []byte("Describe")) || len(job.RequestCipher) != 0 {
		t.Fatal("request persisted in plaintext")
	}
	plain, err := f.cipher.DecryptBytesWithAAD(storedCipher, requestAAD(job.ID))
	if err != nil || !bytes.Equal(plain, testRequest) {
		t.Fatal("encrypted request did not preserve references")
	}
	workerDone := make(chan error, 1)
	go func() { _, err := w.RunOne(context.Background()); workerDone <- err }()
	await(t, started)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := NewClient(f.store, f.cipher).Wait(ctx, job.ID, job.LeaseToken); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost caller: %v", err)
	}
	// A fresh Gateway instance can repeat acceptance with exactly this ID.
	if _, err := NewClient(store.New(f.db), f.cipher).Submit(context.Background(), f.work, testRequest, time.Minute); err != nil {
		t.Fatal(err)
	}
	if worked, err := w.RunOne(context.Background()); err != nil || worked {
		t.Fatalf("running request reclaimed: %v %v", worked, err)
	}
	once.Do(func() { close(finish) })
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	saved, body, err := NewClient(store.New(f.db), f.cipher).Wait(ctx, job.ID, job.LeaseToken)
	if err != nil || !bytes.Equal(body, testResponse) || saved.CompletionCode != "executor_terminal" || calls.Load() != 1 {
		t.Fatalf("recovery: %+v %s %v calls=%d", saved, body, err, calls.Load())
	}
	work, err := f.store.GPUWork(ctx, job.ID)
	if err != nil || work.InferenceCompletion != "executor_terminal" || len(saved.RequestCipher) != 0 {
		t.Fatalf("missing atomic evidence: %+v %v", work, err)
	}
	resource, _ := f.store.GPUResource(ctx, work.ResourceID)
	if _, err = f.store.ReleaseGPUWork(ctx, work.ID, work.LeaseToken, domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: resource.Revision}); err != nil {
		t.Fatal(err)
	}
	leases, err := f.store.ListQuickGenerationMiningLeases(ctx)
	if err != nil || len(leases) != 1 || !leases[0].ResumeReady {
		t.Fatalf("mining not released with inference: %+v %v", leases, err)
	}
	if _, err = f.store.RetryGPUWork(ctx, work.ID); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("reused permanent inference ID: %v", err)
	}
}

func TestDurableInferenceExecutorPersistenceRetry(t *testing.T) {
	f := newFixture(t)
	f.submit(t)
	var calls atomic.Int32
	w := f.worker(t, func(rw http.ResponseWriter, r *http.Request) { calls.Add(1); rw.Write(testResponse) })
	_, err := f.db.Exec(`CREATE SEQUENCE inference_retry_seen;
		CREATE FUNCTION inference_retry_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.inference_completion='executor_terminal' THEN PERFORM nextval('inference_retry_seen'); RAISE EXCEPTION 'fixture persistence outage'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER inference_retry_fail BEFORE UPDATE ON gpu_work FOR EACH ROW EXECUTE FUNCTION inference_retry_fail()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.db.Exec(`DROP TRIGGER IF EXISTS inference_retry_fail ON gpu_work; DROP FUNCTION IF EXISTS inference_retry_fail(); DROP SEQUENCE IF EXISTS inference_retry_seen`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := w.RunOne(ctx); finished <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var attempted bool
		if err = f.db.QueryRow(`SELECT is_called FROM inference_retry_seen`).Scan(&attempted); err != nil {
			t.Fatal(err)
		}
		if attempted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("executor did not attempt persistence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	saved, err := f.store.GPUInference(ctx, f.work.ID)
	if err != nil || saved.State != "running" || len(saved.ResponseCipher) != 0 {
		t.Fatalf("partial commit: %+v %v", saved, err)
	}
	if _, err = f.db.Exec(`DROP TRIGGER inference_retry_fail ON gpu_work`); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err != nil || calls.Load() != 1 {
		t.Fatalf("persistence retried model call: %d %v", calls.Load(), err)
	}
	if _, body, err := NewClient(f.store, f.cipher).Wait(ctx, f.work.ID, f.work.LeaseToken); err != nil || !bytes.Equal(body, testResponse) {
		t.Fatalf("lost response: %s %v", body, err)
	}
}

func TestDurableInferenceConcurrentWorkers(t *testing.T) {
	f := newFixture(t)
	f.submit(t)
	var calls atomic.Int32
	w := f.worker(t, func(rw http.ResponseWriter, r *http.Request) { calls.Add(1); rw.Write(testResponse) })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.RunOne(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent sends: %d", calls.Load())
	}
}

func TestDurableInferenceLateCompletionAndCancellation(t *testing.T) {
	f := newFixture(t)
	job := f.submit(t)
	ctx := context.Background()
	claimed, err := f.store.ClaimGPUInference(ctx, "executor")
	if err != nil {
		t.Fatal(err)
	}
	if job, err = f.store.CancelGPUInference(ctx, job.ID); err != nil || job.Settled() {
		t.Fatalf("running cancellation claimed completion %+v %v", job, err)
	}
	if _, err = f.db.Exec(`UPDATE gpu_work SET lease_until=now()-interval '1 hour',state='uncertain'`); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishGPUInference(ctx, job.ID, claimed.RunToken, "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err = NewClient(f.store, f.cipher).Wait(ctx, job.ID, job.LeaseToken); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	body, err := f.cipher.EncryptBytesWithAAD(testResponse, responseAAD(job.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishGPUInference(ctx, job.ID, claimed.RunToken, "executor_terminal", 200, body); err != nil {
		t.Fatal(err)
	}
	if _, response, err := NewClient(f.store, f.cipher).Wait(ctx, job.ID, job.LeaseToken); err != nil || !bytes.Equal(response, testResponse) {
		t.Fatalf("late result %s %v", response, err)
	}
	if _, err = f.db.Exec(`UPDATE gpu_inference_requests SET result_expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.RedactExpiredGPUInference(ctx); err != nil {
		t.Fatal(err)
	}
	if saved, _, err := NewClient(f.store, f.cipher).Wait(ctx, job.ID, job.LeaseToken); !errors.Is(err, ErrResultExpired) || !saved.Settled() {
		t.Fatalf("expired result lost completion %+v %v", saved, err)
	}
}

func TestDurableInferenceInvalidPayloadNeverReachesModel(t *testing.T) {
	for _, scenario := range []string{"invalid_json", "stream", "corrupt_cipher", "hash_mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			body := testRequest
			if scenario == "invalid_json" {
				body = []byte("invalid")
			}
			if scenario == "stream" {
				body = []byte(`{"model":"fixture","stream":true}`)
			}
			job, err := NewClient(f.store, f.cipher).Submit(context.Background(), f.work, body, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "corrupt_cipher" {
				_, err = f.db.Exec(`UPDATE gpu_inference_requests SET request_cipher=$1`, []byte("invalid"))
			}
			if scenario == "hash_mismatch" {
				_, err = f.db.Exec(`UPDATE gpu_inference_requests SET request_hash=repeat('0',64)`)
			}
			if err != nil {
				t.Fatal(err)
			}
			w := f.worker(t, func(rw http.ResponseWriter, r *http.Request) {
				t.Error("invalid payload reached model")
				rw.Write(testResponse)
			})
			if _, err = w.RunOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, _, err = NewClient(f.store, f.cipher).Wait(context.Background(), job.ID, job.LeaseToken); !errors.Is(err, ErrNotDispatched) {
				t.Fatal(err)
			}
		})
	}
}

func TestDurableInferenceAcceptanceGuards(t *testing.T) {
	for _, scenario := range []string{"preparation", "expired", "wrong_token", "cancelled", "generation"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			switch scenario {
			case "preparation":
				if err := f.store.SetGPUInferencePreparation(ctx, f.work.ID, f.work.LeaseToken, false); err != nil {
					t.Fatal(err)
				}
			case "expired":
				if _, err := f.db.Exec(`UPDATE gpu_work SET lease_until=now()-interval '1 second'`); err != nil {
					t.Fatal(err)
				}
			case "wrong_token":
				f.work.LeaseToken = "wrong"
			case "cancelled":
				if _, err := f.store.RequestGPUWorkCancellation(ctx, f.work.ID); err != nil {
					t.Fatal(err)
				}
			case "generation":
				if _, err := f.db.Exec(`UPDATE gpu_work SET kind='generation'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := NewClient(f.store, f.cipher).Submit(ctx, f.work, testRequest, time.Minute); !errors.Is(err, store.ErrGPUWorkConflict) {
				t.Fatalf("accepted invalid permit: %v", err)
			}
		})
	}
	t.Run("concurrent_duplicates", func(t *testing.T) {
		f := newFixture(t)
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := NewClient(f.store, f.cipher).Submit(context.Background(), f.work, testRequest, time.Minute)
				if err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		var count int
		if err := f.db.QueryRow(`SELECT count(*) FROM gpu_inference_requests`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("duplicate: %d %v", count, err)
		}
		if _, err := NewClient(f.store, f.cipher).Submit(context.Background(), f.work, []byte(`{"model":"different"}`), time.Minute); !errors.Is(err, store.ErrGPUWorkConflict) {
			t.Fatalf("payload mutation: %v", err)
		}
		if _, err := f.store.SetGPUWorkPhase(context.Background(), f.work.ID, f.work.LeaseToken, "dispatching", ""); !errors.Is(err, store.ErrGPUWorkConflict) {
			t.Fatalf("executor binding cleared: %v", err)
		}
	})
}

func TestDurableInferenceUncertainDoesNotReleaseOrReplay(t *testing.T) {
	for _, scenario := range []string{"504", "malformed", "not_done", "oversized", "worker_restart", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			var calls atomic.Int32
			w := f.worker(t, func(rw http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch scenario {
				case "504":
					rw.WriteHeader(504)
				case "malformed":
					rw.Write([]byte(`{"done":true`))
				case "not_done":
					rw.Write([]byte(`{"done":false}`))
				case "oversized":
					rw.Write(bytes.Repeat([]byte("x"), domain.MaxGPUInferenceResponseBytes+1))
				case "redirect":
					rw.Header().Set("Location", "/api/chat")
					rw.WriteHeader(307)
				default:
					rw.Write(testResponse)
				}
			})
			job := f.submit(t)
			if scenario == "worker_restart" {
				if _, err := f.store.ClaimGPUInference(context.Background(), "lost-worker"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := w.RunOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := f.store.RecordGPUInferenceCompletion(context.Background(), domain.GPUWorkAssistant, "fixture", "executor_terminal"); err != nil {
				t.Fatal(err)
			}
			work, _ := f.store.GPUWork(context.Background(), job.ID)
			if work.InferenceCompletion != "" {
				t.Fatal("caller fabricated executor evidence")
			}
			resource, _ := f.store.GPUResource(context.Background(), work.ResourceID)
			if _, err := f.store.ReleaseGPUWork(context.Background(), work.ID, work.LeaseToken, domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: resource.Revision}); !errors.Is(err, store.ErrGPUWorkConflict) {
				t.Fatalf("unknown execution released: %v", err)
			}
			if _, err := f.db.Exec(`UPDATE gpu_work SET lease_until=now()-interval '1 hour'`); err != nil {
				t.Fatal(err)
			}
			if _, changed, err := f.store.FenceExpiredGPUInferenceReservation(context.Background(), job.ID); err != nil || changed {
				t.Fatalf("expired sent job fenced: %v %v", changed, err)
			}
			if worked, err := w.RunOne(context.Background()); err != nil || worked {
				t.Fatalf("unknown execution replayed: %v %v", worked, err)
			}
			want := int32(1)
			if scenario == "worker_restart" {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("unexpected calls: %d", calls.Load())
			}
		})
	}
}

func TestDurableInferenceCancellationAndRetention(t *testing.T) {
	f := newFixture(t)
	job := f.submit(t)
	job, err := f.store.CancelGPUInference(context.Background(), job.ID)
	if err != nil || job.State != "not_dispatched" {
		t.Fatalf("cancel: %+v %v", job, err)
	}
	if _, err = NewClient(f.store, f.cipher).Submit(context.Background(), f.work, testRequest, time.Minute); err != nil {
		t.Fatal(err)
	}
	if work, err := f.store.ClaimGPUInference(context.Background(), "late-worker"); err != nil || work.ID != "" {
		t.Fatalf("cancelled job sent: %+v %v", work, err)
	}
	if _, _, err = NewClient(f.store, f.cipher).Wait(context.Background(), job.ID, job.LeaseToken); !errors.Is(err, ErrNotDispatched) {
		t.Fatal(err)
	}
	// Retention redacts bytes, but never erases the dispatch tombstone.
	if _, err = f.db.Exec(`UPDATE gpu_inference_requests SET response_cipher=$1,result_expires_at=now()-interval '1 second'`, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if n, err := f.store.RedactExpiredGPUInference(context.Background()); err != nil || n != 1 {
		t.Fatalf("retention %d %v", n, err)
	}
	job, err = f.store.GPUInference(context.Background(), job.ID)
	if err != nil || len(job.ResponseCipher) != 0 || job.State != "not_dispatched" {
		t.Fatalf("lost tombstone %+v %v", job, err)
	}
}

func TestDurableInferenceResultCommitRollback(t *testing.T) {
	f := newFixture(t)
	job := f.submit(t)
	claimed, err := f.store.ClaimGPUInference(context.Background(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`CREATE FUNCTION inference_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.inference_completion='executor_terminal' THEN RAISE EXCEPTION 'fixture failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER inference_test_fail BEFORE UPDATE ON gpu_work FOR EACH ROW EXECUTE FUNCTION inference_test_fail()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.db.Exec(`DROP TRIGGER IF EXISTS inference_test_fail ON gpu_work; DROP FUNCTION IF EXISTS inference_test_fail()`)
	})
	if err = f.store.FinishGPUInference(context.Background(), job.ID, claimed.RunToken, "executor_terminal", 200, []byte("encrypted-fixture")); err == nil {
		t.Fatal("expected transaction failure")
	}
	saved, _ := f.store.GPUInference(context.Background(), job.ID)
	if saved.State != "running" || len(saved.ResponseCipher) != 0 {
		t.Fatalf("partial result commit %+v", saved)
	}
	if _, err = f.db.Exec(`DROP TRIGGER inference_test_fail ON gpu_work`); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishGPUInference(context.Background(), job.ID, "wrong", "executor_terminal", 200, []byte("encrypted-fixture")); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("wrong executor %v", err)
	}
	if err = f.store.FinishGPUInference(context.Background(), job.ID, claimed.RunToken, "executor_terminal", 200, []byte("encrypted-fixture")); err != nil {
		t.Fatal(err)
	}
}
