package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestTaskThumbnailIdentityAndMethods(t *testing.T) {
	valid := "kind=generation&id=abc-1&ref=media%3A42&sensitive=0"
	if identity, ok := parseTaskThumbnailIdentity(valid); !ok || identity.reference != "media:42" || identity.sensitive {
		t.Fatalf("identity: %+v %v", identity, ok)
	}
	for _, query := range []string{valid + "&user_id=9", valid + "&id=x", strings.Replace(valid, "sensitive=0", "sensitive=true", 1), strings.Replace(valid, "abc-1", "a%2Fb", 1), strings.Replace(valid, "generation", "generation_batch", 1), strings.Replace(valid, "abc-1", "%00", 1), strings.Replace(valid, "ref=media%3A42", "ref=", 1), valid + "%zz"} {
		if _, ok := parseTaskThumbnailIdentity(query); ok {
			t.Fatalf("accepted %s", query)
		}
	}
	app := &App{}
	for method, want := range map[string]int{"GET": 401, "POST": 405, "HEAD": 405} {
		r := httptest.NewRequest(method, "/api/tasks/thumbnail?"+valid, nil)
		w := httptest.NewRecorder()
		app.handleTaskThumbnail(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" || w.Header().Get("Location") != "" {
			t.Fatalf("%s: %d %v", method, w.Code, w.Header())
		}
	}
}

func TestTaskThumbnailSourcesAndCacheAuthorizationIntegration(t *testing.T) {
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
	execute := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var owner, other int64
	for name, dest := range map[string]*int64{"thumbnail-owner": &owner, "thumbnail-other": &other} {
		if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,can_use_quick_generation,can_generate_text_to_image,can_generate_video,can_train_image_lora) VALUES($1,'disabled','user',true,true,true,true) RETURNING id`, name).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := contentcrypto.NewCipher("thumbnail-integration-secret-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: store.New(db), contentCipher: cipher, cfg: Config{MediaSpoolDir: t.TempDir()}}
	var pngBytes bytes.Buffer
	im := image.NewNRGBA(image.Rect(0, 0, 512, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 512; x++ {
			im.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	if err := png.Encode(&pngBytes, im); err != nil {
		t.Fatal(err)
	}
	payload, err := cipher.EncryptBytes(pngBytes.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	const jobID = "thumbnail-generation"
	if _, _, err := app.store.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: jobID, UserID: owner, RequestID: jobID}); err != nil {
		t.Fatal(err)
	}
	execute(`UPDATE generation_jobs SET prompt_id='thumbnail-prompt',template_id='text-to-image' WHERE public_id=$1`, jobID)
	var eventID, mediaID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO content_events(user_id,service,kind,external_id,prompt_cipher,response_cipher,metadata_cipher) VALUES($1,'comfyui','comfyui_prompt','thumbnail-prompt','x','x','x') RETURNING id`, owner).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO content_media(event_id,media_type,mime_type,original_name,payload_cipher,size_bytes,visual_sensitivity_classified_at) VALUES($1,'image','image/png','source.png',$2,$3,now()) RETURNING id`, eventID, payload, len(pngBytes.Bytes())).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	scope := domain.TaskScope{UserID: owner, Generation: true, Training: true}
	preview, err := app.store.TaskPreview(ctx, scope, "generation", jobID)
	if err != nil {
		t.Fatal(err)
	}
	task := domain.Task{Kind: "generation", ID: jobID, Preview: preview}
	job, err := app.store.GenerationJobByPublicID(ctx, owner, jobID)
	if err != nil {
		t.Fatal(err)
	}
	views, err := app.generationJobViews(ctx, []domain.GenerationJob{job}, owner)
	if err != nil || len(views) != 1 || views[0].Preview == nil || views[0].Preview.URL != taskThumbnailURL(task) || views[0].Preview.Sensitive || views[0].Preview.SourceType != "image" {
		t.Fatalf("studio preview adapter: %+v %v", views, err)
	}
	identity, ok := parseTaskThumbnailIdentity(strings.SplitN(taskThumbnailURL(task), "?", 2)[1])
	if !ok {
		t.Fatal("adapter URL rejected")
	}
	source, err := app.loadTaskThumbnailSource(ctx, owner, identity)
	if err != nil {
		t.Fatal(err)
	}
	// A prefilled cache lets permission tests run even where native FFmpeg is absent.
	_, err = app.taskThumbnailCache().Get(ctx, source.key, preview.ExpiresAt, func(context.Context) ([]byte, error) { return pngBytes.Bytes(), nil })
	if err != nil {
		t.Fatal(err)
	}
	read := func(userID int64, url string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", url, nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: userID, Role: "admin"}))
		w := httptest.NewRecorder()
		app.handleTaskThumbnail(w, r)
		if w.Code != want {
			t.Fatalf("thumbnail %d want %d: %s", w.Code, want, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
			t.Fatal("privacy headers missing")
		}
		return w
	}
	url := taskThumbnailURL(task)
	w := read(owner, url, 200)
	if !bytes.Equal(w.Body.Bytes(), pngBytes.Bytes()) {
		t.Fatal("cache bytes changed")
	}
	read(other, url, 404)
	read(owner, strings.Replace(url, jobID, "missing", 1), 404)
	for _, change := range []struct {
		query, undo string
		id          int64
	}{
		{`UPDATE users SET disabled=true WHERE id=$1`, `UPDATE users SET disabled=false WHERE id=$1`, owner},
		{`UPDATE users SET account_expires_at=now()-interval '1 second' WHERE id=$1`, `UPDATE users SET account_expires_at=NULL WHERE id=$1`, owner},
		{`UPDATE users SET can_use_quick_generation=false WHERE id=$1`, `UPDATE users SET can_use_quick_generation=true WHERE id=$1`, owner},
		{`UPDATE users SET can_generate_text_to_image=false WHERE id=$1`, `UPDATE users SET can_generate_text_to_image=true WHERE id=$1`, owner},
		{`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, `UPDATE content_media SET profile_hidden_at=NULL WHERE id=$1`, mediaID},
		{`UPDATE content_media SET expires_at=now()-interval '1 second' WHERE id=$1`, `UPDATE content_media SET expires_at=now()+interval '1 hour' WHERE id=$1`, mediaID},
		{`UPDATE content_events SET expires_at=now()-interval '1 second' WHERE id=$1`, `UPDATE content_events SET expires_at=now()+interval '1 hour' WHERE id=$1`, eventID},
		{`UPDATE content_media SET visual_sensitivity_classified_at=NULL WHERE id=$1`, `UPDATE content_media SET visual_sensitivity_classified_at=now() WHERE id=$1`, mediaID},
		{`UPDATE content_events SET is_sensitive=true WHERE id=$1`, `UPDATE content_events SET is_sensitive=false WHERE id=$1`, eventID},
	} {
		execute(change.query, change.id)
		read(owner, url, 404)
		execute(change.undo, change.id)
		read(owner, url, 200)
	}
	// A new chosen result invalidates the old immutable URL, even with cached data.
	var videoID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO content_media(event_id,media_type,mime_type,original_name,payload_cipher,size_bytes,visual_sensitivity_classified_at) VALUES($1,'video','video/mp4','video.mp4','x',1,now()) RETURNING id`, eventID).Scan(&videoID); err != nil {
		t.Fatal(err)
	}
	p, err := app.store.TaskPreview(ctx, scope, "generation", jobID)
	if err != nil || p.MediaID != mediaID {
		t.Fatal("video displaced available still")
	}
	execute(`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, mediaID)
	p, err = app.store.TaskPreview(ctx, scope, "generation", jobID)
	if err != nil || p.MediaID != videoID || p.MediaType != "video" {
		t.Fatalf("video fallback: %+v %v", p, err)
	}
	read(owner, url, 404)
	execute(`UPDATE content_media SET profile_hidden_at=NULL WHERE id=$1`, mediaID)
	// Real byte production verifies encrypted source materialization and cleanup.
	if _, err := exec.LookPath("ffmpeg"); err == nil && runtime.GOOS == "linux" {
		data, err := app.produceTaskThumbnail(ctx, owner, source)
		if err != nil {
			t.Fatal(err)
		}
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width != 160 || config.Height != 80 {
			t.Fatalf("derivative: %+v %v", config, err)
		}
	}
	media, err := app.store.ContentMediaByIDForUser(ctx, mediaID, owner)
	if err != nil {
		t.Fatal(err)
	}
	media.PayloadCipher = []byte("corrupt")
	if _, err := app.materializeContentMedia(ctx, media); err == nil {
		t.Fatal("corrupt ciphertext accepted")
	}
	media.PayloadCipher = payload
	media.SizeBytes = 1
	if _, err := app.materializeContentMedia(ctx, media); err == nil {
		t.Fatal("false inline size accepted")
	}
	if files, err := os.ReadDir(app.mediaSpoolDir()); err != nil || len(files) != 0 {
		t.Fatalf("plaintext files leaked: %v %v", files, err)
	}

	// Training covers are bound to immutable snapshots, never the editable set.
	asset, err := app.persistLoraDatasetImage(ctx, owner, "original.png", bytes.NewReader(pngBytes.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.LoraDatasetManifest{Version: 1, Images: []domain.LoraDatasetImage{{ID: "excluded", AssetID: "not-retained", Excluded: true}, {ID: "removed", AssetID: "no-longer-retained"}, {ID: "frame", AssetID: asset.ID}}}
	manifestCipher, hash, err := app.encryptLoraDatasetManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	execute(`INSERT INTO lora_dataset_snapshots(id,user_id,name,revision,manifest_cipher,manifest_hash,image_count,size_bytes,expires_at) VALUES('thumbnail-snapshot',$1,'Original',1,$2,$3,2,$4,now()-interval '1 day')`, owner, manifestCipher, hash, asset.SizeBytes)
	execute(`INSERT INTO lora_dataset_asset_refs(asset_id,snapshot_id) VALUES($1,'thumbnail-snapshot')`, asset.ID)
	const trainingID = "thumbnail-training"
	_, err = app.store.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{PublicID: trainingID, RequestID: trainingID, UserID: owner, ProfileID: "profile", Family: "krea2", BaseModel: "base", Name: "Training", OutputName: "training", TriggerWord: "subject", ConceptType: "object", Preset: "quick", Resolution: 512, MaxTrainSteps: 100, NetworkDim: 8, NetworkAlpha: 8, LearningRate: 0.0001, SampleCount: 5, DatasetBytes: 1, DatasetPath: "fixture.zip", DatasetSnapshotID: "thumbnail-snapshot", DatasetSnapshotHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	trainingPreview, err := app.store.TaskPreview(ctx, scope, "lora_training", trainingID)
	if err != nil {
		t.Fatal(err)
	}
	trainingURL := taskThumbnailURL(domain.Task{Kind: "lora_training", ID: trainingID, Preview: trainingPreview})
	trainingIdentity, ok := parseTaskThumbnailIdentity(strings.SplitN(trainingURL, "?", 2)[1])
	if !ok {
		t.Fatal("training identity invalid")
	}
	trainingSource, err := app.loadTaskThumbnailSource(ctx, owner, trainingIdentity)
	if err != nil || trainingSource.asset == nil || trainingSource.asset.ID != asset.ID {
		t.Fatalf("training source: %+v %v", trainingSource, err)
	}
	_, err = app.taskThumbnailCache().Get(ctx, trainingSource.key, trainingPreview.ExpiresAt, func(context.Context) ([]byte, error) { return pngBytes.Bytes(), nil })
	if err != nil {
		t.Fatal(err)
	}
	read(owner, trainingURL, 200)
	read(other, trainingURL, 404)
	execute(`UPDATE users SET can_train_image_lora=false WHERE id=$1`, owner)
	read(owner, trainingURL, 404)
	execute(`UPDATE users SET can_train_image_lora=true WHERE id=$1`, owner)
	read(owner, trainingURL, 200)
	execute(`UPDATE lora_training_jobs SET dataset_snapshot_hash=repeat('f',64) WHERE public_id=$1`, trainingID)
	read(owner, trainingURL, 404)
	execute(`UPDATE lora_training_jobs SET dataset_snapshot_hash=$2 WHERE public_id=$1`, trainingID, hash)
	execute(`UPDATE lora_dataset_snapshots SET manifest_cipher='corrupt' WHERE id='thumbnail-snapshot'`)
	read(owner, trainingURL, 404)
	execute(`UPDATE lora_dataset_snapshots SET manifest_cipher=$1 WHERE id='thumbnail-snapshot'`, manifestCipher)
	execute(`DELETE FROM lora_dataset_asset_refs WHERE snapshot_id='thumbnail-snapshot'`)
	read(owner, trainingURL, 404)
	execute(`INSERT INTO lora_dataset_asset_refs(asset_id,snapshot_id) VALUES($1,'thumbnail-snapshot')`, asset.ID)
	execute(`UPDATE lora_dataset_assets SET user_id=$2 WHERE id=$1`, asset.ID, other)
	read(owner, trainingURL, 404)
	execute(`UPDATE lora_dataset_assets SET user_id=$2 WHERE id=$1`, asset.ID, owner)
	if _, err := app.store.TaskTrainingPreviewAsset(ctx, owner, "thumbnail-snapshot", []string{"other"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unrecorded asset: %v", err)
	}
	// Caption previews keep the job's asset even after editing the working set.
	execute(`INSERT INTO lora_datasets(id,user_id,name,manifest_cipher) VALUES('thumbnail-dataset',$1,'Edited working set',$2)`, owner, manifestCipher)
	execute(`INSERT INTO lora_caption_jobs(id,user_id,dataset_id,image_id,asset_id,request_key,instruction_version,input_cipher) VALUES('thumbnail-caption',$1,'thumbnail-dataset','frame',$2,repeat('a',64),'v1','x')`, owner, asset.ID)
	captionPreview, err := app.store.TaskPreview(ctx, scope, "lora_caption", "thumbnail-caption")
	if err != nil {
		t.Fatal(err)
	}
	captionURL := taskThumbnailURL(domain.Task{Kind: "lora_caption", ID: "thumbnail-caption", Preview: captionPreview})
	captionIdentity, ok := parseTaskThumbnailIdentity(strings.SplitN(captionURL, "?", 2)[1])
	if !ok {
		t.Fatal("caption identity invalid")
	}
	captionSource, err := app.loadTaskThumbnailSource(ctx, owner, captionIdentity)
	if err != nil || captionSource.asset == nil || captionSource.asset.ID != asset.ID {
		t.Fatalf("caption source: %+v %v", captionSource, err)
	}
	_, err = app.taskThumbnailCache().Get(ctx, captionSource.key, captionPreview.ExpiresAt, func(context.Context) ([]byte, error) { return pngBytes.Bytes(), nil })
	if err != nil {
		t.Fatal(err)
	}
	execute(`UPDATE lora_datasets SET manifest_cipher='new-unrelated-manifest' WHERE id='thumbnail-dataset'`)
	read(owner, captionURL, 200)
	read(other, captionURL, 404)
	execute(`UPDATE lora_caption_jobs SET expires_at=now()-interval '1 second' WHERE id='thumbnail-caption'`)
	read(owner, captionURL, 404)
	badAsset := asset
	badAsset.Hash = strings.Repeat("0", 64)
	if _, err := app.materializeLoraDatasetAsset(ctx, badAsset); err == nil {
		t.Fatal("mismatched asset hash accepted")
	}
	execute(`DELETE FROM lora_dataset_snapshots WHERE id='thumbnail-snapshot'`)
	read(owner, trainingURL, 404)
	if files, err := os.ReadDir(app.mediaSpoolDir()); err != nil || len(files) != 0 {
		t.Fatalf("plaintext files leaked: %v %v", files, err)
	}
}
