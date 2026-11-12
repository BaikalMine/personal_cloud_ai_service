package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"runtime"
	"strings"
	"testing"

	"ai-access-gateway/internal/mediaprobe"
)

func TestPostprocessSourceMaterializationAndAccess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bounded native decoder requires Linux")
	}
	f := newDispatchFixture(t)
	ctx := context.Background()
	job := f.enqueue(t)
	f.app.cfg.MediaSpoolDir = t.TempDir()
	execute := func(query string, args ...any) {
		t.Helper()
		if _, err := f.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	execute(`UPDATE users SET role='user',can_generate_text_to_image=true,can_generate_video=false WHERE id=$1`, f.user.ID)
	img := image.NewRGBA(image.Rect(0, 0, 128, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 128; x++ {
			img.Set(x, y, color.RGBA{R: byte(x), G: byte(y), B: 50, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	cipher, err := f.app.contentCipher.EncryptBytes(data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var eventID, mediaID int64
	if err := f.db.QueryRow(`INSERT INTO content_events(user_id,generation_job_id,service,kind,prompt_cipher,response_cipher,metadata_cipher) VALUES($1,$2,'comfyui','comfyui_prompt','x','x','x') RETURNING id`, f.user.ID, job.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	hash := generationEvidenceDigest(data.Bytes())
	if err := f.db.QueryRow(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,payload_cipher,size_bytes,content_hash) VALUES($1,'image','image/png','source.png',$2,$3,$4) RETURNING id`, eventID, cipher, data.Len(), hash).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	prepare := func() (*preparedPostprocessSource, error) {
		return f.app.preparePostprocessSource(ctx, f.user.ID, job.PublicID, mediaID)
	}
	assertClean := func() {
		t.Helper()
		files, err := os.ReadDir(f.app.cfg.MediaSpoolDir)
		if err != nil || len(files) != 0 {
			t.Fatal("plaintext spool leaked", files, err)
		}
		if len(f.app.postprocessDecodeSlots) != 0 {
			t.Fatal("decode slot leaked")
		}
	}
	if source, err := prepare(); !errors.Is(err, sql.ErrNoRows) {
		source.Close()
		t.Fatalf("unfinished source accepted: %v", err)
	}
	execute(`UPDATE generation_jobs SET state='completed',finished_at=now() WHERE id=$1`, job.ID)
	source, err := prepare()
	if err != nil {
		t.Fatal(err)
	}
	if source.Source.MediaID != mediaID || source.ParentJob.ID != job.ID || source.UserID != f.user.ID || source.Measurement.SHA256 != hash || !source.Facts.DecodeVerified || source.Facts.Frames != 1 || source.Facts.Width != 128 || !source.Facts.Opaque {
		source.Close()
		t.Fatalf("wrong decoded identity: %+v", source)
	}
	plain, err := os.ReadFile(source.Media.path)
	if err != nil || !bytes.Equal(plain, data.Bytes()) {
		source.Close()
		t.Fatal("different materialized bytes", err)
	}
	if _, err := planPostprocess(source.Facts, postprocessOptions{Width: 256, Height: 160, Interpolate: 1, RTXQuality: "HIGH"}); err != nil {
		source.Close()
		t.Fatal("decoded facts cannot enter planner", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal("cleanup not idempotent", err)
	}
	assertClean()
	execute(`UPDATE content_media SET content_hash='' WHERE id=$1`, mediaID)
	legacy, err := prepare()
	if err != nil {
		t.Fatal("legacy source cannot establish byte identity", err)
	}
	if legacy.Measurement.SHA256 != hash || legacy.Source.ContentHash != "" {
		legacy.Close()
		t.Fatal("legacy source hash was fabricated")
	}
	legacy.Close()
	execute(`UPDATE content_media SET content_hash=$2 WHERE id=$1`, mediaID, hash)
	assertClean()
	f.app.postprocessDecodeSlots <- struct{}{}
	if source, err := prepare(); !errors.Is(err, errPostprocessSourceBusy) {
		source.Close()
		t.Fatal("decoder admission not bounded", err)
	}
	<-f.app.postprocessDecodeSlots
	assertClean()
	for _, tc := range []struct {
		deny, restore string
		id            int64
	}{
		{`UPDATE users SET disabled=true WHERE id=$1`, `UPDATE users SET disabled=false WHERE id=$1`, f.user.ID},
		{`UPDATE users SET can_use_quick_generation=false WHERE id=$1`, `UPDATE users SET can_use_quick_generation=true WHERE id=$1`, f.user.ID},
		{`UPDATE users SET can_generate_text_to_image=false WHERE id=$1`, `UPDATE users SET can_generate_text_to_image=true WHERE id=$1`, f.user.ID},
		{`UPDATE users SET account_expires_at=now()-interval '1 minute' WHERE id=$1`, `UPDATE users SET account_expires_at=NULL WHERE id=$1`, f.user.ID},
		{`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, `UPDATE content_media SET profile_hidden_at=NULL WHERE id=$1`, mediaID},
		{`UPDATE content_media SET expires_at=now()-interval '1 minute' WHERE id=$1`, `UPDATE content_media SET expires_at=now()+interval '1 day' WHERE id=$1`, mediaID},
		{`UPDATE content_events SET expires_at=now()-interval '1 minute' WHERE id=$1`, `UPDATE content_events SET expires_at=now()+interval '1 day' WHERE id=$1`, eventID},
	} {
		execute(tc.deny, tc.id)
		if source, err := prepare(); !errors.Is(err, sql.ErrNoRows) {
			source.Close()
			t.Fatal("access survived revocation", tc.deny, err)
		}
		execute(tc.restore, tc.id)
		assertClean()
	}
	for _, request := range []struct {
		user  int64
		job   string
		media int64
	}{{f.user.ID + 1, job.PublicID, mediaID}, {f.user.ID, "missing-source-0001", mediaID}, {f.user.ID, job.PublicID, mediaID + 1}, {f.user.ID, job.PublicID, 0}} {
		if source, err := f.app.preparePostprocessSource(ctx, request.user, request.job, request.media); !errors.Is(err, sql.ErrNoRows) {
			source.Close()
			t.Fatal("foreign or wrong source accepted", request, err)
		}
	}
	execute(`UPDATE content_media SET media_type='video',mime_type='video/mp4' WHERE id=$1`, mediaID)
	if source, err := prepare(); !errors.Is(err, sql.ErrNoRows) {
		source.Close()
		t.Fatal("video accepted under image-only rights", err)
	}
	execute(`UPDATE content_media SET media_type='image',mime_type='image/png' WHERE id=$1`, mediaID)
	assertClean()
	execute(`UPDATE content_media SET content_hash=$2 WHERE id=$1`, mediaID, strings.Repeat("b", 64))
	if source, err := prepare(); !errors.Is(err, errPostprocessSourceChanged) {
		source.Close()
		t.Fatal("wrong hash accepted", err)
	}
	assertClean()
	execute(`UPDATE content_media SET content_hash=$2,payload_cipher='broken' WHERE id=$1`, mediaID, hash)
	if source, err := prepare(); err == nil {
		source.Close()
		t.Fatal("corrupt ciphertext accepted")
	}
	assertClean()
	execute(`UPDATE content_media SET payload_cipher=$2 WHERE id=$1`, mediaID, cipher)
	for _, action := range []struct {
		query, restore string
		id             int64
		want           error
	}{
		{`UPDATE users SET disabled=true WHERE id=$1`, `UPDATE users SET disabled=false WHERE id=$1`, f.user.ID, sql.ErrNoRows},
		{`UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, `UPDATE content_media SET profile_hidden_at=NULL WHERE id=$1`, mediaID, sql.ErrNoRows},
		{`UPDATE content_media SET content_hash=repeat('c',64) WHERE id=$1`, `UPDATE content_media SET content_hash=$2 WHERE id=$1`, mediaID, errPostprocessSourceChanged},
	} {
		decoder := func(ctx context.Context, path, mime string) (mediaprobe.DecodedMeasurement, error) {
			result, err := mediaprobe.Decode(ctx, path, mime)
			if err != nil {
				return result, err
			}
			execute(action.query, action.id)
			return result, nil
		}
		if source, err := f.app.preparePostprocessSourceWithDecoder(ctx, f.user.ID, job.PublicID, mediaID, decoder); !errors.Is(err, action.want) {
			source.Close()
			t.Fatal("post-decode change accepted", action.query, err)
		}
		if strings.Contains(action.restore, "$2") {
			execute(action.restore, action.id, hash)
		} else {
			execute(action.restore, action.id)
		}
		assertClean()
	}
	metadataOnly := func(ctx context.Context, path, mime string) (mediaprobe.DecodedMeasurement, error) {
		measurement, err := mediaprobe.Probe(ctx, path, mime)
		return mediaprobe.DecodedMeasurement{Measurement: measurement}, err
	}
	if source, err := f.app.preparePostprocessSourceWithDecoder(ctx, f.user.ID, job.PublicID, mediaID, metadataOnly); !errors.Is(err, errPostprocessSourceChanged) {
		source.Close()
		t.Fatal("stream metadata treated as full decode", err)
	}
	assertClean()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if source, err := f.app.preparePostprocessSource(cancelled, f.user.ID, job.PublicID, mediaID); !errors.Is(err, context.Canceled) {
		source.Close()
		t.Fatal(err)
	}
	assertClean()
	// A newer output in the same source job must not replace the exact selection.
	execute(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,payload_cipher,size_bytes,content_hash) VALUES($1,'image','image/png','newer.png',$2,$3,$4)`, eventID, cipher, data.Len(), hash)
	execute(`DELETE FROM content_media WHERE id=$1`, mediaID)
	if source, err := prepare(); !errors.Is(err, sql.ErrNoRows) {
		source.Close()
		t.Fatal("deleted source survived", err)
	}
	assertClean()
	if f.posts.Load() != 0 {
		t.Fatal("source check submitted GPU work")
	}
	var jobs int
	if err := f.db.QueryRow(`SELECT count(*) FROM generation_jobs WHERE user_id=$1`, f.user.ID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("source check created work", jobs, err)
	}
}
