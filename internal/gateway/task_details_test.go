package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestTaskDetailRequestValidation(t *testing.T) {
	app := &App{}
	for _, tc := range []struct {
		method, query string
		user          *User
		status        int
	}{
		{"POST", "", nil, 405}, {"GET", "", nil, 401},
		{"GET", "?kind=generation&id=a&id=b", &User{ID: 1}, 400},
		{"GET", "?kind=generation&id=a&user_id=2", &User{ID: 1}, 400},
		{"GET", "?kind=generation&id=%00", &User{ID: 1}, 400},
		{"GET", "?kind=generation&id=%FF", &User{ID: 1}, 400},
		{"GET", "?kind=generation&id=a", &User{ID: 1}, 404},
		{"GET", "?kind=lora_training&id=a", &User{ID: 1}, 404},
		{"GET", "?kind=lora_caption&id=a", &User{ID: 1}, 404},
		{"GET", "?kind=generation_batch&id=a", &User{ID: 1}, 404},
	} {
		for name, handler := range map[string]http.HandlerFunc{"detail": app.handleTaskDetail, "gpu": app.handleTaskGPU} {
			r := httptest.NewRequest(tc.method, "/api/tasks/"+name+tc.query, nil)
			if tc.user != nil {
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey, tc.user))
			}
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("%s %s: %d %s", name, tc.query, w.Code, w.Body.String())
			}
		}
	}
}

func TestTaskDetailsAndStreamIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
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
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var owner, other int64
	for name, dest := range map[string]*int64{"task-detail-owner": &owner, "task-detail-other": &other} {
		if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,can_use_quick_generation,can_train_image_lora) VALUES($1,'disabled','user',true,true) RETURNING id`, name).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := contentcrypto.NewCipher("task-detail-integration-secret-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: store.New(db), contentCipher: cipher}
	const generationID, trainingID, batchID = "task-detail-generation-1", "task-detail-training-1", "task-detail-batch-1"
	if _, _, err := app.store.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: generationID, UserID: owner, RequestID: generationID}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{
		PublicID: trainingID, RequestID: trainingID, UserID: owner, ProfileID: "profile", Family: "krea2", BaseModel: "base", Name: "Task Training", OutputName: "task_output",
		TriggerWord: "task", ConceptType: "object", Preset: "quick", Resolution: 512, MaxTrainSteps: 100, NetworkDim: 8, NetworkAlpha: 8, LearningRate: 0.0001, SampleCount: 5, DatasetBytes: 1, DatasetPath: "fixture.zip",
	}); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO generation_batches(public_id,user_id,request_id,template_id,workflow_id,model_name,experiment_mode,total_count) VALUES($1,$2,$1,'template','workflow','Task model','seeds',2)`, batchID, owner)
	exec(`UPDATE generation_jobs SET batch_id=(SELECT id FROM generation_batches WHERE public_id=$1),batch_position=1 WHERE public_id=$2`, batchID, generationID)
	manifest := domain.LoraDatasetManifest{Version: 1, Images: []domain.LoraDatasetImage{{ID: "photo", AssetID: "task-asset"}}}
	manifestCipher, manifestHash, err := app.encryptLoraDatasetManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO lora_datasets(id,user_id,name,manifest_cipher) VALUES('task-dataset',$1,'Dataset',$2)`, owner, manifestCipher)
	exec(`INSERT INTO lora_dataset_assets(id,user_id,name,content_hash,mime_type,size_bytes,width,height) VALUES('task-asset',$1,'photo',repeat('a',64),'image/png',1,256,256)`, owner)
	job, err := app.makeLoraCaptionJob("task-dataset", domain.LoraDatasetImage{ID: "photo", AssetID: "task-asset"}, domain.LoraDatasetAsset{ID: "task-asset", Hash: strings.Repeat("a", 64)}, "subject", "object")
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO lora_caption_jobs(id,user_id,dataset_id,image_id,asset_id,request_key,instruction_version,input_cipher,state) VALUES($1,$2,'task-dataset','photo','task-asset',$3,$4,$5,'failed')`, job.ID, owner, job.RequestKey, job.InstructionVersion, job.InputCipher)
	read := func(user User, kind, id string, status int) taskDetailView {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/tasks/detail?kind="+kind+"&id="+id, nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &user))
		w := httptest.NewRecorder()
		app.handleTaskDetail(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", kind, id, w.Code, w.Body.String())
		}
		var view taskDetailView
		if status == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"input_cipher", "result_cipher", "lease_token", "request_key", "run_token", "dataset_path"} {
				if strings.Contains(w.Body.String(), `"`+private+`"`) {
					t.Fatalf("private field %s", private)
				}
			}
		}
		return view
	}
	admin := User{ID: owner, Role: "admin"}
	if view := read(admin, "generation", generationID, 200); view.GPU != nil {
		t.Fatal("task without coordinator intent invented GPU status")
	}
	for kind, key := range map[domain.GPUWorkKind]string{domain.GPUWorkGeneration: generationID, domain.GPUWorkTraining: trainingID} {
		if _, err := app.store.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "detail-gpu-" + string(kind), ResourceID: "detail-gpu", Kind: kind, JobKey: key, UserID: owner}); err != nil {
			t.Fatal(err)
		}
		if view := read(admin, string(kind), key, 200); view.GPU == nil || view.GPU.Code != "executor_unavailable" || view.GPU.Position < 1 || view.GPU.SnapshotAt.IsZero() {
			t.Fatalf("detail missing GPU snapshot: %+v", view.GPU)
		}
	}
	readGPU := func(user User, kind, id string, status int) taskGPUView {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/tasks/gpu?kind="+kind+"&id="+id, nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &user))
		w := httptest.NewRecorder()
		app.handleTaskGPU(w, r)
		if w.Code != status || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("GPU %s %s: %d %s", kind, id, w.Code, w.Body.String())
		}
		var result taskGPUView
		if status == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil || len(fields) != 3 || fields["gpu"] == nil {
				t.Fatalf("GPU read includes source detail or lacks envelope: %s", w.Body.String())
			}
		}
		return result
	}
	for kind, id := range map[string]string{"generation": generationID, "lora_training": trainingID, "lora_caption": job.ID} {
		view := readGPU(admin, kind, id, 200)
		if view.Kind != kind || view.ID != id || (kind != "lora_caption" && (view.GPU == nil || view.GPU.Position < 1)) {
			t.Fatalf("independent GPU read identity: %+v", view)
		}
		if kind == "lora_caption" && view.GPU != nil {
			t.Fatal("independent read invented a caption intent")
		}
		readGPU(User{ID: other, Role: "admin"}, kind, id, 404)
		readGPU(User{ID: owner}, kind, id, 404)
	}
	readGPU(admin, "generation_batch", batchID, 404)
	readGPUBatch := func(user User) domain.TaskGPUSnapshots {
		t.Helper()
		query := url.Values{"task": {"generation:" + generationID, "lora_training:" + trainingID,
			"lora_caption:" + job.ID, "generation:missing"}}
		r := httptest.NewRequest("GET", "/api/tasks/gpu-snapshots?"+query.Encode(), nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &user))
		w := httptest.NewRecorder()
		app.handleTaskGPUSnapshots(w, r)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("GPU batch: %d %s", w.Code, w.Body.String())
		}
		var batch domain.TaskGPUSnapshots
		if err := json.Unmarshal(w.Body.Bytes(), &batch); err != nil || len(batch.Tasks) != 4 || batch.SnapshotAt.IsZero() {
			t.Fatalf("GPU batch envelope: %s %v", w.Body.String(), err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil || len(fields) != 2 {
			t.Fatal("batch contains fields beyond snapshot time and entries")
		}
		for i, entry := range batch.Tasks {
			if query["task"][i] != entry.Kind+":"+entry.ID || entry.GPU != nil && !entry.GPU.SnapshotAt.Equal(batch.SnapshotAt) {
				t.Fatalf("GPU batch identity or timestamp: %+v", batch)
			}
		}
		for _, secret := range []string{"input_cipher", "result_cipher", "lease_token", "request_key", "run_token", "dataset_path", "work_id", "user_id", "job_key", "foreign-private"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("GPU batch leaked %s", secret)
			}
		}
		return batch
	}
	batchGPU := readGPUBatch(admin)
	if !batchGPU.Tasks[0].Available || batchGPU.Tasks[0].GPU == nil ||
		!batchGPU.Tasks[1].Available || batchGPU.Tasks[1].GPU == nil ||
		!batchGPU.Tasks[2].Available || batchGPU.Tasks[2].GPU != nil ||
		batchGPU.Tasks[3].Available || batchGPU.Tasks[3].GPU != nil {
		t.Fatalf("batch presence semantics: %+v", batchGPU)
	}
	for _, entry := range readGPUBatch(User{ID: other, Role: "admin"}).Tasks {
		if entry.Available || entry.GPU != nil {
			t.Fatal("batch revealed foreign source")
		}
	}
	trainingOnly := readGPUBatch(User{ID: owner, CanTrainImageLora: true})
	if trainingOnly.Tasks[0].Available || trainingOnly.Tasks[0].GPU != nil || !trainingOnly.Tasks[1].Available || !trainingOnly.Tasks[2].Available {
		t.Fatal("batch ignored effective source permissions")
	}
	beforeGPU := readGPU(admin, "generation", generationID, 200)
	beforeRevision, err := app.store.TaskRevision(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "detail-foreign-gpu", ResourceID: "detail-gpu", Kind: domain.GPUWorkAssistant, JobKey: "foreign-private", UserID: other, Priority: true}); err != nil {
		t.Fatal(err)
	}
	afterGPU := readGPU(admin, "generation", generationID, 200)
	afterRevision, err := app.store.TaskRevision(ctx, owner)
	if err != nil || afterRevision != beforeRevision || afterGPU.GPU.Position != beforeGPU.GPU.Position+1 {
		t.Fatalf("GPU read depended on source revision: before=%+v after=%+v revisions=%d/%d err=%v", beforeGPU.GPU, afterGPU.GPU, beforeRevision, afterRevision, err)
	}
	if fresh := readGPUBatch(admin); fresh.Tasks[0].GPU.Position != batchGPU.Tasks[0].GPU.Position+1 {
		t.Fatal("batch snapshot missed queue movement without source revision")
	}
	var datasetRevision int64
	if err := db.QueryRowContext(ctx, `SELECT revision FROM lora_datasets WHERE id='task-dataset'`).Scan(&datasetRevision); err != nil {
		t.Fatal(err)
	}
	seriesJobs, err := app.store.EnqueueLoraCaptions(ctx, owner, "task-dataset", datasetRevision, []domain.LoraCaptionJob{job})
	if err != nil {
		t.Fatal(err)
	}
	for kind, id := range map[string]string{"generation": generationID, "lora_training": trainingID, "lora_caption": job.ID, "generation_batch": batchID, "lora_caption_series": seriesJobs[0].SeriesID} {
		view := read(admin, kind, id, 200)
		if view.ID != id || view.Kind != kind {
			t.Fatal("detail identity changed")
		}
		read(User{ID: other, Role: "admin"}, kind, id, 404)
		read(User{ID: owner}, kind, id, 404)
	}
	batch := read(admin, "generation_batch", batchID, 200)
	if len(batch.Batch.Jobs) != 1 || batch.Batch.Jobs[0].BatchID != batchID {
		t.Fatal("batch lost child identity")
	}
	if batch.OpenURL != "/generate?batch="+batchID || batch.OpenLabel == "" {
		t.Fatal("batch missing its exact studio target")
	}
	if view := read(admin, "lora_caption", job.ID, 200); view.RetryURL == "" || view.PreviewURL != "/api/lora-datasets/assets/task-asset" {
		t.Fatal("settled caption missing actions")
	}
	if view := read(admin, "lora_caption", job.ID, 200); view.OpenURL != "/train-lora?asset=task-asset&dataset=task-dataset&image=photo" || view.OpenLabel == "" || view.OpenReason != "" {
		t.Fatalf("caption editor target: %+v", view)
	}
	if view := read(admin, "lora_caption_series", seriesJobs[0].SeriesID, 200); view.OpenURL != "/train-lora?dataset=task-dataset" {
		t.Fatal("series missing dataset editor")
	}
	if view := read(admin, "lora_training", trainingID, 200); view.OpenURL != "" || view.OpenReason == "" {
		t.Fatal("legacy training invented an editable source")
	}
	snapshot, err := app.store.CreateLoraDatasetSnapshot(ctx, owner, "task-dataset", datasetRevision, "task-source-version", manifestHash)
	if err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE lora_training_jobs SET dataset_snapshot_id=$1,dataset_snapshot_hash=$2 WHERE public_id=$3`, snapshot.ID, snapshot.Hash, trainingID)
	if view := read(admin, "lora_training", trainingID, 200); view.OpenURL != "/train-lora?version=task-source-version" || view.OpenLabel == "" || view.OpenReason != "" {
		t.Fatalf("training immutable editor target: %+v", view)
	}
	// Even a malformed cross-owner association cannot disclose a dataset target.
	exec(`UPDATE lora_datasets SET user_id=$1 WHERE id='task-dataset'`, other)
	if view := read(admin, "lora_caption", job.ID, 200); view.OpenURL != "" || view.OpenReason == "" {
		t.Fatal("foreign working set exposed an editor link")
	}
	exec(`UPDATE lora_datasets SET user_id=$1 WHERE id='task-dataset'`, owner)
	for _, changedImages := range [][]domain.LoraDatasetImage{{{ID: "photo", AssetID: "replacement"}}, {}} {
		changed, _, err := app.encryptLoraDatasetManifest(domain.LoraDatasetManifest{Version: 1, Images: changedImages})
		if err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE lora_datasets SET manifest_cipher=$1 WHERE id='task-dataset'`, changed)
		if view := read(admin, "lora_caption", job.ID, 200); view.OpenURL != "" || view.OpenReason == "" {
			t.Fatal("removed or replaced frame linked to a different image")
		}
		if view := read(admin, "lora_training", trainingID, 200); view.OpenURL != "/train-lora?version=task-source-version" {
			t.Fatal("working changes replaced training source")
		}
	}
	exec(`UPDATE lora_datasets SET manifest_cipher=$1 WHERE id='task-dataset'`, manifestCipher)
	exec(`INSERT INTO gpu_resources(id) VALUES('task-test-gpu') ON CONFLICT DO NOTHING`)
	exec(`INSERT INTO gpu_work(id,resource_id,kind,job_key,user_id) VALUES('task-inference','task-test-gpu','lora_caption',$1,$2)`, job.ID, owner)
	exec(`INSERT INTO gpu_inference_requests(id,lease_token,request_hash,request_cipher,timeout_ms,state,started_at,run_token) VALUES('task-inference','lease',repeat('b',64),'x',1000,'uncertain',now(),'run')`)
	exec(`UPDATE lora_caption_jobs SET inference_work_id='task-inference' WHERE id=$1`, job.ID)
	if view := read(admin, "lora_caption", job.ID, 200); view.RetryURL != "" || view.RetryReason == "" {
		t.Fatal("uncertain inference allowed retry")
	}
	exec(`UPDATE gpu_inference_requests SET state='completed',completion_code='executor_terminal' WHERE id='task-inference'`)
	if view := read(admin, "lora_caption", job.ID, 200); view.RetryURL == "" {
		t.Fatal("completed inference blocked retry")
	}
	exec(`UPDATE lora_caption_jobs SET input_cipher='corrupt' WHERE id=$1`, job.ID)
	read(admin, "lora_caption", job.ID, 500)
	exec(`DELETE FROM lora_datasets WHERE id='task-dataset'`)
	read(admin, "lora_caption", job.ID, 404)
	if view := read(admin, "lora_caption_series", seriesJobs[0].SeriesID, 200); view.OpenURL != "" || view.OpenReason == "" {
		t.Fatal("deleted dataset retained a series editor link")
	}
	if view := read(admin, "lora_training", trainingID, 200); view.OpenURL != "/train-lora?version=task-source-version" {
		t.Fatal("working dataset deletion lost training snapshot")
	}
	exec(`DELETE FROM lora_dataset_snapshots WHERE id=$1`, snapshot.ID)
	if view := read(admin, "lora_training", trainingID, 200); view.OpenURL != "" || view.OpenReason == "" {
		t.Fatal("deleted snapshot retained an editor link")
	}
	if view := read(admin, "generation", generationID, 200); view.EditURL != "" {
		t.Fatal("active generation offers saved-parameter editing")
	}
	savedPayload, err := cipher.EncryptBytes([]byte(`{"version":1,"values":{"template_id":"text-to-image","generation_workflow":"workflow","model":"model"}}`))
	if err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE generation_jobs SET state='completed',payload_cipher=$1 WHERE public_id=$2`, savedPayload, generationID)
	if view := read(admin, "generation", generationID, 200); view.EditURL != "/generate?edit_job="+generationID {
		t.Fatalf("missing explicit editing link: %+v", view)
	}
	exec(`UPDATE generation_jobs SET payload_cipher='invalid' WHERE public_id=$1`, generationID)
	if view := read(admin, "generation", generationID, 200); view.EditURL != "" {
		t.Fatal("unreadable payload retained editing link")
	}

	// A real streaming HTTP response proves the first event and permission recheck.
	app.cfg.SessionIdleTimeout = time.Hour
	const streamToken = "synthetic-task-stream-session"
	if err := app.store.CreateSession(ctx, owner, security.HashToken(streamToken), time.Now().Add(time.Hour), "test", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	defer app.liveUpdates().close()
	user := User{ID: owner, Role: "user", CanUseQuickGeneration: true, CanTrainImageLora: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &user))
		app.handleTaskEvents(w, r)
	}))
	defer server.Close()
	client := &http.Client{Timeout: 8 * time.Second}
	streamRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/api/tasks/events?since=0", nil)
	streamRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: streamToken})
	response, err := client.Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var event strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		event.WriteString(line)
		if line == "\n" {
			break
		}
	}
	if response.StatusCode != 200 || !strings.Contains(event.String(), "event: tasks") || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream: %d %s", response.StatusCode, event.String())
	}
	exec(`UPDATE users SET can_train_image_lora=false WHERE id=$1`, owner)
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("stream did not close on partial permission revocation: %v", err)
	}
}
