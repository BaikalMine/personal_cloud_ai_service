package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestGenerationCollectionPayload(t *testing.T) {
	cipher, err := contentcrypto.NewCipher("01234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{contentCipher: cipher}
	input := generationDraftPayload{Version: 1, Values: map[string]string{
		"positive_prompt": "  prompt\n", "lora_1": "model.safetensors", "lora_model_strength_1": "0,",
		"image_source_id_1": "42", "image_source_name_1": "portrait.png", "image_role_1": "style", "batch_count": "4", "quality_preset": "balanced",
		"collection_id": "55", "parent_job_id": "old", "correlation_id": "old", "client_request_id": "old", "csrf": "secret",
		"assistant_job_id": "old", "assistant_draft": "old", "lora_verification_source": "old", "lora_verification_plan": "old", "user_id": "99",
	}, Assets: []generationDraftAsset{{Field: "input_image", ID: "owned-source", Name: "portrait.png"}}}
	encode := func(payload generationDraftPayload) []byte {
		t.Helper()
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		value, err := cipher.Encrypt(string(encoded))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	output, err := app.generationCollectionPayload(encode(input))
	if err != nil || len(output.Values) != 8 || output.Values["positive_prompt"] != "  prompt\n" || output.Values["lora_model_strength_1"] != "0," || len(output.Assets) != 1 {
		t.Fatal(output, err)
	}
	for _, field := range []string{"collection_id", "parent_job_id", "correlation_id", "client_request_id", "csrf", "assistant_job_id", "assistant_draft", "lora_verification_source", "lora_verification_plan", "user_id"} {
		if _, ok := output.Values[field]; ok {
			t.Fatal("non-reusable field copied", field)
		}
	}
	input.Assets = append(input.Assets, input.Assets[0])
	if _, err := app.generationCollectionPayload(encode(input)); err == nil {
		t.Fatal("duplicate source accepted")
	}
	for _, values := range []url.Values{{"collection_id": {"-1"}}, {"collection_id": {"NaN"}}, {"collection_id": {"1", "2"}}, {"collection_id": {"9223372036854775808"}}} {
		if _, err := generationCollectionID(values); err == nil {
			t.Fatal("invalid ID accepted", values)
		}
	}
}

func assertGenerationCollectionContextAPI(t *testing.T, ctx context.Context, db *sql.DB, app *App, owner, foreign int64) {
	t.Helper()
	app.csrfSigner = security.NewCSRFSigner("collection-integration-only")
	const session = "collection-session"
	csrf := app.csrfSigner.Token(session)
	request := func(user int64, method, target string, values url.Values, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: user, Role: "user"}))
		w := httptest.NewRecorder()
		if r.URL.Path == "/generate/draft" {
			app.handleGenerationDraft(w, r)
		} else {
			app.handleGenerationCollectionContext(w, r)
		}
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, target, w.Code, want, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("context can be cached")
		}
		return w
	}
	decode := func(w *httptest.ResponseRecorder) generationCollectionContextView {
		t.Helper()
		var response struct {
			Collection generationCollectionContextView `json:"collection"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Collection
	}
	collection, err := app.store.CreateGenerationMediaCollection(ctx, owner, "Working series")
	if err != nil {
		t.Fatal(err)
	}
	const endpoint = "/generate/library/collections/context"
	getURL := endpoint + "?collection_id=" + strconv.FormatInt(collection.ID, 10)
	view := decode(request(owner, "GET", getURL, nil, 200))
	if view.HasContext || view.Revision != collection.Revision {
		t.Fatal("new collection", view)
	}
	form := url.Values{"csrf": {csrf}, "collection_id": {strconv.FormatInt(collection.ID, 10)}, "collection_revision": {strconv.FormatInt(view.Revision, 10)}, "action": {"open"}, "draft_revision": {"0"}, "name": {view.Name}}
	request(owner, "POST", endpoint, form, 409)
	request(owner, "PUT", getURL, nil, 405)
	request(foreign, "GET", getURL, nil, 404)
	request(foreign, "POST", endpoint, form, 404)
	request(owner, "POST", endpoint, url.Values{"collection_id": form["collection_id"]}, 403)
	const ownAsset = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	const foreignAsset = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2"
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for id, user := range map[string]int64{ownAsset: owner, foreignAsset: foreign} {
		folder := comfyUploadNamespace(app.comfyClientID(user))
		if err := app.store.ReserveComfyInputAsset(ctx, user, id, "collection.png", folder, 12, hash, store.ComfyInputQuota{}); err != nil {
			t.Fatal(err)
		}
		if ok, err := app.store.FinalizeComfyInputAsset(ctx, id, "collection.png", folder, 12, hash, time.Hour); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	sourceBefore, err := app.store.ComfyInputAssetForUser(ctx, owner, ownAsset)
	if err != nil {
		t.Fatal(err)
	}
	draftForm := url.Values{"csrf": {csrf}, "draft_revision": {"0"}, "positive_prompt": {"private collection prompt"}, "lora_1": {"Krea2/portrait.safetensors"},
		"lora_model_strength_1": {"0.7"}, "parent_job_id": {"old-job"}, "assistant_draft": {"old assistant"}, "collection_id": {"9999"},
		"draft_asset_input_image": {ownAsset}, "draft_asset_input_image_2": {foreignAsset}, "draft_pending_input_audio": {"voice.wav"}}
	request(owner, "POST", "/generate/draft", draftForm, 200)
	draft, err := app.store.GenerationDraft(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	form.Set("action", "save_draft")
	form.Set("draft_revision", strconv.FormatInt(draft.Revision, 10))
	view = decode(request(owner, "POST", endpoint, form, 200))
	if !view.HasContext || view.SavedAt == nil || view.Values["positive_prompt"] != "private collection prompt" || view.Values["lora_1"] == "" || len(view.Assets) != 3 || !view.Assets[0].Available || view.Assets[1].Available || view.Assets[2].Available {
		t.Fatal("saved context", view)
	}
	if view.Values["collection_id"] != "" || view.Values["parent_job_id"] != "" || view.Values["assistant_draft"] != "" || view.Assets[1].URL != "" || view.Assets[1].Value != "" {
		t.Fatal("context leaked identity or foreign source", view)
	}
	row, err := app.store.GenerationCollectionContext(ctx, owner, collection.ID)
	if err != nil || strings.Contains(string(row.PayloadCipher), "private collection prompt") {
		t.Fatal("unencrypted context", err)
	}
	request(owner, "POST", endpoint, form, 409)
	form.Set("collection_revision", strconv.FormatInt(view.Revision, 10))
	form.Set("action", "open")
	form.Set("draft_revision", "0")
	request(owner, "POST", endpoint, form, 409)
	form.Set("draft_revision", strconv.FormatInt(draft.Revision, 10))
	request(owner, "POST", endpoint, form, 200)
	request(owner, "POST", endpoint, form, 409)
	opened, err := app.store.GenerationDraft(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	openedView, err := app.generationDraftView(ctx, opened)
	if err != nil || openedView.Values["collection_id"] != strconv.FormatInt(collection.ID, 10) || openedView.Values["parent_job_id"] != "" {
		t.Fatal("opened draft", openedView, err)
	}
	sourceAfter, err := app.store.ComfyInputAssetForUser(ctx, owner, ownAsset)
	if err != nil || !sourceAfter.ExpiresAt.Equal(sourceBefore.ExpiresAt) {
		t.Fatal("collection silently extended retention", sourceBefore.ExpiresAt, sourceAfter.ExpiresAt, err)
	}
	if _, err := db.Exec(`UPDATE comfy_input_assets SET expires_at=now()-interval '1 second' WHERE id=$1`, ownAsset); err != nil {
		t.Fatal(err)
	}
	view = decode(request(owner, "GET", getURL, nil, 200))
	if view.Assets[0].Available || view.Assets[0].URL != "" || view.Assets[0].Value != "" || view.Assets[0].Name == "" {
		t.Fatal("expired asset restored", view.Assets)
	}
	form.Set("action", "rename")
	form.Set("name", "Renamed series")
	view = decode(request(owner, "POST", endpoint, form, 200))
	if view.Name != "Renamed series" || !view.HasContext || view.Values["positive_prompt"] != "private collection prompt" {
		t.Fatal("rename lost context", view)
	}
	request(owner, "POST", endpoint, form, 409)
	if err := app.store.DeleteGenerationDraft(ctx, owner, opened.Revision); err != nil {
		t.Fatal(err)
	}
	if ok, err := app.store.DeleteGenerationMediaCollection(ctx, owner, collection.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	request(owner, "GET", getURL, nil, 404)
}
