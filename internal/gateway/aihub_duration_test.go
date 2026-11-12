package gateway

import (
	"context"
	"math"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAIHubReferenceDurationRealVideo(t *testing.T) {
	filename := os.Getenv("AIHUB_DURATION_TEST_VIDEO")
	if filename == "" {
		t.Skip("optional real-video fixture")
	}
	seconds, err := probeAIHubReferenceDuration(context.Background(), filename, "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(seconds-7.608667) > 0.001 {
		t.Fatalf("unexpected source duration %v", seconds)
	}
	frames, err := aiHubReferenceFrames(seconds)
	if err != nil || frames != 183 {
		t.Fatalf("frames=%d err=%v", frames, err)
	}
}

func TestAIHubReferenceFrames(t *testing.T) {
	for _, tc := range []struct {
		seconds float64
		frames  int
	}{
		{7.608667, 183}, {11.666667, 280}, {15, 360}, {1, 24}, {5.0 / 24, 5},
		{20, 480}, {30, 720}, {0, 0}, {-1, 0}, {30.001, 0}, {math.NaN(), 0}, {math.Inf(1), 0}, {0.1, 0},
	} {
		frames, err := aiHubReferenceFrames(tc.seconds)
		if frames != tc.frames || (err != nil) != (tc.frames == 0) {
			t.Fatalf("%v: frames=%d err=%v", tc.seconds, frames, err)
		}
	}
}

func TestAIHubReferenceDurationGraph(t *testing.T) {
	info := aiHubTestSchema(t)
	schema := buildComfySchemaCatalog(info, time.Time{}, "aihub")
	for name, node := range schema.Nodes {
		for key, field := range node.Required {
			switch key {
			case "image", "video", "audio", "unet_name", "clip_name", "vae_name":
				field.Choices = nil
				node.Required[key] = field
			}
		}
		schema.Nodes[name] = node
	}
	for _, source := range []string{"native", "original", "ltx"} {
		for _, target := range []int{5, 183, 280, 360, 480, 720} {
			in := aiHubTestInput(t)
			in.AIHubMatchDuration, in.AIHubOutputFrames = true, target
			in.VideoDurationSeconds, in.VideoReferenceStart = 60, 7
			in.AIHubAudioSource, in.AIHubAudioPrompt = source, "Soft music."
			in.VideoRTXEnabled, in.AIHubVoiceConversion, in.InputAudio = true, true, "voice.wav"
			if err := normalizeAIHub(&in); err != nil {
				t.Fatal(err)
			}
			if in.VideoReferenceStart != 0 {
				t.Fatal("matching length must include the whole source")
			}
			if err := resolveAIHubModels(&in, info); err != nil {
				t.Fatal(err)
			}
			graph, err := buildAIHubPrompt(in)
			if err != nil {
				t.Fatal(err)
			}
			if issues := validateComfyPrompt(schema, graph); len(issues) != 0 {
				t.Fatalf("schema: %+v", issues)
			}
			node := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
			frames := node("7")["length"].(int)
			if frames < target || (frames-5)%17 != 0 {
				t.Fatal("unaligned H3 frame count", frames)
			}
			if node("20")["frame_load_cap"] != target || node("20")["skip_first_frames"] != 0 {
				t.Fatal("wrong reference fragment")
			}
			if node("17")["length"] != target || !reflect.DeepEqual(node("70")["images"], []any{"17", 0}) {
				t.Fatal("must trim before RTX")
			}
			if node("18")["duration"] != float64(target)/24 || !reflect.DeepEqual(node("18")["audio"], []any{"81", 0}) {
				t.Fatal("must trim after voice conversion")
			}
			if !reflect.DeepEqual(node("90")["audio"], []any{"18", 0}) {
				t.Fatal("output bypasses trimmed audio")
			}
		}
	}
}

func TestAIHubDurationRequiresMeasuredOwnedVideo(t *testing.T) {
	in := aiHubTestInput(t)
	in.AIHubMatchDuration = true
	if _, err := buildAIHubPrompt(in); err == nil {
		t.Fatal("unmeasured duration accepted")
	}
	for _, mode := range []string{"photo", "carousel"} {
		in.AIHubMode = mode
		if normalizeAIHub(&in) == nil {
			t.Fatal("invalid mode accepted", mode)
		}
	}
	form := url.Values{"generation_workflow": {aiHubWorkflowID}, "aihub_match_duration": {"true"}, "AIHubOutputFrames": {"360"}, "aihub_output_frames": {"360"}}
	r := httptest.NewRequest("POST", "/generate", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	parsed, err := parseGenerationForm(r)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.AIHubMatchDuration || parsed.AIHubOutputFrames != 0 {
		t.Fatal("client controls server-only frame count")
	}
	in.AIHubMode, in.InputVideo = "video", "foreign/file.mp4"
	if err := (&App{}).resolveAIHubDuration(context.Background(), 1, &in); err == nil {
		t.Fatal("foreign source accepted before fetch")
	}
}
