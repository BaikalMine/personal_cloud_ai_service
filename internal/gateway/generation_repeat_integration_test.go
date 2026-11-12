package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

type generationRepeatFixture struct {
	*dispatchFixture
	source domain.GenerationJob
	files  map[string]string
}

func newGenerationRepeatFixture(t *testing.T, observe bool) *generationRepeatFixture {
	t.Helper()
	f := gpuDispatchFixture(t)
	ctx := context.Background()
	f.app.csrfSigner = security.NewCSRFSigner("generation-repeat-fixture")
	source := f.enqueue(t)
	fixture := &generationRepeatFixture{dispatchFixture: f, source: source, files: map[string]string{}}
	if observe {
		input, err := parseGenerationValues(ctx, f.form)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := f.app.prepareGeneration(ctx, &f.user, input, false)
		if err != nil {
			t.Fatal(err)
		}
		request, err := generationComponentRequest(source.PublicID, prepared.Prompt)
		if err != nil {
			t.Fatal(err)
		}
		roots := map[string][]string{}
		for _, ref := range request.References {
			if roots[ref.Kind] == nil {
				roots[ref.Kind] = []string{t.TempDir()}
			}
			filename := filepath.Join(roots[ref.Kind][0], filepath.FromSlash(ref.Name))
			if err = os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filename, []byte("original generation model bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			fixture.files[ref.Key()] = filename
		}
		inspector, err := componentinspection.New(roots)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(inspector.Close)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
				t.Error("missing observer credential")
			}
			if r.URL.Path == "/v1/activity" {
				_ = json.NewEncoder(w).Encode(loratraining.Activity{ProtocolVersion: 1, State: "idle"})
				return
			}
			var request componentinspection.Request
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				w.WriteHeader(400)
				return
			}
			result, err := inspector.Inspect(request)
			if err != nil {
				w.WriteHeader(503)
				return
			}
			_ = json.NewEncoder(w).Encode(result)
		}))
		t.Cleanup(server.Close)
		base, _ := url.Parse(server.URL)
		f.app.loraTraining = loratraining.NewClient(base, strings.Repeat("t", 32))
		f.app.cfg.GenerationComponentInspectionEnabled = true
		for deadline := time.Now().Add(5 * time.Second); ; {
			result, err := inspector.Inspect(request)
			if err != nil {
				t.Fatal(err)
			}
			if result.State == "complete" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("observer timeout")
			}
			time.Sleep(time.Millisecond)
		}
	}
	if _, err := f.app.dispatchGenerationJobs(ctx); err != nil || f.posts.Load() != 1 {
		t.Fatal("source dispatch failed", err)
	}
	if _, err := f.db.Exec(`UPDATE generation_jobs SET state='completed',finished_at=now() WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.app.gpuCoordinator.release(ctx, domain.GPUWorkGeneration, source.PublicID, "executor_terminal"); err != nil {
		t.Fatal(err)
	}
	var err error
	fixture.source, err = f.app.store.GenerationJobByID(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.body = `{"prompt_id":"abcdef9876543210"}`
	return fixture
}

func (f *generationRepeatFixture) call(t *testing.T, endpoint string, values url.Values, user *User, want int) *httptest.ResponseRecorder {
	t.Helper()
	method := "POST"
	if strings.HasPrefix(endpoint, "/generate/jobs/repeat/status") {
		method = "GET"
	}
	r := httptest.NewRequest(method, endpoint, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "repeat-session"})
	r = r.WithContext(context.WithValue(r.Context(), userCtxKey, user))
	w := httptest.NewRecorder()
	if method == "GET" {
		f.app.handleGenerationRepeatStatus(w, r)
	} else {
		f.app.handleGenerationRepeat(w, r)
	}
	if w.Code != want {
		t.Fatalf("%s: status %d want %d: %s", endpoint, w.Code, want, w.Body.String())
	}
	return w
}

func (f *generationRepeatFixture) values(id string) url.Values {
	return url.Values{"csrf": {f.app.csrfSigner.Token("repeat-session")}, "source_job_id": {f.source.PublicID}, "client_request_id": {id}}
}

func (f *generationRepeatFixture) check(t *testing.T, values url.Values) generationRepeatView {
	t.Helper()
	var view generationRepeatView
	for deadline := time.Now().Add(5 * time.Second); ; {
		w := f.call(t, "/generate/jobs/repeat/check", values, &f.user, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.State != "pending" {
			return view
		}
		if time.Now().After(deadline) {
			t.Fatal("repeat remained pending", w.Body.String())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGenerationRepeatAPIIntegration(t *testing.T) {
	f := newGenerationRepeatFixture(t, true)
	ctx := context.Background()
	values := f.values("generation-repeat-request-0001")
	f.call(t, "/generate/jobs/repeat/check", values, nil, 403)
	bad := cloneGenerationValues(values)
	bad.Set("csrf", "wrong")
	f.call(t, "/generate/jobs/repeat/check", bad, &f.user, 403)
	bad = cloneGenerationValues(values)
	bad.Set("prompt", "client cannot supply a graph")
	f.call(t, "/generate/jobs/repeat/check", bad, &f.user, 400)
	var foreignID int64
	if err := f.db.QueryRow(`INSERT INTO users(username,password_hash,role,can_use_quick_generation) VALUES('repeat-foreign','disabled','admin',true) RETURNING id`).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}
	foreign, err := f.app.store.UserByID(ctx, foreignID)
	if err != nil {
		t.Fatal(err)
	}
	f.call(t, "/generate/jobs/repeat/check", values, &foreign, 404)
	view := f.check(t, values)
	if view.State != "ready" || view.Confirmation == "" || view.Seed != "42" || !view.FilesObserved || len(view.Differences) != 0 {
		t.Fatalf("unexpected check: %+v", view)
	}
	if f.posts.Load() != 1 {
		t.Fatal("checking launched generation")
	}
	values.Set("confirmation", view.Confirmation)
	bad = cloneGenerationValues(values)
	bad.Set("confirmation", view.Confirmation[:len(view.Confirmation)/2])
	f.call(t, "/generate/jobs/repeat/submit", bad, &f.user, 409)
	w := f.call(t, "/generate/jobs/repeat/submit", values, &f.user, 200)
	var accepted struct {
		Accepted bool   `json:"accepted"`
		JobID    string `json:"job_id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || !accepted.Accepted || accepted.JobID == "" {
		t.Fatal(w.Body.String(), err)
	}
	child, err := f.app.store.GenerationJobByPublicID(ctx, f.user.ID, accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if child.QuotaReservedOn == nil || child.DispatchQueuedAt == nil || child.ParentJobID == nil || *child.ParentJobID != f.source.ID || f.posts.Load() != 1 {
		t.Fatal("confirmation sent or lost prepared work", child)
	}
	f.call(t, "/generate/jobs/repeat/submit", values, &f.user, 200)
	// Source deletion cannot erase the accepted identity or silently select a new source.
	if _, err = f.db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, f.source.ID); err != nil {
		t.Fatal(err)
	}
	f.call(t, "/generate/jobs/repeat/submit", values, &f.user, 200)
	if _, err = f.app.dispatchGenerationJobs(ctx); err != nil || f.posts.Load() != 2 {
		t.Fatal("repeat did not dispatch exactly once", err)
	}
	record, err := f.app.store.GenerationExecutionForJob(ctx, f.user.ID, child.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.app.decodeGenerationExecution(child, record)
	if err != nil || snapshot.Repeat == nil || snapshot.ParentJobID != f.source.PublicID || snapshot.Seed != "42" {
		t.Fatal("lost checked lineage", snapshot, err)
	}
	if string(snapshot.Graph) != string(f.graph.Load().(json.RawMessage)) {
		t.Fatal("saved repeat graph differs from actual send")
	}
	if _, err = f.app.dispatchGenerationJobs(ctx); err != nil || f.posts.Load() != 2 {
		t.Fatal("repeat replayed", err)
	}
	if _, err = f.db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, child.ID); err != nil {
		t.Fatal(err)
	}
	w = f.call(t, "/generate/jobs/repeat/submit", values, &f.user, 200)
	if !strings.Contains(w.Body.String(), `"removed":true`) {
		t.Fatal("deleted child lost receipt", w.Body.String())
	}
	f.call(t, "/generate/jobs/repeat/status?request_id="+values.Get("client_request_id"), nil, &f.user, 200)
	var count int
	if err = f.db.QueryRow(`SELECT count(*) FROM generation_repeats`).Scan(&count); err != nil || count != 1 || f.posts.Load() != 2 {
		t.Fatal("duplicate repeat receipt", count, err)
	}
}

func TestGenerationRepeatDriftAndLegacyIntegration(t *testing.T) {
	for _, stage := range []string{"before-check", "before-submit", "before-dispatch", "missing", "legacy", "expiry"} {
		t.Run(stage, func(t *testing.T) {
			f := newGenerationRepeatFixture(t, true)
			values := f.values("generation-repeat-drift-0001")
			filename := ""
			for _, name := range f.files {
				filename = name
				break
			}
			change := func() {
				t.Helper()
				if err := os.WriteFile(filename, []byte("different replacement model data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "before-check" {
				change()
			}
			if stage == "legacy" {
				if _, err := f.db.Exec(`DELETE FROM generation_job_executions WHERE job_id=$1`, f.source.ID); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "missing" {
				if err := os.Remove(filename); err != nil {
					t.Fatal(err)
				}
			}
			view := f.check(t, values)
			if stage == "missing" {
				if view.State != "blocked" || view.Confirmation != "" || !strings.Contains(view.Message, "не найден") {
					t.Fatal(view)
				}
				return
			}
			if stage == "before-check" && (view.State != "changed" || len(view.Differences) == 0) {
				t.Fatal("replacement not explained", view)
			}
			if stage == "legacy" && (view.Recorded || view.State != "unverified" || view.Confirmation == "") {
				t.Fatal("legacy invented evidence", view)
			}
			values.Set("confirmation", view.Confirmation)
			if stage == "before-submit" {
				change()
				f.call(t, "/generate/jobs/repeat/submit", values, &f.user, 409)
				if f.posts.Load() != 1 {
					t.Fatal("changed confirmation sent")
				}
				return
			}
			if stage == "expiry" {
				token, err := f.app.decodeGenerationRepeatConfirmation(view.Confirmation)
				if err != nil {
					t.Fatal(err)
				}
				token.ExpiresAt = time.Now().Add(-time.Minute).Unix()
				expired, _ := f.app.encodeGenerationRepeatConfirmation(token)
				values.Set("confirmation", expired)
				f.call(t, "/generate/jobs/repeat/submit", values, &f.user, 409)
				return
			}
			f.call(t, "/generate/jobs/repeat/submit", values, &f.user, 200)
			if stage == "before-dispatch" {
				change()
				receipt, err := f.app.store.GenerationRepeatByRequest(context.Background(), f.user.ID, values.Get("client_request_id"))
				if err != nil {
					t.Fatal(err)
				}
				for deadline := time.Now().Add(5 * time.Second); ; {
					if _, err = f.db.Exec(`UPDATE generation_jobs SET dispatch_until=now()-interval '1 second' WHERE id=$1`, *receipt.JobID); err != nil {
						t.Fatal(err)
					}
					if _, err = f.app.dispatchGenerationJobs(context.Background()); err != nil {
						t.Fatal(err)
					}
					job, err := f.app.store.GenerationJobByID(context.Background(), *receipt.JobID)
					if err != nil {
						t.Fatal(err)
					}
					if job.State.Terminal() {
						if job.State != domain.GenerationJobFailed || job.QuotaReservedOn != nil || job.SubmissionStartedAt != nil || f.posts.Load() != 1 {
							t.Fatal("drift sent or kept quota", job)
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("drift did not settle")
					}
					time.Sleep(time.Millisecond)
				}
			}
		})
	}
}

func TestGenerationRepeatDiscardIntegration(t *testing.T) {
	f := newGenerationRepeatFixture(t, false)
	values := f.values("generation-repeat-discard-001")
	view := f.check(t, values)
	w := f.call(t, "/generate/jobs/repeat/discard", values, &f.user, 200)
	if !strings.Contains(w.Body.String(), `"cancelled":true`) || !strings.Contains(w.Body.String(), `"accepted":false`) {
		t.Fatal(w.Body.String())
	}
	values.Set("confirmation", view.Confirmation)
	w = f.call(t, "/generate/jobs/repeat/submit", values, &f.user, 200)
	if !strings.Contains(w.Body.String(), `"cancelled":true`) {
		t.Fatal("late request bypassed fence", w.Body.String())
	}
	values.Del("confirmation")
	f.call(t, "/generate/jobs/repeat/check", values, &f.user, 200)
	f.call(t, "/generate/jobs/repeat/discard", values, &f.user, 200)
	f.call(t, "/generate/jobs/repeat/status?request_id="+values.Get("client_request_id"), nil, &f.user, 200)
	values.Set("source_job_id", "different-generation-source-0001")
	f.call(t, "/generate/jobs/repeat/discard", values, &f.user, 409)
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM generation_jobs`).Scan(&count); err != nil || count != 1 || f.posts.Load() != 1 {
		t.Fatal("fenced request created work", count, err)
	}
}

func repeatStoreParams(t *testing.T, f *generationRepeatFixture, requestID string) domain.CreateGenerationRepeatParams {
	t.Helper()
	p, err := f.app.prepareGenerationRepeat(context.Background(), &f.user, f.source.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	input := repeatExecutionInput(p.Prepared.Input)
	payload, err := json.Marshal(generationSavedPayload{Version: 1, Values: p.Payload.Values, ExecutionInput: &input, Repeat: &p.Constraint})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := f.app.contentCipher.Encrypt(string(payload))
	if err != nil {
		t.Fatal(err)
	}
	return domain.CreateGenerationRepeatParams{
		Job:            domain.CreateGenerationJobParams{PublicID: newRequestID(), UserID: f.user.ID, UsernameSnapshot: f.user.Username, RequestID: requestID},
		Prepared:       domain.PreparedGenerationJob{TemplateID: input.TemplateID, WorkflowID: input.PresetID, ModelName: input.ModelName, Seed: input.Seed, PayloadCipher: cipher, Dependencies: generationJobDependencies(input, &f.user), InputCount: generationJobInputCount(input)},
		SourcePublicID: f.source.PublicID, SourcePayloadCipher: f.source.PayloadCipher, SourceExecutionSHA256: p.Constraint.SourceExecutionSHA256, ConfirmationSHA256: p.Constraint.digest(),
	}
}

func TestGenerationRepeatConcurrentReceiptIntegration(t *testing.T) {
	for _, discard := range []bool{false, true} {
		t.Run(map[bool]string{false: "duplicate-submit", true: "discard-versus-submit"}[discard], func(t *testing.T) {
			f := newGenerationRepeatFixture(t, false)
			ctx := context.Background()
			params := repeatStoreParams(t, f, "generation-repeat-race-0001")
			var group sync.WaitGroup
			errs := make(chan error, 12)
			for i := 0; i < 12; i++ {
				group.Add(1)
				go func(i int) {
					defer group.Done()
					if discard && i%2 == 0 {
						_, err := f.app.store.DiscardGenerationRepeatRequest(ctx, f.user.ID, params.Job.RequestID, f.source.PublicID)
						errs <- err
					} else {
						p := params
						p.Job.PublicID = newRequestID()
						_, _, err := f.app.store.CreateGenerationRepeat(ctx, p)
						errs <- err
					}
				}(i)
			}
			group.Wait()
			close(errs)
			receipt, err := f.app.store.GenerationRepeatByRequest(ctx, f.user.ID, params.Job.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			for err := range errs {
				if err != nil && !(receipt.Cancelled && errors.Is(err, store.ErrGenerationRepeatConflict)) {
					t.Fatal(err)
				}
			}
			var count int
			if err = f.db.QueryRow(`SELECT count(*) FROM generation_jobs WHERE request_id=$1`, params.Job.RequestID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if receipt.Cancelled && count != 0 || !receipt.Cancelled && count != 1 {
				t.Fatal("non-atomic receipt", receipt, count)
			}
			if !receipt.Cancelled {
				f.call(t, "/generate/jobs/repeat/status?request_id="+params.Job.RequestID, nil, &f.user, 200)
				job, err := f.app.store.GenerationJobByID(ctx, *receipt.JobID)
				if err != nil || job.DispatchQueuedAt == nil || job.QuotaReservedOn == nil {
					t.Fatal("publication not recovered", job, err)
				}
				w := f.call(t, "/generate/jobs/repeat/discard", f.values(params.Job.RequestID), &f.user, 200)
				if !strings.Contains(w.Body.String(), `"accepted":true`) {
					t.Fatal("accepted job called cancelled", w.Body.String())
				}
			}
			if f.posts.Load() != 1 {
				t.Fatal("receipt recovery bypassed dispatcher")
			}
		})
	}
}

func TestGenerationRepeatAccessRevokedIntegration(t *testing.T) {
	f := newGenerationRepeatFixture(t, false)
	values := f.values("generation-repeat-access-0001")
	view := f.check(t, values)
	values.Set("confirmation", view.Confirmation)
	if _, err := f.db.Exec(`UPDATE users SET can_use_quick_generation=false WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	f.call(t, "/generate/jobs/repeat/submit", values, &f.user, 403)
	params := domain.CreateGenerationRepeatParams{Job: domain.CreateGenerationJobParams{PublicID: newRequestID(), UserID: f.user.ID, RequestID: values.Get("client_request_id")}, Prepared: domain.PreparedGenerationJob{PayloadCipher: []byte("test")}, SourcePublicID: f.source.PublicID, ConfirmationSHA256: strings.Repeat("a", 64)}
	if _, _, err := f.app.store.CreateGenerationRepeat(context.Background(), params); !errors.Is(err, store.ErrQuickGenerationForbidden) {
		t.Fatal("stale user bypassed store access", err)
	}
	if f.posts.Load() != 1 {
		t.Fatal("revoked access sent work")
	}
}

func TestGenerationRepeatInputAvailabilityIntegration(t *testing.T) {
	f := newGenerationRepeatFixture(t, false)
	ctx := context.Background()
	folder := comfyUploadNamespace(f.app.comfyClientID(f.user.ID))
	const id = "1234567890abcdef1234567890abcdef"
	hash := strings.Repeat("a", 64)
	if err := f.app.store.ReserveComfyInputAsset(ctx, f.user.ID, id, "source.png", folder, 12, hash, store.ComfyInputQuota{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.app.store.FinalizeComfyInputAsset(ctx, id, "source.png", folder, 12, hash, time.Hour); err != nil || !ok {
		t.Fatal(ok, err)
	}
	var status atomic.Int32
	status.Store(206)
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.URL.Path != "/view" || r.URL.Query().Get("filename") != "source.png" || r.URL.Query().Get("subfolder") != folder || r.Header.Get("Range") != "bytes=0-0" {
			t.Error("wrong availability request", r.URL, r.Header)
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	f.app.cfg.ComfyUIUpstream, _ = url.Parse(server.URL)
	input := generationForm{TemplateID: "image-to-image", PresetID: "photoflow-krea2-edit", InputImage: folder + "/source.png"}
	assets, err := f.app.generationRepeatAssets(ctx, &f.user, input)
	if err != nil || len(assets) != 1 || assets[0].ID != id {
		t.Fatal("owned retained input unavailable", assets, err)
	}
	status.Store(503)
	if _, err = f.app.generationRepeatAssets(ctx, &f.user, input); !errors.Is(err, errGenerationRepeatWaiting) {
		t.Fatal("uncertain storage not pending", err)
	}
	status.Store(404)
	if _, err = f.app.generationRepeatAssets(ctx, &f.user, input); err == nil || errors.Is(err, errGenerationRepeatWaiting) {
		t.Fatal("missing input not blocked", err)
	}
	status.Store(206)
	if _, err = f.db.Exec(`UPDATE comfy_input_assets SET expires_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	before := reads.Load()
	if _, err = f.app.generationRepeatAssets(ctx, &f.user, input); err == nil {
		t.Fatal("expired input accepted")
	}
	if reads.Load() != before {
		t.Fatal("expired input fetched")
	}
	input.InputImage = "gateway/foreign/source.png"
	if _, err = f.app.generationRepeatAssets(ctx, &f.user, input); err == nil || reads.Load() != before {
		t.Fatal("foreign input fetched", err)
	}
}
