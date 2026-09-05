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

	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestGenerationDraftValues(t *testing.T) {
	values, err := generationDraftValues(url.Values{
		"positive_prompt": {"  unfinished\n"}, "lora_1": {"Krea2/model.safetensors"},
		"lora_model_strength_1": {"0,"}, "video_sage_attention": {"false"}, "batch_count": {"12"},
		"assistant_enabled": {"true"}, "assistant_draft": {"edited suggestion"}, "image_role_4": {"style"},
		"csrf": {"secret"}, "client_request_id": {"request"}, "input_audio": {"private/path"},
		"input_image_2": {"private/path"}, "user_id": {"99"}, "unrecognized": {"value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 8 || values["positive_prompt"] != "  unfinished\n" || values["lora_model_strength_1"] != "0," {
		t.Fatalf("draft fields: %#v", values)
	}
	if _, err := generationDraftValues(url.Values{"positive_prompt": {strings.Repeat("x", (64<<10)+1)}}); err == nil {
		t.Fatal("oversize field accepted")
	}
}

func assertGenerationDraftAPI(t *testing.T, ctx context.Context, db *sql.DB, app *App, userID, foreignID int64) {
	t.Helper()
	app.csrfSigner = security.NewCSRFSigner("draft-integration-only")
	const session = "draft-session"
	csrf := app.csrfSigner.Token(session)
	request := func(user int64, method, target string, form url.Values, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: user, Role: "user"}))
		w := httptest.NewRecorder()
		switch r.URL.Path {
		case "/generate/draft/delete":
			app.handleDeleteGenerationDraft(w, r)
		case "/generate/draft/asset":
			app.handleGenerationDraftAsset(w, r)
		default:
			app.handleGenerationDraft(w, r)
		}
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, target, w.Code, want, w.Body.String())
		}
		return w
	}
	decode := func(w *httptest.ResponseRecorder) *generationDraftView {
		t.Helper()
		var response struct {
			Draft *generationDraftView `json:"draft"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Draft
	}
	const assetID = "11111111111111111111111111111111"
	const foreignAssetID = "22222222222222222222222222222222"
	const hash = "1111111111111111111111111111111111111111111111111111111111111111"
	for id, owner := range map[string]int64{assetID: userID, foreignAssetID: foreignID} {
		folder := comfyUploadNamespace(app.comfyClientID(owner))
		if err := app.store.ReserveComfyInputAsset(ctx, owner, id, "source.png", folder, 12, hash, store.ComfyInputQuota{}); err != nil {
			t.Fatal(err)
		}
		if ok, err := app.store.FinalizeComfyInputAsset(ctx, id, "source.png", folder, 12, hash, time.Hour); err != nil || !ok {
			t.Fatalf("finalize: %v %v", ok, err)
		}
	}
	form := url.Values{"csrf": {csrf}, "draft_revision": {"0"}, "positive_prompt": {"draft private prompt"},
		"lora_1": {"Krea2/portrait.safetensors"}, "lora_model_strength_1": {"0.75"},
		"input_image":            {comfyUploadNamespace(app.comfyClientID(userID)) + "/source.png"},
		"draft_name_input_image": {"my portrait.png"}, "draft_asset_input_image_2": {foreignAssetID},
		"draft_pending_input_audio": {"voice.wav"}, "assistant_enabled": {"true"}, "assistant_draft": {"manual edits"},
	}
	request(userID, http.MethodPost, "/generate/draft", url.Values{"draft_revision": {"0"}}, http.StatusForbidden)
	created := decode(request(userID, http.MethodPost, "/generate/draft", form, http.StatusOK))
	if created == nil || created.Revision == 0 || len(created.Assets) != 3 {
		t.Fatalf("created draft: %+v", created)
	}
	if !created.Assets[0].Available || created.Assets[0].ID != assetID || created.Assets[1].Available || created.Assets[1].Value != "" || created.Assets[2].Available {
		t.Fatalf("asset ownership/pending: %+v", created.Assets)
	}
	row, err := app.store.GenerationDraft(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(row.PayloadCipher), "draft private prompt") {
		t.Fatal("draft was stored in plaintext")
	}
	restored := decode(request(userID, http.MethodGet, "/generate/draft", nil, http.StatusOK))
	if restored.Values["lora_model_strength_1"] != "0.75" || restored.Values["assistant_draft"] != "manual edits" {
		t.Fatalf("restore: %+v", restored)
	}
	if decode(request(foreignID, http.MethodGet, "/generate/draft", nil, http.StatusOK)) != nil {
		t.Fatal("foreign draft leaked")
	}
	request(foreignID, http.MethodGet, "/generate/draft/asset?id="+assetID, nil, http.StatusNotFound)
	request(userID, http.MethodGet, "/generate/draft/asset?id="+foreignAssetID, nil, http.StatusNotFound)
	request(userID, http.MethodPost, "/generate/draft", form, http.StatusConflict)
	foreignForm := url.Values{"csrf": {csrf}, "draft_revision": {"0"}, "input_image": {comfyUploadNamespace(app.comfyClientID(foreignID)) + "/source.png"}}
	request(userID, http.MethodPost, "/generate/draft", foreignForm, http.StatusBadRequest)
	if _, err := db.ExecContext(ctx, `UPDATE comfy_input_assets SET expires_at=now()-interval '1 second' WHERE id=$1`, assetID); err != nil {
		t.Fatal(err)
	}
	expired := decode(request(userID, http.MethodGet, "/generate/draft", nil, http.StatusOK))
	if expired.Assets[0].Available || expired.Assets[0].Name != "my portrait.png" || expired.Assets[0].Value != "" {
		t.Fatalf("expired asset: %+v", expired.Assets[0])
	}
	request(userID, http.MethodGet, "/generate/draft/asset?id="+assetID, nil, http.StatusNotFound)
	form.Del("input_image")
	form.Set("draft_asset_input_image", assetID)
	form.Set("draft_revision", strconv.FormatInt(created.Revision, 10))
	updated := decode(request(userID, http.MethodPost, "/generate/draft", form, http.StatusOK))
	request(userID, http.MethodPost, "/generate/draft/delete", url.Values{"csrf": {csrf}, "draft_revision": {strconv.FormatInt(created.Revision, 10)}}, http.StatusConflict)
	request(foreignID, http.MethodPost, "/generate/draft/delete", url.Values{"csrf": {csrf}, "draft_revision": {strconv.FormatInt(updated.Revision, 10)}}, http.StatusConflict)
	request(userID, http.MethodPost, "/generate/draft/delete", url.Values{"csrf": {csrf}, "draft_revision": {strconv.FormatInt(updated.Revision, 10)}}, http.StatusOK)
	if decode(request(userID, http.MethodGet, "/generate/draft", nil, http.StatusOK)) != nil {
		t.Fatal("deleted draft restored")
	}
	if _, err := app.store.ComfyInputAssetForUser(ctx, foreignID, foreignAssetID); err != nil {
		t.Fatalf("deleting draft deleted another media: %v", err)
	}
}
