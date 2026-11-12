package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
)

func TestGalleryDatasetImportIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	f.app.csrfSigner = security.NewCSRFSigner("gallery-import-test")
	const session = "gallery-import-session"
	csrf := f.app.csrfSigner.Token(session)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	media := func(width int) string {
		t.Helper()
		data := datasetTestPNG(t, width, 256)
		cipher, err := f.app.contentCipher.EncryptBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		var event, id int64
		if err = f.db.QueryRow(`INSERT INTO content_events(user_id,service,kind,prompt_cipher,response_cipher,metadata_cipher) VALUES($1,'comfyui','comfyui_prompt','x','x','x') RETURNING id`, f.user.ID).Scan(&event); err != nil {
			t.Fatal(err)
		}
		if err = f.db.QueryRow(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,payload_cipher,size_bytes) VALUES($1,'image','image/png','source.png',$2,$3) RETURNING id`, event, cipher, len(data)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(id, 10)
	}
	first, second := media(300), media(301)
	call := func(owner int64, method, path string, body any, token string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/lora-datasets/gallery-import"+path, bytes.NewReader(data))
		r.Header.Set("X-CSRF-Token", token)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: owner, Role: "user", CanTrainImageLora: true}))
		w := httptest.NewRecorder()
		f.app.handleLoraDatasets(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) domain.GalleryDatasetImportReceipt {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
		var v struct {
			Receipt domain.GalleryDatasetImportReceipt `json:"receipt"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v.Receipt
	}
	input := galleryDatasetImportRequest{RequestID: "gallery-import-request-0001", MediaID: first, Name: "Original"}
	if w := call(f.user.ID, "POST", "", input, ""); w.Code != 403 {
		t.Fatal("CSRF", w.Code)
	}
	receipt := decode(call(f.user.ID, "POST", "", input, csrf))
	row, err := f.app.store.LoraDataset(ctx, f.user.ID, receipt.DatasetID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := f.app.decodeLoraDatasetManifest(row.ManifestCipher)
	if err != nil || len(manifest.Images) != 1 {
		t.Fatal(manifest, err)
	}
	manifest.Settings.TriggerWord = "preserved_trigger"
	manifest.Images[0].Caption = "manual caption"
	manifest.Images[0].Excluded = true
	cipher, _, err := f.app.encryptLoraDatasetManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	row, err = f.app.store.SaveLoraDataset(ctx, f.user.ID, row.ID, row.Revision, row.Name, cipher, []string{manifest.Images[0].AssetID})
	if err != nil {
		t.Fatal(err)
	}
	appendInput := galleryDatasetImportRequest{RequestID: "gallery-import-request-0002", MediaID: second, DatasetID: row.ID, Revision: row.Revision}
	appended := decode(call(f.user.ID, "POST", "", appendInput, csrf))
	row, err = f.app.store.LoraDataset(ctx, f.user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = f.app.decodeLoraDatasetManifest(row.ManifestCipher)
	if err != nil || len(manifest.Images) != 2 || manifest.Settings.TriggerWord != "preserved_trigger" || manifest.Images[0].Caption != "manual caption" || !manifest.Images[0].Excluded {
		t.Fatal("overwrote existing manifest", manifest, err)
	}
	if repeat := decode(call(f.user.ID, "POST", "", appendInput, csrf)); repeat.Revision != appended.Revision || repeat.ImageID != appended.ImageID {
		t.Fatal("not idempotent", repeat)
	}
	stale := appendInput
	stale.RequestID = "gallery-import-stale-0001"
	if w := call(f.user.ID, "POST", "", stale, csrf); w.Code != 409 {
		t.Fatal("lost revision guard", w.Code, w.Body.String())
	}
	duplicate := appendInput
	duplicate.RequestID = "gallery-import-duplicate-001"
	duplicate.Revision = row.Revision
	if got := decode(call(f.user.ID, "POST", "", duplicate, csrf)); !got.AlreadyPresent || got.Revision != row.Revision {
		t.Fatal("duplicate inserted", got)
	}
	changed := input
	changed.Name = "Different"
	if w := call(f.user.ID, "POST", "", changed, csrf); w.Code != 409 {
		t.Fatal("request rebound", w.Code)
	}
	var foreign int64
	if err = f.db.QueryRow(`INSERT INTO users(username,password_hash,role,can_train_image_lora) VALUES('gallery-foreign','x','user',true) RETURNING id`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if w := call(foreign, "GET", "?request="+input.RequestID, nil, csrf); w.Code != 404 {
		t.Fatal("foreign receipt leak", w.Code)
	}
	foreignInput := input
	foreignInput.RequestID = "gallery-import-foreign-0001"
	if w := call(foreign, "POST", "", foreignInput, csrf); w.Code != 404 {
		t.Fatal("foreign media accepted", w.Code)
	}
	foreignInput.DatasetID = row.ID
	foreignInput.Revision = row.Revision
	if w := call(foreign, "POST", "", foreignInput, csrf); w.Code != 404 {
		t.Fatal("foreign dataset accepted", w.Code)
	}
	cancelled := galleryDatasetImportRequest{RequestID: "gallery-import-discard-0001", MediaID: first, Name: "Must not exist"}
	if got := decode(call(f.user.ID, "POST", "/discard", cancelled, csrf)); !got.Cancelled {
		t.Fatal(got)
	}
	if got := decode(call(f.user.ID, "POST", "", cancelled, csrf)); !got.Cancelled {
		t.Fatal("late send revived discarded request", got)
	}
	if got := decode(call(f.user.ID, "POST", "/discard", input, csrf)); got.Cancelled || got.DatasetID != receipt.DatasetID {
		t.Fatal("discard hid committed import", got)
	}
	concurrent := galleryDatasetImportRequest{RequestID: "gallery-import-concurrent-01", MediaID: second, Name: "Concurrent"}
	var wg sync.WaitGroup
	answers := make(chan *httptest.ResponseRecorder, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); answers <- call(f.user.ID, "POST", "", concurrent, csrf) }()
	}
	wg.Wait()
	close(answers)
	var destination string
	for answer := range answers {
		got := decode(answer)
		if destination != "" && destination != got.DatasetID {
			t.Fatal("duplicate datasets", destination, got.DatasetID)
		}
		destination = got.DatasetID
	}
	var count int
	if err = f.db.QueryRow(`SELECT count(*) FROM lora_datasets WHERE name='Concurrent'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	exec(`DELETE FROM lora_datasets WHERE id=$1`, receipt.DatasetID)
	exec(`DELETE FROM content_media WHERE id=$1`, first)
	if got := decode(call(f.user.ID, "POST", "", input, csrf)); got.DatasetID != receipt.DatasetID {
		t.Fatal("receipt lost after deletion", got)
	}
	if got := decode(call(f.user.ID, "GET", "?request="+input.RequestID, nil, csrf)); got.RequestID != input.RequestID {
		t.Fatal(got)
	}
	exec(`UPDATE users SET role='user',can_train_image_lora=false WHERE id=$1`, f.user.ID)
	denied := galleryDatasetImportRequest{RequestID: "gallery-import-denied-0001", MediaID: second, Name: "Denied"}
	if w := call(f.user.ID, "POST", "", denied, csrf); w.Code != 403 {
		t.Fatal("stale permission accepted", w.Code, w.Body.String())
	}
	if f.posts.Load() != 0 {
		t.Fatal("import started GPU work", f.posts.Load())
	}
	for _, table := range []string{"generation_jobs", "gpu_work"} {
		if err := f.db.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("unexpected work", table, count)
		}
	}
}

func TestGalleryDestinationPresets(t *testing.T) {
	user := &User{Role: "user", CanUseQuickGeneration: true, CanGenerateImageToImage: true, CanGenerateVideo: true}
	base := generationPreset{ID: "edit", TemplateID: "image-to-image", Name: "Edit", Available: true, AllowsImages: true, MaxInputImages: 4}
	input := []generationPreset{base}
	for _, modify := range []func(*generationPreset){func(p *generationPreset) { p.Available = false }, func(p *generationPreset) { p.Restricted = true }, func(p *generationPreset) { p.AdminOnly = true }, func(p *generationPreset) { p.AllowsImages = false }, func(p *generationPreset) { p.MaxInputImages = 0 }} {
		copy := base
		modify(&copy)
		input = append(input, copy)
	}
	if got := galleryDestinationPresets(input, user); len(got) != 1 || got[0].ID != "edit" {
		t.Fatal(got)
	}
	user.CanGenerateImageToImage = false
	if got := galleryDestinationPresets(input, user); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestGalleryCompatibleDestinations(t *testing.T) {
	user := &User{Role: "user", CanUseQuickGeneration: true, CanGenerateImageToImage: true, CanGenerateVideo: true}
	models := []generationModel{
		{ID: "krea:text", Family: modelFamilyKrea2, Available: true},
		{ID: "krea:edit", Family: modelFamilyKrea2, Available: true, SupportsImage: true},
		{ID: "flux:edit", Family: modelFamilyFlux2, Available: true, SupportsImage: true},
		{ID: "flux:offline", Family: modelFamilyFlux2, SupportsImage: true},
		{ID: "video:frames", Family: modelFamilyMiniMaxH3, Available: true},
		{ID: "video:refs", Family: modelFamilyMiniMaxH3, Available: true, VideoReferenceOnly: true},
	}
	catalog := generationModelCatalog{Online: true, Groups: []generationModelGroup{{Models: models}}}
	got := galleryCompatibleDestinations(catalog, domain.GenerationAccessPolicy{}, user)
	if len(got) != 4 {
		t.Fatalf("expected both editors and both video models: %+v", got)
	}
	seen := map[string]bool{}
	for _, item := range got {
		if seen[item.Key] || item.Key != item.ID+":"+item.ModelID || item.Maximum <= 0 {
			t.Fatalf("invalid destination identity/capacity: %+v", item)
		}
		seen[item.Key] = true
		if item.Frames != (item.ModelID != "video:refs") {
			t.Fatalf("reference-only frame capability: %+v", item)
		}
		if item.ModelID == "krea:text" || item.ModelID == "flux:offline" {
			t.Fatalf("incompatible model exposed: %+v", item)
		}
	}
	policy := domain.GenerationAccessPolicy{ModelIDs: []string{"video:refs", "flux:edit"}, PresetIDs: []string{"minimax-h3-video"}}
	got = galleryCompatibleDestinations(catalog, policy, user)
	if len(got) != 1 || got[0].ModelID != "video:refs" || got[0].Frames {
		t.Fatalf("effective model and recipe restrictions ignored: %+v", got)
	}
	user.CanGenerateVideo = false
	if got = galleryCompatibleDestinations(catalog, policy, user); len(got) != 0 {
		t.Fatalf("revoked video access ignored: %+v", got)
	}
}
