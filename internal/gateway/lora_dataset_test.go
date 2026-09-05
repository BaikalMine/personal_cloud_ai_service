package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/security"
)

func datasetTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewSource(73))
	if _, err := rng.Read(img.Pix); err != nil {
		t.Fatal(err)
	}
	for index := 3; index < len(img.Pix); index += 4 {
		img.Pix[index] = 255
	}
	var body bytes.Buffer
	if err := png.Encode(&body, img); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func TestLoraDatasetManifestValidation(t *testing.T) {
	manifest := domain.LoraDatasetManifest{Version: 1, Settings: domain.LoraDatasetSettings{Name: "incomplete"}, Images: []domain.LoraDatasetImage{{ID: "item-1", AssetID: "asset-1", Caption: "  manual\ncaption  "}}}
	if err := validateLoraDatasetManifest(manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Images = append(manifest.Images, manifest.Images[0])
	if err := validateLoraDatasetManifest(manifest); err == nil {
		t.Fatal("duplicate item ID accepted")
	}
	manifest.Images = manifest.Images[:1]
	manifest.Images[0].AssetID = "../foreign"
	if err := validateLoraDatasetManifest(manifest); err == nil {
		t.Fatal("unsafe ID accepted")
	}
	manifest.Images[0].AssetID = "asset-1"
	manifest.Images[0].Caption = strings.Repeat("a", 1001)
	if err := validateLoraDatasetManifest(manifest); err == nil {
		t.Fatal("oversize caption accepted")
	}
	manifest.Images[0].Caption = ""
	manifest.Version = 2
	if err := validateLoraDatasetManifest(manifest); err == nil {
		t.Fatal("future manifest accepted")
	}
}

func assertLoraDatasetAPI(t *testing.T, ctx context.Context, db *sql.DB, app *App, userID, foreignID int64) {
	t.Helper()
	app.csrfSigner = security.NewCSRFSigner("dataset-integration-only")
	const session = "dataset-session"
	csrf := app.csrfSigner.Token(session)
	request := func(owner int64, method, target string, body []byte, contentType, token string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, bytes.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("X-CSRF-Token", token)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: owner, Role: "user", CanTrainImageLora: true}))
		w := httptest.NewRecorder()
		app.handleLoraDatasets(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d want %d: %s", method, target, w.Code, want, w.Body.String())
		}
		return w
	}
	post := func(owner int64, target string, value any, want int) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return request(owner, http.MethodPost, target, body, "application/json", csrf, want)
	}
	decodeView := func(w *httptest.ResponseRecorder) loraDatasetView {
		t.Helper()
		var view loraDatasetView
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	manifest := domain.LoraDatasetManifest{Version: 1, Settings: domain.LoraDatasetSettings{Name: "Portrait dataset", TriggerWord: "test_person", ConceptType: "character", Resolution: 1024}, Images: []domain.LoraDatasetImage{}}
	request(userID, http.MethodPost, "/api/lora-datasets", []byte(`{}`), "application/json", "", http.StatusForbidden)
	view := decodeView(post(userID, "/api/lora-datasets", loraDatasetRequest{ClientID: "create-retry", Manifest: manifest}, http.StatusCreated))
	retry := decodeView(post(userID, "/api/lora-datasets", loraDatasetRequest{ClientID: "create-retry", Manifest: manifest}, http.StatusCreated))
	if retry.Dataset.ID != view.Dataset.ID || retry.Dataset.Revision != view.Dataset.Revision {
		t.Fatal("repeated creation duplicated or changed the dataset")
	}
	base := "/api/lora-datasets/" + view.Dataset.ID
	request(foreignID, http.MethodGet, base, nil, "", "", http.StatusNotFound)
	imageBytes := datasetTestPNG(t, 768, 768)
	if len(imageBytes) <= loraDatasetChunkBytes {
		t.Fatal("fixture does not span chunks")
	}
	upload := func(payload []byte, want int) domain.LoraDatasetAsset {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("image", "portrait.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		w := request(userID, http.MethodPost, base+"/assets", body.Bytes(), writer.FormDataContentType(), csrf, want)
		var response struct {
			Asset domain.LoraDatasetAsset `json:"asset"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Asset
	}
	asset := upload(imageBytes, http.StatusCreated)
	if asset.Width != 768 || asset.Height != 768 || asset.MIMEType != "image/png" {
		t.Fatalf("uploaded metadata: %+v", asset)
	}
	if duplicate := upload(imageBytes, http.StatusCreated); duplicate.ID != asset.ID {
		t.Fatalf("upload dedup: %+v", duplicate)
	}
	upload(imageBytes[:len(imageBytes)-20], http.StatusBadRequest)
	upload(datasetTestPNG(t, 128, 128), http.StatusBadRequest)
	upload([]byte("not an image"), http.StatusBadRequest)
	manifest.Images = []domain.LoraDatasetImage{{ID: "frame-1", AssetID: asset.ID, Caption: "  private handwritten caption\n"}, {ID: "frame-2", AssetID: asset.ID, Excluded: true}}
	view = decodeView(post(userID, base+"/save", loraDatasetRequest{Revision: view.Dataset.Revision, Manifest: manifest}, http.StatusOK))
	if len(view.Warnings) != 3 {
		t.Fatalf("quality warnings: %+v", view.Warnings)
	}
	staleRevision := view.Dataset.Revision
	post(userID, base+"/save", loraDatasetRequest{Revision: staleRevision - 1, Manifest: manifest}, http.StatusConflict)
	stored, err := app.store.LoraDataset(ctx, userID, view.Dataset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored.ManifestCipher, []byte("private handwritten")) {
		t.Fatal("caption persisted as plaintext")
	}
	reloaded := decodeView(request(userID, http.MethodGet, base, nil, "", "", http.StatusOK))
	if reloaded.Manifest.Images[0].Caption != manifest.Images[0].Caption || !reloaded.Manifest.Images[1].Excluded {
		t.Fatalf("reload changed manual state: %+v", reloaded.Manifest)
	}
	assertLoraDatasetZIPRoundTrip(t, ctx, app, userID, foreignID, reloaded, session, csrf)
	var encrypted []byte
	if err := db.QueryRowContext(ctx, `SELECT payload_cipher FROM lora_dataset_asset_chunks WHERE asset_id=$1 AND chunk_index=0`, asset.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(encrypted[:8], imageBytes[:8]) {
		t.Fatal("image stored as plaintext")
	}
	read := request(userID, http.MethodGet, "/api/lora-datasets/assets/"+asset.ID, nil, "", "", http.StatusOK)
	if !bytes.Equal(read.Body.Bytes(), imageBytes) {
		t.Fatal("image round trip changed bytes")
	}
	request(foreignID, http.MethodGet, "/api/lora-datasets/assets/"+asset.ID, nil, "", "", http.StatusNotFound)
	var versionResponse struct {
		Version domain.LoraDatasetSnapshot `json:"version"`
	}
	if err := json.Unmarshal(post(userID, base+"/versions", loraDatasetRequest{Revision: view.Dataset.Revision}, http.StatusCreated).Body.Bytes(), &versionResponse); err != nil {
		t.Fatal(err)
	}
	snapshot := versionResponse.Version
	manifest.Images[0].Caption = "changed after snapshot"
	view = decodeView(post(userID, base+"/save", loraDatasetRequest{Revision: view.Dataset.Revision, Manifest: manifest}, http.StatusOK))
	var versionView struct {
		Manifest domain.LoraDatasetManifest `json:"manifest"`
	}
	if err := json.Unmarshal(request(userID, http.MethodGet, "/api/lora-datasets/versions/"+snapshot.ID, nil, "", "", http.StatusOK).Body.Bytes(), &versionView); err != nil {
		t.Fatal(err)
	}
	if versionView.Manifest.Images[0].Caption == manifest.Images[0].Caption {
		t.Fatal("immutable version changed")
	}
	post(userID, base+"/delete", loraDatasetRequest{Revision: staleRevision}, http.StatusConflict)
	post(userID, base+"/delete", loraDatasetRequest{Revision: view.Dataset.Revision}, http.StatusOK)
	request(userID, http.MethodGet, base, nil, "", "", http.StatusNotFound)
	request(userID, http.MethodGet, "/api/lora-datasets/versions/"+snapshot.ID, nil, "", "", http.StatusOK)
	post(foreignID, "/api/lora-datasets/versions/"+snapshot.ID+"/restore", nil, http.StatusNotFound)
	restored := decodeView(post(userID, "/api/lora-datasets/versions/"+snapshot.ID+"/restore", nil, http.StatusCreated))
	if restored.Dataset.ID == snapshot.DatasetID || restored.Manifest.Images[0].Caption != versionView.Manifest.Images[0].Caption || len(restored.Assets) != 1 {
		t.Fatalf("version restoration changed content: %+v", restored)
	}
	request(foreignID, http.MethodGet, "/api/lora-datasets/versions/"+snapshot.ID+"/export", nil, "", "", http.StatusNotFound)
	request(userID, http.MethodGet, "/api/lora-datasets/versions/"+snapshot.ID+"/export", nil, "", "", http.StatusOK)
	post(userID, "/api/lora-datasets/"+restored.Dataset.ID+"/delete", loraDatasetRequest{Revision: restored.Dataset.Revision}, http.StatusOK)
	post(foreignID, "/api/lora-datasets/versions/"+snapshot.ID+"/delete", nil, http.StatusNotFound)
	post(userID, "/api/lora-datasets/versions/"+snapshot.ID+"/delete", nil, http.StatusOK)
	if entries, err := os.ReadDir(app.mediaSpoolDir()); err != nil || len(entries) != 0 {
		t.Fatalf("spool leak: %v %v", entries, err)
	}
	// A copied gallery image remains a dataset asset after gallery retention.
	view = decodeView(post(userID, "/api/lora-datasets", loraDatasetRequest{Manifest: domain.LoraDatasetManifest{Version: 1}}, http.StatusCreated))
	base = "/api/lora-datasets/" + view.Dataset.ID
	eventID, err := app.store.InsertContentEvent(ctx, domain.ContentEventRecord{UserID: userID, Service: "comfyui", Kind: "comfyui_prompt", PromptCipher: []byte{1}, ResponseCipher: []byte{1}, MetadataCipher: []byte{1}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := app.contentCipher.EncryptBytes(imageBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.InsertContentMedia(ctx, domain.ContentMediaRecord{EventID: eventID, MediaType: "image", MIMEType: "image/png", OriginalName: "gallery.png", StorageType: "output", PayloadCipher: cipher, SizeBytes: int64(len(imageBytes)), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var mediaID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM content_media WHERE event_id=$1`, eventID).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	post(userID, base+"/reuse", loraDatasetRequest{MediaID: mediaID}, http.StatusCreated)
	if _, err := db.ExecContext(ctx, `DELETE FROM content_events WHERE id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
	request(userID, http.MethodGet, "/api/lora-datasets/assets/"+asset.ID, nil, "", "", http.StatusOK)
	post(userID, base+"/reuse", loraDatasetRequest{MediaID: mediaID}, http.StatusNotFound)
	// The API queues a versioned archive; this fake agent only exposes profiles
	// and never executes training or touches a GPU.
	manifest = domain.LoraDatasetManifest{Version: 1, Settings: domain.LoraDatasetSettings{Name: "Training fixture", OutputName: "training_fixture", TriggerWord: "fixture_person", ConceptType: "character", ProfileID: "krea2-fixture", Preset: "quick", Resolution: 512}}
	for i := 0; i < 6; i++ {
		manifest.Images = append(manifest.Images, domain.LoraDatasetImage{ID: fmt.Sprintf("training-%d", i), AssetID: asset.ID, Caption: fmt.Sprintf("original caption %d", i), Excluded: i == 1})
	}
	view = decodeView(post(userID, base+"/save", loraDatasetRequest{Revision: view.Dataset.Revision, Manifest: manifest}, http.StatusOK))
	post(userID, base+"/train", loraDatasetRequest{Revision: view.Dataset.Revision}, http.StatusConflict)
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/profiles" {
			t.Errorf("unexpected training call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, loratraining.ProfilesResponse{Available: true, Profiles: []loratraining.Profile{{ID: "krea2-fixture", Family: "krea2", BaseModel: "raw-fixture.safetensors", Ready: true}}})
	}))
	defer agent.Close()
	agentURL, err := url.Parse(agent.URL)
	if err != nil {
		t.Fatal(err)
	}
	app.loraTraining = loratraining.NewClient(agentURL, strings.Repeat("x", 32))
	var trainingResponse struct {
		Job     loraTrainingJobJSON        `json:"job"`
		Version domain.LoraDatasetSnapshot `json:"version"`
	}
	if err := json.Unmarshal(post(userID, base+"/train", loraDatasetRequest{Revision: view.Dataset.Revision}, http.StatusCreated).Body.Bytes(), &trainingResponse); err != nil {
		t.Fatal(err)
	}
	job, err := app.store.LoraTrainingJobByPublicID(ctx, trainingResponse.Job.ID, userID, false)
	if err != nil {
		t.Fatal(err)
	}
	if job.SampleCount != 5 || job.DatasetSnapshotID == "" || job.DatasetSnapshotHash != trainingResponse.Version.Hash || trainingResponse.Job.DatasetSnapshotID != job.DatasetSnapshotID {
		t.Fatalf("queued dataset provenance: %+v %+v", job, trainingResponse)
	}
	post(userID, "/api/lora-datasets/versions/"+job.DatasetSnapshotID+"/delete", nil, http.StatusConflict)
	manifest.Images[0].Caption = "manual edit after training started"
	view = decodeView(post(userID, base+"/save", loraDatasetRequest{Revision: view.Dataset.Revision, Manifest: manifest}, http.StatusOK))
	post(userID, base+"/train", loraDatasetRequest{Revision: view.Dataset.Revision}, http.StatusConflict)
	archive, err := zip.OpenReader(job.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 10 {
		t.Fatalf("archive entries: %d", len(archive.File))
	}
	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(entry.Name, ".txt") {
			if !bytes.HasPrefix(data, []byte("fixture_person")) || bytes.Contains(data, []byte("original caption 1")) || bytes.Contains(data, []byte("manual edit")) {
				t.Fatalf("snapshot caption changed: %s %q", entry.Name, data)
			}
		} else if !bytes.Equal(data, imageBytes) {
			t.Fatalf("snapshot image changed: %s", entry.Name)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	post(userID, base+"/delete", loraDatasetRequest{Revision: view.Dataset.Revision}, http.StatusOK)
	if _, err := os.Stat(job.DatasetPath); err != nil {
		t.Fatalf("working dataset deletion removed queued archive: %v", err)
	}
	if _, err := app.store.RequestLoraTrainingCancellation(ctx, job.PublicID, userID, false); err != nil {
		t.Fatal(err)
	}
	app.removeLoraTrainingDataset(job)
	if deleted, err := app.store.DeleteTerminalLoraTrainingJob(ctx, job.ID); err != nil || !deleted {
		t.Fatalf("cleanup fixture job: %v %v", deleted, err)
	}
	if err := os.Remove(filepath.Join(app.mediaSpoolDir(), "lora-training")); err != nil {
		t.Fatalf("failed submission leaked a training archive: %v", err)
	}
	// Authentication tags reject swapped/corrupt chunks before any response bytes.
	if _, err := db.ExecContext(ctx, `UPDATE lora_dataset_asset_chunks SET payload_cipher=$2 WHERE asset_id=$1 AND chunk_index=0`, asset.ID, []byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	request(userID, http.MethodGet, "/api/lora-datasets/assets/"+asset.ID, nil, "", "", http.StatusInternalServerError)
	if entries, err := os.ReadDir(app.mediaSpoolDir()); err != nil || len(entries) != 0 {
		t.Fatalf("failed materialization leaked spool: %v %v", entries, err)
	}
	if err := app.store.ForEachLoraDatasetAssetChunk(ctx, foreignID, asset.ID, func(int, []byte, int) error { return io.EOF }); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}
