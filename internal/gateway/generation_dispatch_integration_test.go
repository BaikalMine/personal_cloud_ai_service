package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestComfyPromptValidationRejected(t *testing.T) {
	for _, tc := range []struct {
		status   int
		body     string
		rejected bool
	}{
		{400, `{"error":{"type":"prompt_outputs_failed_validation"},"node_errors":{}}`, true},
		{400, `{"error":{"type":"prompt_no_outputs"}}`, true},
		{400, `{"error":{"type":"no_prompt"}}`, true},
		{400, `{"error":{"type":"invalid_prompt_id"}}`, true},
		{504, `{"error":{"type":"no_prompt"}}`, false},
		{400, `{"error":{"type":"internal_error"}}`, false},
		{400, `<html>proxy unavailable</html>`, false},
		{400, `{"error":{"type":"no_prompt"}`, false},
		{400, `{"error":{"type":"no_prompt"},"prompt_id":"accepted"}`, false},
		{200, `{"error":{"type":"no_prompt"}}`, false},
	} {
		if got := comfyPromptValidationRejected(tc.status, []byte(tc.body)); got != tc.rejected {
			t.Errorf("%d %s: %v", tc.status, tc.body, got)
		}
	}
}

type dispatchFixture struct {
	app      *App
	db       *sql.DB
	user     User
	form     url.Values
	posts    atomic.Int32
	accepted atomic.Bool
	jobID    atomic.Value
	status   int
	body     string
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	f := &dispatchFixture{db: db, status: 200, body: `{"prompt_id":"abcdef0123456789"}`}
	f.jobID.Store("")
	info := compatibilityFixtureObjectInfo(t)
	catalog := buildGenerationModelCatalog(info)
	var preset generationPreset
	for _, item := range buildGenerationPresets(catalog) {
		if item.ID == "photoflow-krea2" {
			preset = item
			break
		}
	}
	if !preset.Available {
		t.Fatal("Krea2 fixture preset unavailable")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/object_info":
			_ = json.NewEncoder(w).Encode(info)
		case "/prompt":
			f.posts.Add(1)
			var document struct {
				ExtraData map[string]any `json:"extra_data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&document); err != nil {
				t.Error(err)
			}
			f.jobID.Store(fmt.Sprint(document.ExtraData["gateway_job_id"]))
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.body))
		case "/queue":
			running := []any{}
			if f.accepted.Load() {
				running = append(running, []any{0, "abcdef0123456789", map[string]any{}, map[string]any{"gateway_job_id": f.jobID.Load()}, []any{}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"queue_running": running, "queue_pending": []any{}})
		case "/history", "/history/abcdef0123456789":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	cipher, err := contentcrypto.NewCipher("dispatch-fixture-secret-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	f.app = &App{store: store.New(db), contentCipher: cipher, proxyCounts: map[string]int64{}, cfg: Config{ComfyUIUpstream: endpoint, SessionSecret: "dispatch-fixture-secret-at-least-32-characters", MediaSpoolDir: t.TempDir()}}
	var id int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role,can_use_quick_generation) VALUES('dispatch-owner','disabled','admin',true) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.user, err = f.app.store.UserByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f.form = url.Values{"template_id": {"text-to-image"}, "generation_workflow": {preset.ID}, "model": {preset.ModelID}, "positive_prompt": {"A ceramic teapot on a table in daylight."}, "seed": {"42"}, "width": {"512"}, "height": {"512"}, "steps": {"8"}, "cfg": {"1"}, "sampler_name": {"euler"}, "scheduler": {"simple"}}
	return f
}

func (f *dispatchFixture) enqueue(t *testing.T) domain.GenerationJob {
	t.Helper()
	ctx := context.Background()
	input, err := parseGenerationValues(ctx, f.form)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.app.prepareGeneration(ctx, &f.user, input, false)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := f.app.generationJobPayloadCipher(prepared.Input, f.form)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := f.app.store.ClaimGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: newRequestID(), UserID: f.user.ID, UsernameSnapshot: f.user.Username, RequestID: newRequestID()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.app.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{State: domain.GenerationJobPreparing}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.PrepareGenerationJob(ctx, job.ID, domain.PreparedGenerationJob{TemplateID: input.TemplateID, WorkflowID: input.PresetID, ModelName: prepared.Input.ModelName, Seed: 42, PayloadCipher: payload}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.app.store.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{State: domain.GenerationJobWaitingForResources}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.app.store.ReserveQuickGenerationForJob(ctx, job.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	job, err = f.app.store.QueueGenerationJobDispatch(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestGenerationDispatchIntegration(t *testing.T) {
	t.Run("resource release permanently closes publication and batch claims", func(t *testing.T) {
		f := newDispatchFixture(t)
		ctx := context.Background()
		job := f.enqueue(t)
		claimed, err := f.app.store.ClaimNextGenerationDispatch(ctx, "closing", 2)
		if err != nil {
			t.Fatal(err)
		}
		if fenced, err := f.app.store.FenceGenerationJobResourceRelease(ctx, job.ID, claimed.DispatchToken); err != nil || !fenced {
			t.Fatalf("close: %v %v", fenced, err)
		}
		if _, err = f.app.store.QueueGenerationJobDispatch(ctx, job.ID); !errors.Is(err, store.ErrGenerationJobStateConflict) {
			t.Fatalf("republished closed job: %v", err)
		}
		if _, err = f.app.store.ClaimNextGenerationDispatch(ctx, "late", 2); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("claimed closed job: %v", err)
		}
		if err = f.app.store.BeginGenerationJobSubmission(ctx, job.ID, "closing"); !errors.Is(err, store.ErrGenerationJobStateConflict) {
			t.Fatalf("sent closed job: %v", err)
		}
		prepared := domain.PreparedGenerationJob{TemplateID: job.TemplateID, WorkflowID: job.WorkflowID, ModelName: job.ModelName, Seed: job.Seed, PayloadCipher: job.PayloadCipher}
		params := domain.CreateGenerationBatchParams{PublicID: newRequestID(), UserID: f.user.ID, UsernameSnapshot: f.user.Username, RequestID: newRequestID(), TemplateID: job.TemplateID, WorkflowID: job.WorkflowID, ModelName: job.ModelName, Mode: domain.GenerationBatchSeeds, MaxParallel: 1}
		for position := 1; position <= 2; position++ {
			params.Jobs = append(params.Jobs, domain.CreateGenerationBatchJobParams{PublicID: newRequestID(), CorrelationID: newRequestID(), RequestID: newRequestID(), Position: position, Prepared: prepared})
		}
		batch, _, err := f.app.store.CreateGenerationBatch(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		children, err := f.app.store.GenerationBatchJobs(ctx, f.user.ID, batch.ID)
		if err != nil || len(children) != 2 {
			t.Fatalf("children: %d %v", len(children), err)
		}
		for _, child := range children {
			if fenced, err := f.app.store.FenceGenerationJobResourceRelease(ctx, child.ID, ""); err != nil || !fenced {
				t.Fatalf("close draft: %v %v", fenced, err)
			}
		}
		if _, err = f.app.store.ClaimNextGenerationDispatch(ctx, "late-batch", 2); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("claimed closed batch draft: %v", err)
		}
	})
	t.Run("batch cancellation reports unconfirmed sends and skips remaining variants", func(t *testing.T) {
		for _, submitted := range []bool{false, true} {
			t.Run(fmt.Sprint("submitted-", submitted), func(t *testing.T) {
				f := newDispatchFixture(t)
				f.app.csrfSigner = security.NewCSRFSigner("dispatch-csrf-test-secret")
				request := func(path string, values url.Values) *http.Request {
					values.Set("csrf", f.app.csrfSigner.Token("fixture-session"))
					r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "fixture-session"})
					return r.WithContext(context.WithValue(r.Context(), userCtxKey, &f.user))
				}
				f.form.Set("batch_count", "2")
				f.form.Set("batch_mode", string(domain.GenerationBatchSeeds))
				f.form.Set("client_request_id", newRequestID())
				f.form.Set("correlation_id", newRequestID())
				w := httptest.NewRecorder()
				f.app.handleGenerationBatches(w, request("/generate/batches", f.form))
				var created struct {
					Batch generationBatchView `json:"batch"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != http.StatusCreated || len(created.Batch.Jobs) != 2 {
					t.Fatalf("batch create: %d %s %v", w.Code, w.Body.String(), err)
				}
				if f.posts.Load() != 0 {
					t.Fatal("batch creation submitted a prompt")
				}
				if submitted {
					f.status, f.body = http.StatusGatewayTimeout, `{}`
					if _, err := f.app.dispatchGenerationJobs(context.Background()); err == nil {
						t.Fatal("expected uncertain receipt")
					}
				}
				w = httptest.NewRecorder()
				f.app.handleGenerationBatchCancel(w, request("/generate/batches/cancel", url.Values{"batch_id": {created.Batch.BatchID}}))
				var cancelled struct {
					Batch     generationBatchView `json:"batch"`
					Cancelled bool                `json:"cancelled"`
					Message   string              `json:"message"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &cancelled); err != nil || w.Code != http.StatusAccepted {
					t.Fatalf("cancel: %d %s %v", w.Code, w.Body.String(), err)
				}
				wantState, wantCancelled := "cancelled", 2
				if submitted {
					wantState, wantCancelled = "cancelling", 1
				}
				if cancelled.Cancelled == submitted || cancelled.Batch.State != wantState || cancelled.Batch.CancelledCount != wantCancelled {
					t.Fatalf("false cancellation: %+v", cancelled)
				}
				if submitted && cancelled.Message == "" {
					t.Fatal("pending cancellation has no explanation")
				}
				if _, err := f.app.dispatchGenerationJobs(context.Background()); err != nil {
					t.Fatal(err)
				}
				wantPosts := int32(0)
				wantUsed := int64(0)
				if submitted {
					wantPosts, wantUsed = 1, 1
				}
				quota, err := f.app.store.QuickGenerationQuota(context.Background(), f.user.ID)
				if err != nil || f.posts.Load() != wantPosts || quota.Image.TotalUsed != wantUsed {
					t.Fatalf("posts=%d quota=%+v err=%v", f.posts.Load(), quota, err)
				}
			})
		}
	})
	t.Run("HTTP receipt queues once without contacting prompt endpoint", func(t *testing.T) {
		f := newDispatchFixture(t)
		f.app.csrfSigner = security.NewCSRFSigner("dispatch-csrf-test-secret")
		f.form.Set("csrf", f.app.csrfSigner.Token("fixture-session"))
		f.form.Set("client_request_id", newRequestID())
		f.form.Set("correlation_id", newRequestID())
		var jobID string
		for i := 0; i < 2; i++ {
			r := httptest.NewRequest(http.MethodPost, "/generate/run", strings.NewReader(f.form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "fixture-session"})
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &f.user))
			w := httptest.NewRecorder()
			f.app.handleGenerateRun(w, r)
			var receipt map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			if w.Code != 202 || receipt["job_id"] == nil || receipt["prompt_id"] != nil || receipt["dispatch_waiting"] != true {
				t.Fatalf("receipt: %d %s", w.Code, w.Body.String())
			}
			if i == 0 {
				jobID = fmt.Sprint(receipt["job_id"])
			} else if jobID != receipt["job_id"] {
				t.Fatal("duplicate HTTP submission created another job")
			}
		}
		if f.posts.Load() != 0 {
			t.Fatal("HTTP handler sent a prompt")
		}
		quota, err := f.app.store.QuickGenerationQuota(context.Background(), f.user.ID)
		if err != nil || quota.Image.TotalUsed != 1 {
			t.Fatalf("quota: %+v %v", quota, err)
		}
		if _, err = f.app.dispatchGenerationJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.posts.Load() != 1 {
			t.Fatal("worker did not submit exactly once")
		}
	})
	t.Run("local queue admission waits without failing the job", func(t *testing.T) {
		f := newDispatchFixture(t)
		job := f.enqueue(t)
		f.app.comfyPromptLimiter = security.NewLoginLimiter(time.Minute, 1)
		f.app.comfyPromptLimiter.RecordFailure(fmt.Sprint(f.user.ID))
		if _, err := f.app.dispatchGenerationJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, err := f.app.store.GenerationJobByID(context.Background(), job.ID)
		if err != nil || got.State.Terminal() || got.SubmissionStartedAt != nil || got.QuotaReservedOn == nil || got.DispatchToken != "" || f.posts.Load() != 0 {
			t.Fatalf("capacity wait: %+v %v", got, err)
		}
	})
	t.Run("priority head start is bounded by aging", func(t *testing.T) {
		for _, minutes := range []int{5, 11} {
			f := newDispatchFixture(t)
			ordinary := f.enqueue(t)
			var priorityUserID int64
			if err := f.db.QueryRow(`INSERT INTO users(username,password_hash,role,can_use_quick_generation,queue_priority) VALUES('priority-owner','disabled','admin',true,true) RETURNING id`).Scan(&priorityUserID); err != nil {
				t.Fatal(err)
			}
			var err error
			f.user, err = f.app.store.UserByID(context.Background(), priorityUserID)
			if err != nil {
				t.Fatal(err)
			}
			priority := f.enqueue(t)
			if _, err = f.db.Exec(`UPDATE generation_jobs SET created_at=now()-($2::bigint*interval '1 minute') WHERE id=$1`, ordinary.ID, minutes); err != nil {
				t.Fatal(err)
			}
			got, err := f.app.store.ClaimNextGenerationDispatch(context.Background(), "priority-check", 2)
			want := priority.ID
			if minutes > 10 {
				want = ordinary.ID
			}
			if err != nil || got.ID != want {
				t.Fatalf("age=%d got=%d want=%d err=%v", minutes, got.ID, want, err)
			}
		}
	})
	t.Run("resource release and send marker are mutually exclusive", func(t *testing.T) {
		f := newDispatchFixture(t)
		for i := 0; i < 20; i++ {
			job := f.enqueue(t)
			claimed, err := f.app.store.ClaimNextGenerationDispatch(context.Background(), fmt.Sprint("race-", i), 2)
			if err != nil {
				t.Fatal(err)
			}
			if claimed.ID != job.ID {
				t.Fatal("wrong claim")
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			var released bool
			var releaseErr, sendErr error
			go func() {
				defer wg.Done()
				<-start
				released, releaseErr = f.app.store.FenceGenerationJobResourceRelease(context.Background(), job.ID, claimed.DispatchToken)
			}()
			go func() {
				defer wg.Done()
				<-start
				sendErr = f.app.store.BeginGenerationJobSubmission(context.Background(), job.ID, claimed.DispatchToken)
			}()
			close(start)
			wg.Wait()
			if releaseErr != nil || (sendErr != nil && !errors.Is(sendErr, store.ErrGenerationJobStateConflict)) {
				t.Fatalf("release=%v send=%v", releaseErr, sendErr)
			}
			if released == (sendErr == nil) {
				t.Fatalf("release=%v send=%v", released, sendErr)
			}
			if sendErr == nil {
				if err = f.app.store.RejectGenerationJobSubmission(context.Background(), job.ID, claimed.DispatchToken); err != nil {
					t.Fatal(err)
				}
			}
			f.app.failGenerationJob(context.Background(), claimed, "fixture_finished", "Fixture complete", nil)
		}
	})
	t.Run("server launches without original HTTP request", func(t *testing.T) {
		f := newDispatchFixture(t)
		job := f.enqueue(t)
		if f.posts.Load() != 0 {
			t.Fatal("enqueue launched GPU work")
		}
		if _, err := f.app.dispatchGenerationJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, err := f.app.store.GenerationJobByID(context.Background(), job.ID)
		if err != nil || got.PromptID == "" || got.SubmissionStartedAt == nil || got.QuotaCommittedAt == nil || got.State != domain.GenerationJobQueued {
			t.Fatalf("receipt: %+v %v", got, err)
		}
		if _, err := f.app.dispatchGenerationJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.posts.Load() != 1 {
			t.Fatalf("posts=%d", f.posts.Load())
		}
	})
	t.Run("waiting is not expired after two minutes", func(t *testing.T) {
		f := newDispatchFixture(t)
		job := f.enqueue(t)
		_, err := f.db.Exec(`UPDATE generation_jobs SET state_changed_at=now()-interval '10 minutes' WHERE id=$1`, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.app.refreshTrackedGenerationStatuses(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, _ := f.app.store.GenerationJobByID(context.Background(), job.ID)
		if got.State != domain.GenerationJobWaitingForResources || got.ResourcesReleasedAt != nil {
			t.Fatalf("waiting expired: %+v", got)
		}
	})
	t.Run("unknown receipt retains quota and survives restart", func(t *testing.T) {
		f := newDispatchFixture(t)
		job := f.enqueue(t)
		f.status = 504
		f.body = `{"error":"response lost"}`
		if _, err := f.app.dispatchGenerationJobs(context.Background()); err == nil {
			t.Fatal("expected uncertain send")
		}
		got, _ := f.app.store.GenerationJobByID(context.Background(), job.ID)
		if got.State.Terminal() || got.ResourcesReleasedAt != nil || got.QuotaReservedOn == nil || got.SubmissionStartedAt == nil {
			t.Fatalf("released unknown: %+v", got)
		}
		_, err := f.db.Exec(`UPDATE generation_jobs SET dispatch_until=now()-interval '10 minutes',state_changed_at=now()-interval '10 minutes' WHERE id=$1`, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.app = &App{store: store.New(f.db), contentCipher: f.app.contentCipher, cfg: f.app.cfg}
		if _, err = f.app.dispatchGenerationJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err = f.app.refreshTrackedGenerationStatuses(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, _ = f.app.store.GenerationJobByID(context.Background(), job.ID)
		if got.State.Terminal() || f.posts.Load() != 1 {
			t.Fatalf("redispatched/expired: %+v posts=%d", got, f.posts.Load())
		}
		f.accepted.Store(true)
		if _, err = f.app.refreshTrackedGenerationStatuses(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, _ = f.app.store.GenerationJobByID(context.Background(), job.ID)
		if got.PromptID == "" || got.QuotaCommittedAt == nil || f.posts.Load() != 1 {
			t.Fatalf("not recovered: %+v", got)
		}
	})
	t.Run("validation rejection releases quota", func(t *testing.T) {
		f := newDispatchFixture(t)
		job := f.enqueue(t)
		f.status = 400
		f.body = `{"error":{"type":"prompt_outputs_failed_validation"},"node_errors":{}}`
		if _, err := f.app.dispatchGenerationJobs(context.Background()); !errors.Is(err, errComfyPromptRejected) {
			t.Fatal(err)
		}
		got, _ := f.app.store.GenerationJobByID(context.Background(), job.ID)
		if got.State != domain.GenerationJobFailed || got.ResourcesReleasedAt == nil || got.SubmissionRejectedAt == nil || got.QuotaReservedOn != nil {
			t.Fatalf("rejection: %+v", got)
		}
	})
	t.Run("cancel before dispatch never submits", func(t *testing.T) {
		f := newDispatchFixture(t)
		job := f.enqueue(t)
		job, _, err := f.app.store.RequestGenerationJobCancellation(context.Background(), job.ID, f.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, cancelled, err := f.app.continueGenerationJobCancellation(context.Background(), job)
		if err != nil || !cancelled || got.State != domain.GenerationJobCancelled {
			t.Fatalf("cancel: %+v %v", got, err)
		}
		if _, err = f.app.dispatchGenerationJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.posts.Load() != 0 {
			t.Fatal("cancelled job submitted")
		}
	})
	t.Run("cancel cannot confirm unknown submission", func(t *testing.T) {
		f := newDispatchFixture(t)
		job := f.enqueue(t)
		f.status = 504
		f.body = `{}`
		_, _ = f.app.dispatchGenerationJobs(context.Background())
		job, _, err := f.app.store.RequestGenerationJobCancellation(context.Background(), job.ID, f.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, cancelled, err := f.app.continueGenerationJobCancellation(context.Background(), job)
		if err != nil || cancelled || got.CancellationConfirmedAt != nil || got.ResourcesReleasedAt != nil {
			t.Fatalf("false cancel: %+v %v", got, err)
		}
	})
	t.Run("one claim and stale pre-dispatch token fenced", func(t *testing.T) {
		f := newDispatchFixture(t)
		job := f.enqueue(t)
		var wins atomic.Int32
		var wg sync.WaitGroup
		var token atomic.Value
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				key := fmt.Sprint("claim-", i)
				_, err := f.app.store.ClaimNextGenerationDispatch(context.Background(), key, 2)
				if err == nil {
					wins.Add(1)
					token.Store(key)
				} else if !errors.Is(err, sql.ErrNoRows) {
					t.Error(err)
				}
			}(i)
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("claims=%d", wins.Load())
		}
		_, err := f.db.Exec(`UPDATE generation_jobs SET dispatch_until=now()-interval '1 second' WHERE id=$1`, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.app.store.ClaimNextGenerationDispatch(context.Background(), "replacement", 2); err != nil {
			t.Fatal(err)
		}
		if err = f.app.store.BeginGenerationJobSubmission(context.Background(), job.ID, token.Load().(string)); !errors.Is(err, store.ErrGenerationJobStateConflict) {
			t.Fatalf("stale marker: %v", err)
		}
		if err = f.app.store.BeginGenerationJobSubmission(context.Background(), job.ID, "replacement"); err != nil {
			t.Fatal(err)
		}
		if err = f.app.store.BeginGenerationJobSubmission(context.Background(), job.ID, "replacement"); !errors.Is(err, store.ErrGenerationJobStateConflict) {
			t.Fatalf("duplicate marker: %v", err)
		}
	})
}
