package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestGalleryExactLookupIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	const mediaID = "9007199254740993"
	var event int64
	if err := f.db.QueryRow(`INSERT INTO content_events(user_id,service,kind,prompt_cipher,response_cipher,metadata_cipher) VALUES($1,'comfyui','comfyui_prompt','x','x','x') RETURNING id`, f.user.ID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO content_media(id,event_id,media_type,mime_type,original_name,payload_cipher,size_bytes,created_at)
		VALUES($1,$2,'image','image/png','older-result.png','x',100,now()-interval '1 day')`, mediaID, event)
	exec(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,payload_cipher,size_bytes)
		SELECT $1,'image','image/png','newer-result-'||n||'.png','x',100 FROM generate_series(1,101) n`, event)
	call := func(owner int64, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/generate/library/images"+query, nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: owner}))
		w := httptest.NewRecorder()
		f.app.handleGenerationLibraryImages(w, r)
		return w
	}
	var list struct {
		MediaID string                      `json:"media_id"`
		Images  []generationPickerImageView `json:"images"`
	}
	w := call(f.user.ID, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Images) != 100 {
		t.Fatalf("bounded picker: %d %s", w.Code, w.Body.String())
	}
	for _, image := range list.Images {
		if image.Filename == "older-result.png" {
			t.Fatal("old result unexpectedly in the latest 100")
		}
	}
	w = call(f.user.ID, "?media="+mediaID)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || list.MediaID != mediaID || len(list.Images) != 1 || list.Images[0].Filename != "older-result.png" || list.Images[0].URL != "/generate/library/"+mediaID {
		t.Fatalf("exact bigint lookup: %d %s", w.Code, w.Body.String())
	}
	if w := call(f.user.ID+100, "?media="+mediaID); w.Code != 404 {
		t.Fatalf("foreign owner: %d %s", w.Code, w.Body.String())
	}
	for _, query := range []string{"?media=0", "?media=-1", "?media=x", "?media=1&media=2", "?media=1&extra=2"} {
		if w := call(f.user.ID, query); w.Code != 400 {
			t.Fatalf("malformed query %s: %d", query, w.Code)
		}
	}
	for _, mutation := range []struct{ change, undo string }{
		{`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, `UPDATE content_media SET profile_hidden_at=NULL WHERE id=$1`},
		{`UPDATE content_media SET expires_at=now()-interval '1 second' WHERE id=$1`, `UPDATE content_media SET expires_at=now()+interval '1 day' WHERE id=$1`},
		{`UPDATE content_media SET media_type='video' WHERE id=$1`, `UPDATE content_media SET media_type='image' WHERE id=$1`},
		{`UPDATE content_events SET expires_at=now()-interval '1 second' WHERE id=(SELECT event_id FROM content_media WHERE id=$1)`, `UPDATE content_events SET expires_at=now()+interval '1 day' WHERE id=(SELECT event_id FROM content_media WHERE id=$1)`},
		{`UPDATE content_events SET service='openwebui' WHERE id=(SELECT event_id FROM content_media WHERE id=$1)`, `UPDATE content_events SET service='comfyui' WHERE id=(SELECT event_id FROM content_media WHERE id=$1)`},
	} {
		exec(mutation.change, mediaID)
		if w := call(f.user.ID, "?media="+mediaID); w.Code != 404 {
			t.Fatalf("unavailable media exposed after %s: %d %s", mutation.change, w.Code, w.Body.String())
		}
		exec(mutation.undo, mediaID)
	}
}
