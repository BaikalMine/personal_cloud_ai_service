package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestOwnerTaskReadModelIntegration(t *testing.T) {
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
	resetIntegrationDatabase(t, db)
	repo := store.New(db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var owner, other int64
	for name, dest := range map[string]*int64{"task-owner": &owner, "task-other": &other} {
		if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES($1,'disabled','user') RETURNING id`, name).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	scope := domain.TaskScope{UserID: owner, Generation: true, Training: true}
	list := func(scope domain.TaskScope, f domain.TaskFilter, limit int, cursor string) domain.TaskPage {
		t.Helper()
		page, err := repo.ListTasks(ctx, scope, f, limit, cursor)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	for _, user := range []int64{owner, other} {
		id := fmt.Sprintf("task-generation-%d", user)
		if _, _, err := repo.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: id, UserID: user, RequestID: id}); err != nil {
			t.Fatal(err)
		}
	}
	training, err := repo.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{
		PublicID: "task-training-0001", RequestID: "task-training-0001", UserID: owner,
		ProfileID: "profile", Family: "krea2", BaseModel: "base", Name: "Task Training", OutputName: "task_output",
		TriggerWord: "task", ConceptType: "object", Preset: "quick", Resolution: 512, MaxTrainSteps: 100,
		NetworkDim: 8, NetworkAlpha: 8, LearningRate: 0.0001, SampleCount: 5, DatasetBytes: 1, DatasetPath: "fixture.zip",
	})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO lora_datasets(id,user_id,name,manifest_cipher) VALUES('task-dataset',$1,'Exact 100%_set','x')`, owner)
	exec(`INSERT INTO lora_dataset_assets(id,user_id,name,content_hash,mime_type,size_bytes,width,height)
		VALUES('task-asset',$1,'fixture',repeat('a',64),'image/png',1,256,256)`, owner)
	exec(`INSERT INTO lora_caption_jobs(id,user_id,dataset_id,image_id,asset_id,request_key,instruction_version,input_cipher)
		VALUES('task-caption',$1,'task-dataset','image-1','task-asset',repeat('b',64),'v1','x')`, owner)
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	exec(`UPDATE generation_jobs SET created_at=$1,model_name='Task Model'`, created)
	exec(`UPDATE lora_training_jobs SET created_at=$1,progress=67`, created)
	exec(`UPDATE lora_caption_jobs SET created_at=$1`, created)
	page := list(scope, domain.TaskFilter{}, 10, "")
	if page.Total != 3 || len(page.Tasks) != 3 || page.Revision < 3 || page.NextCursor != "" {
		t.Fatalf("initial page: %+v", page)
	}
	if page.Tasks[0].Kind != "lora_training" || page.Tasks[0].Progress != nil || page.Tasks[1].Kind != "lora_caption" || page.Tasks[2].Kind != "generation" {
		t.Fatalf("ordering or queued percentage: %+v", page.Tasks)
	}
	if page.Tasks[1].DatasetID != "task-dataset" || page.Tasks[1].ImageID != "image-1" || page.Tasks[1].ExpiresAt == nil {
		t.Fatalf("caption source identity lost: %+v", page.Tasks[1])
	}
	if page.Tasks[0].Preview != nil || page.Tasks[2].Preview != nil || page.Tasks[1].Preview == nil || page.Tasks[1].Preview.AssetID != "task-asset" {
		t.Fatalf("initial previews: %+v", page.Tasks)
	}
	if page.Tasks[0].SourceAvailable || page.Tasks[0].ArtifactAvailable || !page.Tasks[1].SourceAvailable || page.Tasks[2].SourceAvailable {
		t.Fatalf("initial action availability: %+v", page.Tasks)
	}
	exec(`UPDATE generation_jobs SET payload_cipher='saved-input' WHERE user_id=$1`, owner)
	if !list(scope, domain.TaskFilter{Kind: "generation"}, 10, "").Tasks[0].SourceAvailable {
		t.Fatal("saved input not reported")
	}
	exec(`UPDATE generation_jobs SET payload_cipher=NULL WHERE user_id=$1`, owner)
	exec(`INSERT INTO lora_dataset_snapshots(id,user_id,name,revision,manifest_cipher,manifest_hash,image_count,size_bytes)
		VALUES('task-source-version',$1,'Source version',1,'x',repeat('c',64),1,1)`, owner)
	exec(`UPDATE lora_training_jobs SET dataset_snapshot_id='task-source-version',artifact_name='test.safetensors' WHERE id=$1`, training.ID)
	source := list(scope, domain.TaskFilter{Kind: "lora_training"}, 10, "").Tasks[0]
	if !source.SourceAvailable || !source.ArtifactAvailable {
		t.Fatalf("training source flags: %+v", source)
	}
	exec(`UPDATE lora_dataset_snapshots SET user_id=$1 WHERE id='task-source-version'`, other)
	if list(scope, domain.TaskFilter{Kind: "lora_training"}, 10, "").Tasks[0].SourceAvailable {
		t.Fatal("foreign source version reported")
	}
	exec(`DELETE FROM lora_dataset_snapshots WHERE id='task-source-version'`)
	if list(scope, domain.TaskFilter{Kind: "lora_training"}, 10, "").Tasks[0].SourceAvailable {
		t.Fatal("deleted source version reported")
	}
	exec(`UPDATE lora_training_jobs SET artifact_name='' WHERE id=$1`, training.ID)
	exec(`UPDATE generation_jobs SET prompt_id='task-preview-prompt' WHERE user_id=$1`, owner)
	var eventID, mediaID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO content_events(user_id,service,kind,external_id,prompt_cipher,response_cipher,metadata_cipher)
		VALUES($1,'comfyui','comfyui_prompt','task-preview-prompt','x','x','x') RETURNING id`, owner).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO content_media(event_id,media_type,mime_type,original_name,payload_cipher,size_bytes)
		VALUES($1,'image','image/png','preview.png','x',1) RETURNING id`, eventID).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	preview := func() *domain.TaskPreview {
		t.Helper()
		task := list(scope, domain.TaskFilter{Kind: "generation"}, 10, "").Tasks[0]
		previews, err := repo.GenerationTaskPreviews(ctx, owner, []string{task.ID, task.ID, fmt.Sprintf("task-generation-%d", other), "missing"})
		if err != nil {
			t.Fatal(err)
		}
		got := previews[task.ID].Preview
		if len(previews) > 1 || (task.Preview == nil) != (got == nil) || task.Preview != nil && *task.Preview != *got {
			t.Fatalf("studio/task cover mismatch: %+v %+v", task.Preview, previews)
		}
		foreign, err := repo.GenerationTaskPreviews(ctx, other, []string{task.ID})
		if err != nil || len(foreign) != 0 {
			t.Fatalf("foreign studio preview: %+v %v", foreign, err)
		}
		return task.Preview
	}
	if p := preview(); p == nil || p.MediaID != mediaID || !p.Sensitive || !p.ExpiresAt.After(time.Now()) {
		t.Fatalf("unclassified preview: %+v", p)
	}
	exec(`UPDATE content_media SET visual_sensitivity_classified_at=now() WHERE id=$1`, mediaID)
	if p := preview(); p == nil || p.Sensitive {
		t.Fatalf("classified preview: %+v", p)
	}
	exec(`UPDATE content_events SET is_sensitive=true WHERE id=$1`, eventID)
	if p := preview(); p == nil || !p.Sensitive {
		t.Fatalf("sensitive preview: %+v", p)
	}
	for _, change := range []struct{ query, undo string }{
		{`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, `UPDATE content_media SET profile_hidden_at=NULL WHERE id=$1`},
		{`UPDATE content_media SET expires_at=now()-interval '1 second' WHERE id=$1`, `UPDATE content_media SET expires_at=now()+interval '1 hour' WHERE id=$1`},
	} {
		exec(change.query, mediaID)
		if p := preview(); p != nil {
			t.Fatalf("unavailable media leaked: %+v", p)
		}
		exec(change.undo, mediaID)
	}
	exec(`UPDATE content_events SET expires_at=now()-interval '1 second' WHERE id=$1`, eventID)
	if p := preview(); p != nil {
		t.Fatalf("expired event leaked: %+v", p)
	}
	exec(`UPDATE content_events SET expires_at=now()+interval '1 hour',user_id=$2 WHERE id=$1`, eventID, other)
	if p := preview(); p != nil {
		t.Fatalf("another owner's media leaked: %+v", p)
	}
	exec(`UPDATE content_events SET user_id=$2 WHERE id=$1`, eventID, owner)
	exec(`UPDATE lora_caption_jobs SET expires_at=now()-interval '1 second' WHERE id='task-caption'`)
	if p := list(scope, domain.TaskFilter{Kind: "lora_caption"}, 10, "").Tasks[0].Preview; p != nil {
		t.Fatalf("expired caption preview: %+v", p)
	}
	exec(`UPDATE lora_caption_jobs SET expires_at=now()+interval '1 hour' WHERE id='task-caption'`)
	exec(`UPDATE lora_dataset_assets SET user_id=$1 WHERE id='task-asset'`, other)
	if p := list(scope, domain.TaskFilter{Kind: "lora_caption"}, 10, "").Tasks[0].Preview; p != nil {
		t.Fatalf("foreign caption asset: %+v", p)
	}
	exec(`UPDATE lora_dataset_assets SET user_id=$1 WHERE id='task-asset'`, owner)
	first := list(scope, domain.TaskFilter{}, 1, "")
	exec(`UPDATE lora_training_jobs SET state='running',updated_at=now() WHERE id=$1`, training.ID)
	second := list(scope, domain.TaskFilter{}, 1, first.NextCursor)
	third := list(scope, domain.TaskFilter{}, 1, second.NextCursor)
	if second.Tasks[0].Kind != "lora_caption" || third.Tasks[0].Kind != "generation" || third.NextCursor != "" || second.Revision <= first.Revision {
		t.Fatalf("keyset pagination after update: %+v %+v", second, third)
	}
	page = list(scope, domain.TaskFilter{Kind: "lora_training", State: "running"}, 10, "")
	if page.Total != 1 || page.Tasks[0].Progress == nil || *page.Tasks[0].Progress != 67 {
		t.Fatalf("reported training progress lost: %+v", page)
	}
	for _, query := range []string{"100%_", "TASK-CAPTION", "exact"} {
		if got := list(scope, domain.TaskFilter{Search: query}, 10, ""); got.Total != 1 || got.Tasks[0].Kind != "lora_caption" {
			t.Fatalf("literal/name/id search %q: %+v", query, got)
		}
	}
	for _, query := range []string{"%missing", "' OR 1=1 --"} {
		if got := list(scope, domain.TaskFilter{Search: query}, 10, ""); got.Total != 0 {
			t.Fatalf("search treated as syntax: %q", query)
		}
	}
	before := created.Add(time.Microsecond)
	if list(scope, domain.TaskFilter{From: &created, Before: &before}, 10, "").Total != 3 || list(scope, domain.TaskFilter{Before: &created}, 10, "").Total != 0 {
		t.Fatal("date interval is not inclusive-from/exclusive-before")
	}
	for _, test := range []struct {
		scope domain.TaskScope
		total int64
	}{
		{domain.TaskScope{UserID: owner, Generation: true}, 1}, {domain.TaskScope{UserID: owner, Training: true}, 2},
		{domain.TaskScope{UserID: owner}, 0}, {domain.TaskScope{UserID: other, Generation: true, Training: true}, 1},
	} {
		if got := list(test.scope, domain.TaskFilter{}, 10, ""); got.Total != test.total {
			t.Fatalf("scope leaked work: %+v -> %+v", test.scope, got)
		}
	}
	if _, err := repo.ListTasks(ctx, domain.TaskScope{UserID: other, Generation: true, Training: true}, domain.TaskFilter{}, 1, first.NextCursor); !errors.Is(err, store.ErrTaskQuery) {
		t.Fatal("accepted another owner's cursor")
	}
	if _, err := repo.ListTasks(ctx, scope, domain.TaskFilter{State: "running"}, 1, first.NextCursor); !errors.Is(err, store.ErrTaskQuery) {
		t.Fatal("accepted cursor with changed filters")
	}
	prior := list(scope, domain.TaskFilter{}, 10, "")
	exec(`UPDATE generation_jobs SET status_message='other owner changed' WHERE user_id=$1`, other)
	if list(scope, domain.TaskFilter{}, 10, "").Revision != prior.Revision {
		t.Fatal("another owner's update changed revision")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lora_training_jobs SET message='rolled back' WHERE id=$1`, training.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if list(scope, domain.TaskFilter{}, 10, "").Revision != prior.Revision {
		t.Fatal("revision escaped rollback")
	}
	exec(`UPDATE lora_datasets SET name='Renamed dataset' WHERE id='task-dataset'`)
	if got := list(scope, domain.TaskFilter{Search: "Renamed"}, 10, ""); got.Total != 1 || got.Revision <= prior.Revision {
		t.Fatal("dataset rename did not invalidate projection")
	}
	prior = list(scope, domain.TaskFilter{}, 10, "")
	exec(`UPDATE users SET can_train_image_lora=true WHERE id=$1`, owner)
	if list(scope, domain.TaskFilter{}, 10, "").Revision <= prior.Revision {
		t.Fatal("access change did not invalidate projection")
	}
	exec(`UPDATE generation_jobs SET cancellation_requested_at=now() WHERE user_id=$1`, owner)
	exec(`UPDATE lora_training_jobs SET cancellation_requested_at=now() WHERE user_id=$1`, owner)
	exec(`UPDATE lora_caption_jobs SET cancel_requested=true WHERE user_id=$1`, owner)
	for _, task := range list(scope, domain.TaskFilter{}, 10, "").Tasks {
		if !task.CancellationPending {
			t.Fatalf("active cancellation not shown: %+v", task)
		}
	}
	exec(`UPDATE generation_jobs SET state='cancelled' WHERE user_id=$1`, owner)
	exec(`UPDATE lora_training_jobs SET state='cancelled' WHERE user_id=$1`, owner)
	exec(`UPDATE lora_caption_jobs SET state='cancelled' WHERE user_id=$1`, owner)
	for _, task := range list(scope, domain.TaskFilter{}, 10, "").Tasks {
		if task.CancellationPending {
			t.Fatalf("terminal cancellation still pending: %+v", task)
		}
	}
	exec(`INSERT INTO generation_batches(public_id,user_id,request_id,template_id,workflow_id,model_name,experiment_mode,total_count)
		VALUES('task-batch-000001',$1,'task-batch-000001','text-to-image','fixture','model','seeds',2)`, owner)
	exec(`UPDATE generation_jobs SET batch_id=(SELECT id FROM generation_batches WHERE public_id='task-batch-000001'),batch_position=2 WHERE user_id=$1`, owner)
	if task := list(scope, domain.TaskFilter{Kind: "generation"}, 10, "").Tasks[0]; task.BatchID != "task-batch-000001" || task.BatchPosition != 2 {
		t.Fatalf("batch identity lost: %+v", task)
	}
	prior = list(scope, domain.TaskFilter{}, 10, "")
	exec(`DELETE FROM generation_batches WHERE public_id='task-batch-000001'`)
	if got := list(scope, domain.TaskFilter{Kind: "generation"}, 10, ""); got.Tasks[0].BatchID != "" || got.Tasks[0].BatchPosition != 0 || got.Revision <= prior.Revision {
		t.Fatal("deleted batch remained in projection")
	}
	for _, id := range []string{"task-page-Z-000001", "task-page-a-000001", "task-page-0-000001"} {
		if _, _, err := repo.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: id, UserID: owner, RequestID: id}); err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE generation_jobs SET created_at=$1 WHERE public_id=$2`, created, id)
	}
	seen := map[string]bool{}
	cursor, previous := "", ""
	for {
		got := list(scope, domain.TaskFilter{}, 1, cursor)
		if len(got.Tasks) != 1 {
			t.Fatalf("missing page: %+v", got)
		}
		task := got.Tasks[0]
		key := task.Kind + ":" + task.ID
		if seen[key] || (previous != "" && previous <= key) || task.ID == "task-page-newest-01" {
			t.Fatalf("unstable creation order: %s after %s", key, previous)
		}
		seen[key], previous = true, key
		if len(seen) == 1 {
			if _, _, err := repo.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: "task-page-newest-01", UserID: owner, RequestID: "task-page-newest-01"}); err != nil {
				t.Fatal(err)
			}
		}
		cursor = got.NextCursor
		if cursor == "" {
			break
		}
		if len(seen) > 6 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 6 {
		t.Fatalf("page traversal lost tasks: %d", len(seen))
	}
	exec(`DELETE FROM generation_jobs WHERE user_id=$1 AND public_id LIKE 'task-page-%'`, owner)

	// The message is the exact revision produced by the same transaction. A list
	// assembled from different snapshots will eventually expose a mismatched pair.
	updateMarker := `UPDATE generation_jobs SET status_message=(SELECT (revision+1)::text FROM user_task_revisions WHERE user_id=$1),updated_at=clock_timestamp() WHERE user_id=$1`
	exec(updateMarker, owner)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := db.ExecContext(ctx, updateMarker, owner); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 100; i++ {
		got := list(scope, domain.TaskFilter{Kind: "generation"}, 10, "")
		if got.Total != 1 || got.Tasks[0].Message != strconv.FormatInt(got.Revision, 10) {
			t.Fatalf("mixed snapshot: %+v", got)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	prior = list(scope, domain.TaskFilter{}, 10, "")
	exec(`DELETE FROM lora_datasets WHERE id='task-dataset'`)
	if got := list(scope, domain.TaskFilter{}, 10, ""); got.Total != 2 || got.Revision <= prior.Revision {
		t.Fatal("cascade deletion not reflected")
	}
	exec(`DELETE FROM users WHERE id=$1`, owner)
	if got := list(scope, domain.TaskFilter{}, 10, ""); got.Total != 0 || got.Revision != 0 {
		t.Fatalf("owner deletion: %+v", got)
	}
	if got := list(domain.TaskScope{UserID: other, Generation: true}, domain.TaskFilter{}, 10, ""); got.Total != 1 || strings.Contains(got.Tasks[0].ID, "caption") {
		t.Fatal("other owner affected by deletion")
	}
}
