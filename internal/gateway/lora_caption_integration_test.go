package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestLoraCaptionJobsIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	var owner, foreign int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,can_train_image_lora) VALUES('caption-owner','disabled','admin',true) RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,can_train_image_lora) VALUES('caption-foreign','disabled','user',true) RETURNING id`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	cipher, err := contentcrypto.NewCipher("caption-integration-secret-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	var mode atomic.Int32
	var requests, active, maximum atomic.Int32
	entered := make(chan struct{}, 1)
	instruction, _ := promptassistant.LoraCaptionInstructionSnapshot()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if n <= old || maximum.CompareAndSwap(old, n) {
				break
			}
		}
		requests.Add(1)
		var body struct {
			Messages []struct {
				Role    string   `json:"role"`
				Content string   `json:"content"`
				Images  []string `json:"images"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if len(body.Messages) != 2 || body.Messages[0].Content != instruction || len(body.Messages[1].Images) != 1 {
			t.Error("caption request lost its immutable instruction or combined images")
		}
		if mode.CompareAndSwap(1, 0) {
			http.Error(w, "temporary", 504)
			return
		}
		if mode.Load() == 2 {
			entered <- struct{}{}
			<-r.Context().Done()
			return
		}
		writeJSON(w, 200, map[string]any{"message": map[string]string{"content": `{"caption":"person_x, person_x, portrait with light hair in daylight"}`}})
	}))
	defer model.Close()
	base, _ := url.Parse(model.URL)
	repository := store.New(db)
	newApp := func() *App {
		return &App{cfg: Config{MediaSpoolDir: t.TempDir(), MediaInFlightLimitBytes: 256 << 20}, store: repository, contentCipher: cipher, csrfSigner: security.NewCSRFSigner("caption-csrf"), promptAssistant: promptassistant.NewClient(base, "test:text").WithVisionModel("test:vision", time.Minute, "0"), promptAssistantSlots: make(chan struct{}, 1)}
	}
	app := newApp()
	asset, err := app.persistLoraDatasetImage(ctx, owner, "portrait.png", bytes.NewReader(datasetTestPNG(t, 768, 768)))
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.LoraDatasetManifest{Version: 1, Settings: domain.LoraDatasetSettings{Name: "Caption series", TriggerWord: "person_x", ConceptType: "character"}}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		manifest.Images = append(manifest.Images, domain.LoraDatasetImage{ID: id, AssetID: asset.ID, CaptionRevision: "original"})
	}
	emptyCipher, _, err := app.encryptLoraDatasetManifest(domain.LoraDatasetManifest{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	row, err := repository.CreateLoraDataset(ctx, owner, newRequestID(), "Caption series", emptyCipher)
	if err != nil {
		t.Fatal(err)
	}
	manifestCipher, _, err := app.encryptLoraDatasetManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	row, err = repository.SaveLoraDataset(ctx, owner, row.ID, row.Revision, row.Name, manifestCipher, []string{asset.ID, asset.ID, asset.ID, asset.ID, asset.ID})
	if err != nil {
		t.Fatal(err)
	}
	const session = "caption-session"
	csrf := app.csrfSigner.Token(session)
	call := func(user int64, method, target string, body any, token string, want int) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, target, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", token)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: user, Role: "user", CanTrainImageLora: true}))
		w := httptest.NewRecorder()
		if strings.HasPrefix(target, "/api/lora-datasets") {
			app.handleLoraDatasets(w, r)
		} else {
			app.handleLoraTrainingCaptionStatus(w, r)
		}
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, target, w.Code, want, w.Body.String())
		}
		return w
	}
	endpoint := "/api/lora-datasets/" + row.ID + "/captions"
	enqueue := func(ids []string) []loraCaptionJobView {
		t.Helper()
		response := call(owner, "POST", endpoint, map[string]any{"revision": row.Revision, "image_ids": ids, "only_empty": true}, csrf, 202)
		var data struct {
			Jobs []loraCaptionJobView `json:"jobs"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data.Jobs
	}
	jobs := enqueue([]string{"a", "b"})
	if len(jobs) != 2 {
		t.Fatal("series not persisted")
	}
	duplicates := enqueue([]string{"a", "b"})
	if duplicates[0].ID != jobs[0].ID {
		t.Fatal("repeat POST created competing jobs")
	}
	call(foreign, "GET", endpoint, nil, "", 404)
	call(foreign, "GET", "/api/lora-training/caption/"+jobs[0].ID, nil, "", 404)
	call(owner, "POST", "/api/lora-training/caption/"+jobs[0].ID+"/cancel", nil, "", 403)
	call(owner, "POST", endpoint, map[string]any{"revision": row.Revision - 1, "image_ids": []string{"c"}}, csrf, 409)
	stored, err := repository.LoraCaptionJob(ctx, owner, jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored.InputCipher, []byte("person_x")) {
		t.Fatal("caption instructions saved in plaintext")
	}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copy := stored
			copy.ID = newRequestID()
			rows, e := repository.EnqueueLoraCaptions(ctx, owner, row.ID, row.Revision, []domain.LoraCaptionJob{copy})
			if e != nil {
				errs <- e
			} else {
				ids <- rows[0].ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for id := range ids {
		if id != stored.ID {
			t.Fatal("concurrent caption enqueue duplicated a record")
		}
	}
	// Simulate a stopped process: its DB session has released the worker lock,
	// but its running job remains. A new App has no in-memory job registry.
	var abandoned domain.LoraCaptionJob
	_, err = repository.WithLoraCaptionWorker(ctx, func(*sql.Conn) error {
		var e error
		abandoned, e = repository.ClaimLoraCaptionJob(ctx, "old-worker-token")
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	app = newApp()
	if count, err := app.refreshLoraCaptionJobs(ctx); err != nil || count != 1 {
		t.Fatalf("restart recovery: %d %v", count, err)
	}
	completed, err := repository.LoraCaptionJob(ctx, owner, abandoned.ID)
	if err != nil || completed.State != "completed" {
		t.Fatalf("recovered result: %+v %v", completed, err)
	}
	view, err := app.loraCaptionJobView(completed)
	if err != nil || view.Caption != "person_x, portrait with light hair in daylight" {
		t.Fatalf("trigger or persisted result: %+v %v", view, err)
	}
	if bytes.Contains(completed.ResultCipher, []byte("portrait")) {
		t.Fatal("result saved in plaintext")
	}
	if accepted, err := repository.FinishLoraCaptionJob(ctx, abandoned.ID, "old-worker-token", "completed", "stale", []byte("stale"), 0); err != nil || accepted {
		t.Fatalf("stale worker overwrote result: %v %v", accepted, err)
	}
	mode.Store(1)
	if _, err := app.refreshLoraCaptionJobs(ctx); err != nil {
		t.Fatal(err)
	}
	secondID := jobs[0].ID
	if secondID == abandoned.ID {
		secondID = jobs[1].ID
	}
	second, err := repository.LoraCaptionJob(ctx, owner, secondID)
	if err != nil || second.State != "queued" || second.Attempts != 1 {
		t.Fatalf("504 retry: %+v %v", second, err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE lora_caption_jobs SET available_at=now() WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	app = newApp()
	if _, err = app.refreshLoraCaptionJobs(ctx); err != nil {
		t.Fatal(err)
	}
	second, err = repository.LoraCaptionJob(ctx, owner, second.ID)
	if err != nil || second.State != "completed" || second.Attempts != 2 {
		t.Fatalf("504 recovery: %+v %v", second, err)
	}
	third := enqueue([]string{"c"})[0]
	mode.Store(2)
	finished := make(chan error, 1)
	go func() { _, err := app.refreshLoraCaptionJobs(ctx); finished <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("model request did not begin")
	}
	other := newApp()
	if count, err := other.refreshLoraCaptionJobs(ctx); err != nil || count != 0 {
		t.Fatalf("second worker entered a live model call: %d %v", count, err)
	}
	call(owner, "POST", "/api/lora-training/caption/"+third.ID+"/cancel", nil, csrf, 200)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("cancel did not stop worker")
	}
	thirdRow, err := repository.LoraCaptionJob(ctx, owner, third.ID)
	if err != nil || thirdRow.State != "cancelled" || len(thirdRow.ResultCipher) != 0 {
		t.Fatalf("cancel result: %+v %v", thirdRow, err)
	}
	mode.Store(0)
	call(owner, "POST", "/api/lora-training/caption/"+third.ID+"/retry", nil, csrf, 200)
	if _, err = app.refreshLoraCaptionJobs(ctx); err != nil {
		t.Fatal(err)
	}
	thirdRow, err = repository.LoraCaptionJob(ctx, owner, third.ID)
	if err != nil || thirdRow.State != "completed" {
		t.Fatalf("selected retry failed: %+v %v", thirdRow, err)
	}
	queued := enqueue([]string{"d", "e"})
	call(owner, "POST", endpoint, map[string]any{"cancel": true}, csrf, 200)
	for _, job := range queued {
		state, err := repository.LoraCaptionJob(ctx, owner, job.ID)
		if err != nil || state.State != "cancelled" {
			t.Fatalf("series cancel: %+v %v", state, err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("concurrent model requests: %d", maximum.Load())
	}
	if requests.Load() != 5 {
		t.Fatalf("unexpected model request count: %d", requests.Load())
	}
	call(owner, "GET", endpoint, nil, "", 200)
	if _, err = db.ExecContext(ctx, `UPDATE lora_caption_jobs SET expires_at=now()-interval '1 hour' WHERE id=$1`, queued[0].ID); err != nil {
		t.Fatal(err)
	}
	if n, err := repository.CleanupLoraCaptionJobs(ctx); err != nil || n != 1 {
		t.Fatalf("caption cleanup: %d %v", n, err)
	}
	if _, err = repository.LoraCaptionJob(ctx, owner, queued[0].ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("expired job remained")
	}
	for _, job := range []loraCaptionJobView{jobs[0], jobs[1], third} {
		if _, err := repository.LoraCaptionJob(ctx, owner, job.ID); err != nil {
			t.Fatal("unexpired result was deleted")
		}
	}
	if entries, err := os.ReadDir(app.mediaSpoolDir()); err != nil || len(entries) != 0 {
		t.Fatalf("caption files leaked: %v %v", entries, err)
	}
	t.Run("quota is atomic and jobs retain their image until cleanup", func(t *testing.T) {
		changed := manifest.Images[4]
		changed.CaptionRevision = "new-version"
		newJob, err := app.makeLoraCaptionJob(row.ID, changed, asset, "person_x", "character")
		if err != nil {
			t.Fatal(err)
		}
		created, err := repository.EnqueueLoraCaptions(ctx, owner, row.ID, row.Revision, []domain.LoraCaptionJob{newJob})
		if err != nil || len(created) != 1 {
			t.Fatalf("new image version: %v", err)
		}
		if _, err = repository.RetryLoraCaption(ctx, owner, queued[1].ID); !errors.Is(err, store.ErrLoraDatasetConflict) {
			t.Fatalf("old version competed with active job: %v", err)
		}
		bulk := []domain.LoraCaptionJob{}
		for i := range 100 {
			item := domain.LoraDatasetImage{ID: fmt.Sprintf("bulk-%03d", i), AssetID: asset.ID}
			job, err := app.makeLoraCaptionJob(row.ID, item, asset, "person_x", "character")
			if err != nil {
				t.Fatal(err)
			}
			bulk = append(bulk, job)
		}
		if _, err = repository.EnqueueLoraCaptions(ctx, owner, row.ID, row.Revision, bulk); !errors.Is(err, store.ErrLoraCaptionQuota) {
			t.Fatalf("101 queued jobs allowed: %v", err)
		}
		var pending int
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM lora_caption_jobs WHERE state='queued'`).Scan(&pending); err != nil || pending != 1 {
			t.Fatalf("partial series survived quota failure: %d %v", pending, err)
		}
		if _, err = repository.CancelLoraCaptions(ctx, owner, row.ID, ""); err != nil {
			t.Fatal(err)
		}
		if jobs, err := repository.EnqueueLoraCaptions(ctx, owner, row.ID, row.Revision, bulk); err != nil || len(jobs) != 100 {
			t.Fatalf("100-image series: %d %v", len(jobs), err)
		}
		empty := domain.LoraDatasetManifest{Version: 1, Settings: manifest.Settings}
		cipher, _, err := app.encryptLoraDatasetManifest(empty)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repository.SaveLoraDataset(ctx, owner, row.ID, row.Revision, row.Name, cipher, nil); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `UPDATE lora_dataset_assets SET last_used_at=now()-interval '2 days' WHERE id=$1`, asset.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = repository.CleanupLoraDatasets(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = repository.LoraDatasetAsset(ctx, owner, asset.ID); err != nil {
			t.Fatal("queued caption lost its only image reference")
		}
		if _, err = repository.CancelLoraCaptions(ctx, owner, row.ID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `UPDATE lora_caption_jobs SET expires_at=now()-interval '1 hour'`); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err = repository.CleanupLoraCaptionJobs(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = repository.CleanupLoraDatasets(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = repository.LoraDatasetAsset(ctx, owner, asset.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unreferenced caption image remained after cleanup: %v", err)
		}
	})
}
