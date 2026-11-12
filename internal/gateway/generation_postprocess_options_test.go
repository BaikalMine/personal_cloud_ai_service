package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/loratraining"
)

func TestPostprocessOptionsRateDoesNotBlockQuote(t *testing.T) {
	a := &App{}
	now := time.Now()
	if !a.allowPostprocessOptions(now) || !a.allowNewPostprocessQuote(now) || a.allowPostprocessOptions(now) || a.allowNewPostprocessQuote(now) {
		t.Fatal("options and quote rates must be independent")
	}
	if !a.allowPostprocessOptions(now.Add(3*time.Second)) || !a.allowNewPostprocessQuote(now.Add(3*time.Second)) {
		t.Fatal("rate did not expire")
	}
}

func TestPostprocessDialogAssetsArePublished(t *testing.T) {
	for _, path := range []string{"static/generation-postprocess.js", "static/generation-postprocess-state.js", "static/generation-postprocess.css"} {
		found := false
		for _, asset := range frontendAssetPaths {
			if asset == path {
				found = true
			}
		}
		if !found {
			t.Fatal("postprocess asset missing from version hash", path)
		}
		assets := staticJavaScriptAssets
		if strings.HasSuffix(path, ".css") {
			assets = staticCSSAssets
		}
		if assets["/"+path] != path {
			t.Fatal("postprocess asset has no production route", path)
		}
	}
}

func TestPostprocessAvailableSizeChoices(t *testing.T) {
	for _, source := range []postprocessSourceFacts{postprocessTestSource(), postprocessVideoSource()} {
		choices := postprocessUpscaleChoices(source, "gateway/gateway-test", postprocessTestCatalog())
		if len(choices) == 0 || len(choices) > 20 {
			t.Fatal("unbounded or absent choices", choices)
		}
		for _, choice := range choices {
			if _, err := postprocessOperationGraph(source, choice.Options, "gateway/gateway-test", postprocessTestCatalog()); err != nil {
				t.Fatal("offered invalid size", choice, err)
			}
		}
		catalog := postprocessTestCatalog()
		delete(catalog.Nodes, "RTXVideoSuperResolution")
		if choices := postprocessUpscaleChoices(source, "gateway/gateway-test", catalog); len(choices) != 0 {
			t.Fatal("missing node offered")
		}
	}
	source := postprocessVideoSource()
	source.Height++
	upscale := postprocessUpscaleChoices(source, "gateway/gateway-test", postprocessTestCatalog())
	choice, _, ok := postprocessDependentChoice(source, postprocessOptions{Width: source.Width, Height: source.Height, Interpolate: 2, RIFECheckpoint: "rife49.pth"}, upscale, "gateway/gateway-test", postprocessTestCatalog())
	if !ok || !choice.RequiresUpscale || choice.Options.Height%2 != 0 {
		t.Fatal("odd output not tied to resizing", choice, ok)
	}
	if _, _, ok := postprocessDependentChoice(source, postprocessOptions{Width: source.Width, Height: source.Height, Interpolate: 2, RIFECheckpoint: "rife49.pth"}, nil, "gateway/gateway-test", postprocessTestCatalog()); ok {
		t.Fatal("odd H264 output offered")
	}
}

func TestPostprocessOptionsHTTPIntegration(t *testing.T) {
	p := newPostprocessConfirmationFixture(t)
	source := p.quote.Sources[0].Identity
	values := url.Values{"csrf": {p.values().Get("csrf")}, "source_job_id": {source.JobPublicID}, "source_media_id": {strconv.FormatInt(source.MediaID, 10)}}
	result := postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/options", values), 200)
	if result["source"].(map[string]any)["media_id"] != values.Get("source_media_id") || len(result["upscale"].([]any)) == 0 || len(result["color"].([]any)) != 2 || len(result["interpolation"].([]any)) != 0 {
		t.Fatal(result)
	}
	postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/options", values), 429)
	r, err := p.f.app.store.GenerationPostprocessByRequest(context.Background(), p.f.user.ID, p.receipt.RequestID)
	if err != nil || r.JobID != nil || r.State != "quoted" {
		t.Fatal("options accepted a quote", r, err)
	}
	var count int
	if err := p.f.db.QueryRow(`SELECT count(*) FROM generation_postprocess_receipts`).Scan(&count); err != nil || count != 1 {
		t.Fatal("options persisted extra quotes", count, err)
	}
}

func TestPostprocessOptionsFreshSourceIntegration(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(strconv.FormatBool(late), func(t *testing.T) {
			p := newPostprocessConfirmationFixture(t)
			hide := func() {
				if _, err := p.f.db.Exec(`UPDATE content_media SET profile_hidden_at=now()`); err != nil {
					t.Error(err)
				}
			}
			if late {
				p.hook.Store(hide)
			} else {
				hide()
			}
			s := p.quote.Sources[0].Identity
			v := url.Values{"csrf": {p.values().Get("csrf")}, "source_job_id": {s.JobPublicID}, "source_media_id": {strconv.FormatInt(s.MediaID, 10)}}
			postprocessHTTPResult(t, p.call("POST", "/generate/postprocess/options", v), 404)
		})
	}
}

const postprocessVideoOptionsObjectInfoFixture = `{
 "VHS_LoadVideo":{"input":{"required":{"video":[["video.mp4"]],"force_rate":["FLOAT"],"custom_width":["INT"],"custom_height":["INT"],"frame_load_cap":["INT"],"skip_first_frames":["INT"],"select_every_nth":["INT"],"format":[["None"]]}},"output":["IMAGE","INT","AUDIO","VHS_VIDEOINFO"]},
 "VHS_VideoCombine":{"input":{"required":{"images":["IMAGE"],"frame_rate":["FLOAT"],"loop_count":["INT"],"filename_prefix":["STRING"],"format":[["video/h264-mp4"],{"formats":{"video/h264-mp4":[["pix_fmt",["yuv420p"]],["crf","INT",{"min":0,"max":100}],["save_metadata","BOOLEAN"],["trim_to_audio","BOOLEAN"]]}}],"pingpong":["BOOLEAN"],"save_output":["BOOLEAN"],"audio":["AUDIO"]}},"output":["VHS_FILENAMES"]},
 "RIFE VFI":{"input":{"required":{"ckpt_name":[["rife47.pth","rife49.pth","rife417.pth","rife426.pth"]],"frames":["IMAGE"],"clear_cache_after_n_frames":["INT"],"multiplier":["INT"],"fast_mode":["BOOLEAN"],"ensemble":["BOOLEAN"],"scale_factor":[[1.0]],"dtype":[["float32"]],"torch_compile":["BOOLEAN"],"batch_size":["INT"]}},"output":["IMAGE"]},
 "ImageFromBatch":{"input":{"required":{"image":["IMAGE"],"batch_index":["INT"],"length":["INT"]}},"output":["IMAGE"]},
 "RepeatImageBatch":{"input":{"required":{"image":["IMAGE"],"amount":["INT"]}},"output":["IMAGE"]},
 "ImageBatch":{"input":{"required":{"image1":["IMAGE"],"image2":["IMAGE"]}},"output":["IMAGE"]}
}`

func TestPostprocessOptionsVerifiedInterpolationIntegration(t *testing.T) {
	p := newPostprocessConfirmationFixture(t)
	path := filepath.Join(t.TempDir(), "source.mp4")
	if output, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=128x80:r=10:d=1", "-an", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", path).CombinedOutput(); err != nil {
		t.Fatalf("video fixture: %v %s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := p.f.app.contentCipher.EncryptBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	s := p.quote.Sources[0].Identity
	if _, err := p.f.db.Exec(`UPDATE content_media SET media_type='video',mime_type='video/mp4',original_name='source.mp4',payload_cipher=$2,size_bytes=$3,content_hash=$4 WHERE id=$1`, s.MediaID, cipher, len(data), generationEvidenceDigest(data)); err != nil {
		t.Fatal(err)
	}
	var nodes, extra map[string]json.RawMessage
	if err := json.Unmarshal([]byte(postprocessImageObjectInfoFixture), &nodes); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(postprocessVideoOptionsObjectInfoFixture), &extra); err != nil {
		t.Fatal(err)
	}
	for key, value := range extra {
		nodes[key] = value
	}
	encoded, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	p.catalog.Store(string(encoded))
	ctx := context.Background()
	view, err := p.f.app.postprocessOptionsForSource(ctx, p.f.user.ID, s.JobPublicID, s.MediaID)
	if err != nil || len(view.Interpolation) != 0 || len(view.Upscale) == 0 || len(view.Color) == 0 || view.Source.Measurement.Frames != 10 {
		t.Fatal("unconfigured host", view, err)
	}
	var mode atomic.Value
	mode.Store("verified")
	var inspections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/generation/components/inspect" {
			t.Error("unexpected host operation", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		inspections.Add(1)
		var request componentinspection.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.References) != 1 {
			t.Error("invalid inspection request", request, err)
			w.WriteHeader(400)
			return
		}
		now := time.Now().UTC()
		observation := componentinspection.Observation{Reference: request.References[0], State: "verified", Bytes: 123456, SHA256: strings.Repeat("b", 64), CheckedAt: now, HashedAt: now}
		response := componentinspection.Response{Version: 1, RequestID: request.RequestID, RequestSHA256: request.Digest(), Source: componentinspection.Source, State: "complete"}
		switch mode.Load().(string) {
		case "missing":
			observation.State, observation.Bytes, observation.SHA256, observation.HashedAt = "missing", 0, "", time.Time{}
		case "stale":
			observation.CheckedAt, observation.HashedAt = now.Add(-time.Hour), now.Add(-time.Hour)
		case "mismatch":
			response.RequestID += "x"
		}
		response.Items = []componentinspection.Observation{observation}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	p.f.app.loraTraining = loratraining.NewClient(endpoint, strings.Repeat("t", 32))
	for _, state := range []string{"verified", "missing", "stale", "mismatch"} {
		mode.Store(state)
		view, err := p.f.app.postprocessOptionsForSource(ctx, p.f.user.ID, s.JobPublicID, s.MediaID)
		want := 0
		if state == "verified" {
			want = 12
		}
		if err != nil || len(view.Interpolation) != want {
			t.Fatal(state, view, err)
		}
	}
	if inspections.Load() != 16 {
		t.Fatal("each installed checkpoint must be inspected", inspections.Load())
	}
}
