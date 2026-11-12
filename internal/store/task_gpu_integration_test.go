package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestTaskGPUStatusIntegration(t *testing.T) {
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
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	repo := store.New(db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`TRUNCATE gpu_resources CASCADE`)
	var owner, other int64
	for name, dest := range map[string]*int64{"gpu-task-owner": &owner, "gpu-task-other": &other} {
		if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES($1,'disabled','user') RETURNING id`, name).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	scope := domain.TaskScope{UserID: owner, Generation: true, Training: true}
	const id = "task-gpu-generation"
	if _, _, err := repo.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: id, UserID: owner, RequestID: id}); err != nil {
		t.Fatal(err)
	}
	read := func(code string, position int) *domain.TaskGPUStatus {
		t.Helper()
		status, err := repo.TaskGPUStatus(ctx, scope, "generation", id)
		if err != nil {
			t.Fatal(err)
		}
		if code == "" {
			if status != nil {
				t.Fatalf("unexpected GPU intent: %+v", status)
			}
			return nil
		}
		if status == nil || status.Code != code || status.Position != position || status.SnapshotAt.IsZero() {
			t.Fatalf("want %s/%d, got %+v", code, position, status)
		}
		encoded, _ := json.Marshal(status)
		for _, secret := range []string{id, "foreign-private-key", "lease-secret", "executor-secret", "job_key", "user_id", "work_id", "phase"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("snapshot disclosed %s", secret)
			}
		}
		return status
	}
	read("", 0)
	for _, denied := range []domain.TaskScope{{UserID: other, Generation: true, Training: true}, {UserID: owner, Training: true}} {
		if _, err := repo.TaskGPUStatus(ctx, denied, "generation", id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("scope: %v", err)
		}
	}
	enqueue := func(workID string, user int64, kind domain.GPUWorkKind, key string, priority bool) {
		t.Helper()
		if _, err := repo.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: workID, UserID: user, Kind: kind, JobKey: key, ResourceID: domain.GPUPrimaryResource, Priority: priority}); err != nil {
			t.Fatal(err)
		}
	}
	enqueue("own-work", owner, domain.GPUWorkGeneration, id, false)
	read("executor_unavailable", 1)
	observe := func(state string) {
		t.Helper()
		r, err := repo.GPUResource(ctx, domain.GPUPrimaryResource)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.ObserveGPUResource(ctx, r.ID, r.Revision, state, "executor-secret", 30*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	observe("idle")
	read("admission_pending", 1)
	enqueue("foreign-work", other, domain.GPUWorkAssistant, "foreign-private-key", true)
	read("waiting_turn", 2)
	admission, err := repo.AcquireGPUWork(ctx, "own-work", "lease-secret", time.Minute)
	if err != nil || admission.Granted || admission.WaitCode != "waiting_turn" || admission.Position != 2 {
		t.Fatalf("admission disagrees: %+v %v", admission, err)
	}
	exec(`UPDATE gpu_work SET queued_at=now()-interval '20 minutes' WHERE id='own-work'`)
	read("admission_pending", 1)
	exec(`UPDATE gpu_work SET queued_at=now() WHERE id='own-work'`)
	read("waiting_turn", 2)
	exec(`UPDATE users SET disabled=true WHERE id=$1`, other)
	read("admission_pending", 1)
	exec(`UPDATE users SET disabled=false,account_expires_at=now()-interval '1 second' WHERE id=$1`, other)
	read("admission_pending", 1)
	exec(`UPDATE users SET account_expires_at=NULL WHERE id=$1`, other)
	exec(`UPDATE gpu_work SET ready_until=now()-interval '1 second' WHERE id='foreign-work'`)
	read("admission_pending", 1)
	exec(`UPDATE gpu_work SET ready_until=now()+interval '5 minutes',cancellation_requested=true WHERE id='foreign-work'`)
	read("admission_pending", 1)
	exec(`UPDATE gpu_work SET cancellation_requested=false WHERE id='foreign-work'`)
	observe("busy")
	read("external_work", 2)
	exec(`UPDATE gpu_resources SET valid_until=now()-interval '1 second'`)
	read("executor_unavailable", 2)
	observe("idle")
	exec(`INSERT INTO gpu_executor_permits(id,resource_id,executor,instance_id,token_hash) VALUES(repeat('a',32),'primary','comfyui',repeat('b',32),repeat('c',64))`)
	read("external_work", 2)
	exec(`UPDATE gpu_executor_permits SET state='closed',completion_code='quiesced'`)
	admission, err = repo.AcquireGPUWork(ctx, "foreign-work", "lease-secret", time.Minute)
	if err != nil || !admission.Granted {
		t.Fatalf("holder setup: %+v %v", admission, err)
	}
	read("gpu_in_use", 1)
	exec(`UPDATE gpu_work SET lease_until=now()-interval '1 second' WHERE id='foreign-work'`)
	before, err := repo.GPUWork(ctx, "foreign-work")
	if err != nil {
		t.Fatal(err)
	}
	resourceBefore, err := repo.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil {
		t.Fatal(err)
	}
	read("executor_unconfirmed", 1)
	after, err := repo.GPUWork(ctx, "foreign-work")
	if err != nil {
		t.Fatal(err)
	}
	resourceAfter, err := repo.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != before.State || !after.UpdatedAt.Equal(before.UpdatedAt) || resourceAfter.Revision != resourceBefore.Revision {
		t.Fatal("read mutated coordinator state")
	}
	exec(`UPDATE gpu_work SET ready_until=now()-interval '1 second' WHERE id='own-work'`)
	read("intent_expired", 0)
	exec(`UPDATE gpu_work SET ready_until=now()+interval '5 minutes' WHERE id='own-work'`)
	exec(`UPDATE users SET disabled=true WHERE id=$1`, owner)
	read("owner_unavailable", 0)
	exec(`UPDATE users SET disabled=false WHERE id=$1`, owner)
	exec(`UPDATE generation_jobs SET cancellation_requested_at=now() WHERE public_id=$1`, id)
	read("cancellation_pending", 0)
	exec(`UPDATE generation_jobs SET cancellation_requested_at=NULL,state='failed' WHERE public_id=$1`, id)
	read("source_terminal", 0)
	exec(`UPDATE gpu_work SET state='released',released_at=now(),lease_token='',lease_until=NULL WHERE id='foreign-work'`)
	exec(`UPDATE generation_jobs SET state='queued' WHERE public_id=$1`, id)
	observe("idle")
	admission, err = repo.AcquireGPUWork(ctx, "own-work", "lease-secret", time.Minute)
	if err != nil || !admission.Granted {
		t.Fatalf("own admission: %+v %v", admission, err)
	}
	read("gpu_reserved", 0)
	if _, err = repo.SetGPUWorkPhase(ctx, "own-work", "lease-secret", "dispatching", "executor-secret"); err != nil {
		t.Fatal(err)
	}
	read("gpu_reserved", 0)
	if _, err = repo.SetGPUWorkPhase(ctx, "own-work", "lease-secret", "running", "executor-secret"); err != nil {
		t.Fatal(err)
	}
	read("gpu_running", 0)
	exec(`UPDATE generation_jobs SET cancellation_requested_at=now() WHERE public_id=$1`, id)
	read("cancellation_pending", 0)
	exec(`UPDATE gpu_work SET lease_until=now()-interval '1 second' WHERE id='own-work'`)
	read("executor_unconfirmed", 0)
	exec(`UPDATE gpu_work SET state='uncertain' WHERE id='own-work'`)
	read("executor_unconfirmed", 0)
	exec(`UPDATE gpu_work SET state='released',released_at=now(),lease_token='',lease_until=NULL WHERE id='own-work'`)
	read("", 0)

	training, err := repo.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{
		PublicID: "task-gpu-training", RequestID: "task-gpu-training", UserID: owner,
		ProfileID: "profile", Family: "krea2", BaseModel: "base", Name: "Training", OutputName: "output",
		TriggerWord: "task", ConceptType: "object", Preset: "quick", Resolution: 512, MaxTrainSteps: 100,
		NetworkDim: 8, NetworkAlpha: 8, LearningRate: 0.0001, SampleCount: 5, DatasetBytes: 1, DatasetPath: "fixture.zip",
	})
	if err != nil {
		t.Fatal(err)
	}
	enqueue("training-work", owner, domain.GPUWorkTraining, training.PublicID, false)
	status, err := repo.TaskGPUStatus(ctx, scope, "lora_training", training.PublicID)
	if err != nil || status == nil || status.Position != 1 {
		t.Fatalf("training source: %+v %v", status, err)
	}
	exec(`INSERT INTO lora_dataset_assets(id,user_id,name,content_hash,mime_type,size_bytes,width,height) VALUES('gpu-asset',$1,'fixture',repeat('d',64),'image/png',1,256,256)`, owner)
	exec(`INSERT INTO lora_caption_jobs(id,user_id,image_id,asset_id,request_key,instruction_version,input_cipher,run_token) VALUES('gpu-caption',$1,'image','gpu-asset',repeat('e',64),'v1','x','current')`, owner)
	enqueue("caption-old", owner, domain.GPUWorkCaption, "gpu-caption:old", false)
	status, err = repo.TaskGPUStatus(ctx, scope, "lora_caption", "gpu-caption")
	if err != nil || status != nil {
		t.Fatalf("old attempt selected: %+v %v", status, err)
	}
	enqueue("caption-current", owner, domain.GPUWorkCaption, "gpu-caption:current", false)
	status, err = repo.TaskGPUStatus(ctx, scope, "lora_caption", "gpu-caption")
	if err != nil || status == nil || status.Position != 3 {
		t.Fatalf("current attempt missing: %+v %v", status, err)
	}
	exec(`UPDATE lora_caption_jobs SET inference_work_id='caption-current',run_token='recovered' WHERE id='gpu-caption'`)
	status, err = repo.TaskGPUStatus(ctx, scope, "lora_caption", "gpu-caption")
	if err != nil || status == nil || status.Position != 3 {
		t.Fatalf("durable attempt lost: %+v %v", status, err)
	}
	exec(`UPDATE lora_caption_jobs SET inference_work_id=NULL,run_token='' WHERE id='gpu-caption'`)
	status, err = repo.TaskGPUStatus(ctx, scope, "lora_caption", "gpu-caption")
	if err != nil || status != nil {
		t.Fatalf("reset attempt inherited queue entry: %+v %v", status, err)
	}

	// A second resource must have its own queue, even in a mixed-source read.
	exec(`UPDATE lora_caption_jobs SET inference_work_id='caption-current' WHERE id='gpu-caption'`)
	const secondID = "task-gpu-second-resource"
	if _, _, err := repo.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: secondID, UserID: owner, RequestID: secondID}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "second-resource-work", UserID: owner,
		Kind: domain.GPUWorkGeneration, JobKey: secondID, ResourceID: "second"}); err != nil {
		t.Fatal(err)
	}
	const foreignID = "task-gpu-foreign-source"
	if _, _, err := repo.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: foreignID, UserID: other, RequestID: foreignID}); err != nil {
		t.Fatal(err)
	}
	keys := []domain.TaskGPUKey{{Kind: "lora_caption", ID: "gpu-caption"}, {Kind: "generation", ID: secondID},
		{Kind: "lora_training", ID: training.PublicID}, {Kind: "generation", ID: id},
		{Kind: "generation", ID: foreignID}, {Kind: "generation", ID: "missing"}}
	readBatch := func(scope domain.TaskScope) domain.TaskGPUSnapshots {
		t.Helper()
		batch, err := repo.TaskGPUSnapshots(ctx, scope, keys)
		if err != nil || len(batch.Tasks) != len(keys) || batch.SnapshotAt.IsZero() {
			t.Fatalf("batch: %+v %v", batch, err)
		}
		for i, entry := range batch.Tasks {
			if entry.TaskGPUKey != keys[i] || entry.GPU != nil && !entry.GPU.SnapshotAt.Equal(batch.SnapshotAt) {
				t.Fatalf("identity/order/time mismatch: %+v", batch)
			}
		}
		return batch
	}
	batch := readBatch(scope)
	if !batch.Tasks[0].Available || batch.Tasks[0].GPU == nil || batch.Tasks[0].GPU.Position != 3 ||
		!batch.Tasks[1].Available || batch.Tasks[1].GPU == nil || batch.Tasks[1].GPU.Position != 1 ||
		!batch.Tasks[2].Available || batch.Tasks[2].GPU == nil || batch.Tasks[2].GPU.Position != 1 ||
		!batch.Tasks[3].Available || batch.Tasks[3].GPU != nil {
		t.Fatalf("mixed source/resource resolution: %+v", batch)
	}
	for _, entry := range batch.Tasks[4:] {
		if entry.Available || entry.GPU != nil {
			t.Fatal("missing and foreign sources must both be unavailable")
		}
	}
	for _, restricted := range []domain.TaskScope{{UserID: owner, Generation: true}, {UserID: owner, Training: true}, {UserID: owner}} {
		batch := readBatch(restricted)
		for i, entry := range batch.Tasks {
			allowed := i < 4 && (entry.Kind == "generation" && restricted.Generation || entry.Kind != "generation" && restricted.Training)
			if entry.Available != allowed || !allowed && entry.GPU != nil {
				t.Fatalf("permission leak: %+v %+v", restricted, entry)
			}
		}
	}
	beforeRevision, err := repo.TaskRevision(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	enqueue("foreign-priority-batch", other, domain.GPUWorkAssistant, "foreign-private-key-batch", true)
	batch = readBatch(scope)
	afterRevision, err := repo.TaskRevision(ctx, owner)
	if err != nil || beforeRevision != afterRevision || batch.Tasks[0].GPU.Position != 4 || batch.Tasks[2].GPU.Position != 2 || batch.Tasks[1].GPU.Position != 1 {
		t.Fatalf("batch tied to source revision or wrong resource: %+v %v", batch, err)
	}
	// Source completion does not erase an unconfirmed reservation.
	exec(`UPDATE generation_jobs SET state='completed' WHERE public_id=$1`, secondID)
	exec(`UPDATE gpu_work SET state='held',lease_token='lease-secret',lease_until=now()+interval '1 minute' WHERE id='second-resource-work'`)
	if gpu := readBatch(scope).Tasks[1].GPU; gpu == nil || gpu.Code != "gpu_reserved" {
		t.Fatalf("terminal source erased holder: %+v", gpu)
	}
	exec(`UPDATE gpu_work SET lease_until=now()-interval '1 second' WHERE id='second-resource-work'`)
	coordinatorState := func() string {
		t.Helper()
		var state string
		if err := db.QueryRowContext(ctx, `SELECT json_build_array(
			(SELECT json_agg(w ORDER BY id) FROM gpu_work w),
			(SELECT json_agg(r ORDER BY id) FROM gpu_resources r),
			(SELECT json_agg(p ORDER BY id) FROM gpu_executor_permits p))::text`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	beforeState := coordinatorState()
	if gpu := readBatch(scope).Tasks[1].GPU; gpu == nil || gpu.Code != "executor_unconfirmed" || gpu.Position != 0 {
		t.Fatalf("expired terminal holder: %+v", gpu)
	}
	if coordinatorState() != beforeState {
		t.Fatal("batch read mutated coordinator")
	}
	maxKeys := make([]domain.TaskGPUKey, domain.TaskGPUSnapshotLimit)
	for i := range maxKeys {
		maxKeys[i] = domain.TaskGPUKey{Kind: "generation", ID: fmt.Sprintf("missing-%d", i)}
	}
	maxBatch, err := repo.TaskGPUSnapshots(ctx, scope, maxKeys)
	if err != nil || len(maxBatch.Tasks) != domain.TaskGPUSnapshotLimit {
		t.Fatalf("maximum batch: %v", err)
	}
	for _, invalid := range [][]domain.TaskGPUKey{nil, append(maxKeys, domain.TaskGPUKey{Kind: "generation", ID: "extra"}),
		{keys[0], keys[0]}, {{Kind: "generation_batch", ID: "a"}}, {{Kind: "generation", ID: ""}}, {{Kind: "generation", ID: strings.Repeat("a", 201)}}} {
		if _, err := repo.TaskGPUSnapshots(ctx, scope, invalid); !errors.Is(err, store.ErrTaskQuery) {
			t.Fatalf("invalid batch: %v", err)
		}
	}
}
