package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/store"
)

func TestGenerationComparisonStaticAssets(t *testing.T) {
	mux := http.NewServeMux()
	(&App{}).registerStaticRoutes(mux)
	for _, asset := range []string{"static/image-comparison.js", "static/image-comparison.css"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+asset, nil))
		want, err := embeddedFS.ReadFile(asset)
		if err != nil || w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) {
			t.Fatalf("production asset %s: status %d, error %v", asset, w.Code, err)
		}
		versioned := false
		for _, path := range frontendAssetPaths {
			versioned = versioned || path == asset
		}
		if !versioned {
			t.Fatalf("asset missing from cache version: %s", asset)
		}
	}
}

func TestGenerationComparisonBoundary(t *testing.T) {
	for _, tc := range []struct {
		method string
		user   *User
		query  string
		status int
	}{
		{"POST", nil, "", 405}, {"GET", nil, "", 401}, {"GET", &User{ID: 1}, "", 403},
		{"GET", &User{ID: 1, Role: "admin", Disabled: true}, "", 401},
		{"GET", &User{ID: 1, Role: "admin", AccountExpiresAt: sql.NullTime{Valid: true, Time: time.Now().Add(-time.Hour)}}, "", 401},
		{"GET", &User{ID: 1, Role: "admin"}, "?job=bad", 400},
		{"GET", &User{ID: 1, Role: "admin"}, "?job=comparison-fixture-001&job=comparison-fixture-002", 400},
		{"GET", &User{ID: 1, Role: "admin"}, "?job=comparison-fixture-001&image=2", 400},
	} {
		r := httptest.NewRequest(tc.method, "/generate/jobs/comparison"+tc.query, nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, tc.user))
		w := httptest.NewRecorder()
		(&App{}).handleGenerationComparison(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("boundary: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestGenerationComparisonOwnedSourceIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	job := f.enqueue(t)
	const assetID = "11111111111111111111111111111111"
	const digest = "1111111111111111111111111111111111111111111111111111111111111111"
	folder := comfyUploadNamespace(f.app.comfyClientID(f.user.ID))
	if err := f.app.store.ReserveComfyInputAsset(ctx, f.user.ID, assetID, "original.png", folder, 12, digest, store.ComfyInputQuota{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.app.store.FinalizeComfyInputAsset(ctx, assetID, "original.png", folder, 12, digest, time.Hour); err != nil || !ok {
		t.Fatalf("finalize: %v %v", ok, err)
	}
	call := func(owner int64, want int) string {
		t.Helper()
		r := httptest.NewRequest("GET", "/generate/jobs/comparison?job="+job.PublicID, nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: owner, Role: "admin"}))
		w := httptest.NewRecorder()
		f.app.handleGenerationComparison(w, r)
		if w.Code != want {
			t.Fatalf("comparison status: %d want %d: %s", w.Code, want, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-prompt") || strings.Contains(w.Body.String(), folder) || strings.Contains(w.Body.String(), "reference-2") {
			t.Fatal("comparison leaked the recipe or a secondary reference")
		}
		return w.Body.String()
	}
	for _, tc := range []struct {
		template, state, input string
		available              bool
	}{
		{"image-to-image", "completed", folder + "/original.png", true},
		{"text-to-image", "completed", folder + "/original.png", false},
		{"image-to-image", "running", folder + "/original.png", false},
		{"image-to-image", "completed", folder + "/missing.png", false},
		{"image-to-image", "completed", comfyUploadNamespace(f.app.comfyClientID(f.user.ID+99)) + "/original.png", false},
		{"image-to-image", "completed", "../original.png", false},
	} {
		payload, err := json.Marshal(generationSavedPayload{Version: 1, Values: map[string]string{
			"input_image": tc.input, "positive_prompt": "private-prompt", "input_image_2": "reference-2.png",
		}})
		if err != nil {
			t.Fatal(err)
		}
		cipher, err := f.app.contentCipher.Encrypt(string(payload))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.Exec(`UPDATE generation_jobs SET template_id=$2,state=$3,payload_cipher=$4 WHERE id=$1`, job.ID, tc.template, tc.state, cipher); err != nil {
			t.Fatal(err)
		}
		body := call(f.user.ID, 200)
		var result struct {
			Available bool `json:"available"`
			Original  struct {
				URL string `json:"url"`
			} `json:"original"`
		}
		if err = json.Unmarshal([]byte(body), &result); err != nil || result.Available != tc.available {
			t.Fatalf("availability: %s %v", body, err)
		}
		if tc.available && result.Original.URL != "/generate/draft/asset?id="+assetID || !tc.available && result.Original.URL != "" {
			t.Fatalf("source mismatch: %s", body)
		}
		call(f.user.ID+99, 404)
	}
	payload, err := f.app.generationJobPayloadCipher(generationForm{}, generationJobFormValues(map[string]string{"input_image": folder + "/original.png"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE generation_jobs SET payload_cipher=$2 WHERE id=$1`, job.ID, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE comfy_input_assets SET expires_at=now()-interval '1 minute' WHERE id=$1`, assetID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(call(f.user.ID, 200), assetID) {
		t.Fatal("expired source retained a URL")
	}
	if f.posts.Load() != 0 {
		t.Fatal("comparison dispatched GPU work")
	}
}
