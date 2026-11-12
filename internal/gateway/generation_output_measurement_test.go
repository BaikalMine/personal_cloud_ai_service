package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mediaprobe"
)

func TestGenerationOutputMeasurementHTTPGuards(t *testing.T) {
	a := &App{}
	for _, tc := range []struct {
		method, query string
		user          *User
		want          int
	}{
		{"POST", "", nil, 405}, {"HEAD", "", nil, 405}, {"GET", "", nil, 401},
		{"GET", "job=valid-source-0001&media=01", &User{ID: 1}, 400},
		{"GET", "job=valid-source-0001&media=1&media=2", &User{ID: 1}, 400},
		{"GET", "job=valid-source-0001&media=1&path=/etc/passwd", &User{ID: 1}, 400},
		{"GET", "job=%zz&media=1", &User{ID: 1}, 400},
		{"GET", "job=valid-source-0001&media=-1", &User{ID: 1}, 400},
	} {
		r := httptest.NewRequest(tc.method, "/generate/jobs/output-measurement?"+tc.query, nil)
		if tc.user != nil {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, tc.user))
		}
		w := httptest.NewRecorder()
		a.handleGenerationOutputMeasurement(w, r)
		if w.Code != tc.want || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}

func TestGenerationOutputMeasurementIntegration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native probe uses Linux")
	}
	f := newDispatchFixture(t)
	ctx := context.Background()
	job := f.enqueue(t)
	f.app.cfg.MediaSpoolDir = t.TempDir()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 137, 89))); err != nil {
		t.Fatal(err)
	}
	cipher, err := f.app.contentCipher.EncryptBytes(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	execute := func(q string, args ...any) {
		t.Helper()
		if _, err := f.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	execute(`UPDATE users SET role='user',can_generate_text_to_image=true WHERE id=$1`, f.user.ID)
	var eventID, mediaID int64
	if err := f.db.QueryRow(`INSERT INTO content_events(user_id,generation_job_id,service,kind,prompt_cipher,response_cipher,metadata_cipher) VALUES($1,$2,'comfyui','comfyui_prompt','x','x','x') RETURNING id`, f.user.ID, job.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,payload_cipher,size_bytes,content_hash) VALUES($1,'image','image/png','source.png',$2,$3,$4) RETURNING id`, eventID, cipher, b.Len(), generationEvidenceDigest(b.Bytes())).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	request := func(user User, jobID string, id int64) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", fmt.Sprintf("/generate/jobs/output-measurement?job=%s&media=%d", jobID, id), nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &user))
		w := httptest.NewRecorder()
		f.app.handleGenerationOutputMeasurement(w, r)
		return w
	}
	views, more, err := f.app.generationOutputViews(ctx, f.user.ID, job.PublicID)
	if err != nil || more || len(views) != 1 || views[0]["measurement_state"] != "not_recorded" {
		t.Fatal(views, err)
	}
	f.app.mediaProbeOnce.Do(func() { f.app.mediaProbeSlots = make(chan struct{}, 2) })
	f.app.mediaProbeSlots <- struct{}{}
	f.app.mediaProbeSlots <- struct{}{}
	if w := request(f.user, job.PublicID, mediaID); w.Code != 429 || w.Header().Get("Retry-After") != "5" {
		t.Fatal("unbounded native work", w.Code, w.Body.String())
	}
	<-f.app.mediaProbeSlots
	<-f.app.mediaProbeSlots
	w := request(f.user, job.PublicID, mediaID)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Measurement mediaprobe.Measurement `json:"measurement"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Measurement.Width != 137 || response.Measurement.Height != 89 || response.Measurement.SHA256 != generationEvidenceDigest(b.Bytes()) || response.Measurement.AverageFPS != "" {
		t.Fatal(response)
	}
	views, _, err = f.app.generationOutputViews(ctx, f.user.ID, job.PublicID)
	if err != nil || views[0]["measurement_state"] != "measured" {
		t.Fatal(views, err)
	}
	var saved []byte
	if err := f.db.QueryRow(`SELECT payload_cipher FROM content_media_measurements WHERE media_id=$1`, mediaID).Scan(&saved); err != nil || bytes.Contains(saved, []byte(`"width"`)) {
		t.Fatal("measurement is not encrypted", err)
	}
	// Cached reads do not need a spool and retain the original measurement time.
	f.app.cfg.MediaSpoolDir = "/nonexistent/media-probe-cache-test"
	w = request(f.user, job.PublicID, mediaID)
	if w.Code != 200 || !strings.Contains(w.Body.String(), response.Measurement.MeasuredAt.Format(time.RFC3339Nano)) {
		t.Fatal("cache re-probed", w.Body.String())
	}
	other := f.user
	other.ID++
	foreign, missing := request(other, job.PublicID, mediaID), request(other, "missing-source-0001", mediaID)
	if foreign.Code != 404 || foreign.Body.String() != missing.Body.String() {
		t.Fatal("owner leak", foreign.Body.String())
	}
	if w := request(f.user, "another-source-0001", mediaID); w.Code != 404 {
		t.Fatal("wrong job accepted", w.Body.String())
	}
	for _, tc := range []struct{ deny, restore string }{
		{`UPDATE users SET disabled=true WHERE id=$1`, `UPDATE users SET disabled=false WHERE id=$1`},
		{`UPDATE users SET can_use_quick_generation=false WHERE id=$1`, `UPDATE users SET can_use_quick_generation=true WHERE id=$1`},
		{`UPDATE users SET can_generate_text_to_image=false WHERE id=$1`, `UPDATE users SET can_generate_text_to_image=true WHERE id=$1`},
		{`UPDATE users SET account_expires_at=now()-interval '1 minute' WHERE id=$1`, `UPDATE users SET account_expires_at=NULL WHERE id=$1`},
	} {
		execute(tc.deny, f.user.ID)
		if w := request(f.user, job.PublicID, mediaID); w.Code != 404 {
			t.Fatal("cached access survived revoke", tc.deny, w.Body.String())
		}
		execute(tc.restore, f.user.ID)
	}
	for _, tc := range []struct {
		deny, restore string
		id            int64
	}{
		{`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, `UPDATE content_media SET profile_hidden_at=NULL WHERE id=$1`, mediaID},
		{`UPDATE content_media SET expires_at=now()-interval '1 minute' WHERE id=$1`, `UPDATE content_media SET expires_at=now()+interval '1 day' WHERE id=$1`, mediaID},
		{`UPDATE content_events SET expires_at=now()-interval '1 minute' WHERE id=$1`, `UPDATE content_events SET expires_at=now()+interval '1 day' WHERE id=$1`, eventID},
	} {
		execute(tc.deny, tc.id)
		if w := request(f.user, job.PublicID, mediaID); w.Code != 404 {
			t.Fatal("cached access survived deletion/expiry", tc.deny, w.Body.String())
		}
		err := f.app.store.SaveMediaMeasurement(ctx, f.user.ID, job.PublicID, domain.MediaMeasurementRecord{MediaID: mediaID, SHA256: response.Measurement.SHA256, SizeBytes: int64(b.Len()), PayloadCipher: saved})
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("saved inaccessible measurement", err)
		}
		execute(tc.restore, tc.id)
	}
	execute(`UPDATE content_media_measurements SET payload_cipher='broken' WHERE media_id=$1`, mediaID)
	if w := request(f.user, job.PublicID, mediaID); w.Code != 503 {
		t.Fatal("corrupt measurement accepted", w.Body.String())
	}
	views, _, err = f.app.generationOutputViews(ctx, f.user.ID, job.PublicID)
	if err != nil || views[0]["measurement_state"] != "unavailable" {
		t.Fatal("corrupt auxiliary data hid the passport", views, err)
	}
	execute(`UPDATE content_media_measurements SET payload_cipher=$2 WHERE media_id=$1`, mediaID, saved)
	f.app.cfg.MediaSpoolDir = t.TempDir()
	execute(`UPDATE content_media SET content_hash=repeat('b',64) WHERE id=$1`, mediaID)
	if w := request(f.user, job.PublicID, mediaID); w.Code != 422 {
		t.Fatal("hash mismatch accepted", w.Code, w.Body.String())
	}
	execute(`UPDATE content_media SET content_hash=$2 WHERE id=$1`, mediaID, response.Measurement.SHA256)
	files, err := os.ReadDir(f.app.cfg.MediaSpoolDir)
	if err != nil || len(files) != 0 {
		t.Fatal("plaintext spool leaked", files, err)
	}
	if len(f.app.mediaProbeSlots) != 0 {
		t.Fatal("native slot leaked")
	}
	// Encrypted cache data cannot be transplanted to a different media identity.
	sources, err := f.app.store.GenerationOutputSources(ctx, f.user.ID, job.PublicID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	source := sources[0]
	source.MediaID++
	if _, err := f.app.decodeMediaMeasurement(source); err == nil {
		t.Fatal("AAD did not bind media ID")
	}
	execute(`DELETE FROM content_media WHERE id=$1`, mediaID)
	if w := request(f.user, job.PublicID, mediaID); w.Code != 404 {
		t.Fatal("deleted source survived", w.Body.String())
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM content_media_measurements WHERE media_id=$1`, mediaID).Scan(&count); err != nil || count != 0 {
		t.Fatal("measurement not cascaded", count, err)
	}
	if f.posts.Load() != 0 {
		t.Fatal("measurement submitted GPU work")
	}
}
