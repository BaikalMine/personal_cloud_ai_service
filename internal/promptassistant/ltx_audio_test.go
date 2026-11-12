package promptassistant

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestLTXAudioResponseContract(t *testing.T) {
	video := VideoContext{AIHubMode: "photo", AudioSource: "ltx"}
	contract := structuredResponseInstruction(ModeTextToVideo, ProfileMiniMaxH3REF2VA, nil, video)
	for _, required := range []string{"audio_prompt", "verbatim", "non-speech", "4000"} {
		if !strings.Contains(contract, required) {
			t.Fatalf("missing %s", required)
		}
	}
	for _, other := range []VideoContext{{}, {AIHubMode: "video", AudioSource: "original"}, {AIHubMode: "carousel", AudioSource: "ltx"}} {
		if strings.Contains(structuredResponseInstruction(ModeTextToVideo, ProfileMiniMaxH3REF2VA, nil, other), "audio_prompt") {
			t.Fatal("audio contract leaked to another mode")
		}
	}
}

func TestLTXAudioPreparedResponse(t *testing.T) {
	for _, tc := range []struct {
		name, audio    string
		ltx, wantError bool
	}{
		{"soundtrack", "A woman says: \"Hello.\" Soft room ambience.", true, false},
		{"missing soundtrack", "", true, true},
		{"oversized soundtrack", strings.Repeat("a", 4001), true, true},
		{"non LTX ignores soundtrack", "Unexpected soundtrack", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, _ := json.Marshal(map[string]any{"prompt": "A woman waves.", "audio_prompt": tc.audio, "references": []any{}})
			body, _ := json.Marshal(map[string]any{"done": true, "message": map[string]string{"content": string(content)}})
			prepared := PreparedEnhancement{Mode: ModeTextToVideo, LTXAudio: tc.ltx}
			stored, _ := json.Marshal(prepared)
			var restored PreparedEnhancement
			if err := json.Unmarshal(stored, &restored); err != nil {
				t.Fatal(err)
			}
			result, err := ParseEnhancementResponse(restored, 200, body)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			if tc.ltx && tc.audio == "" && !errors.Is(err, ErrMissingAudioPrompt) {
				t.Fatalf("missing typed soundtrack error: %v", err)
			}
			if result.Execution != ExecutionCompleted {
				t.Fatal("completion signal lost")
			}
			if err == nil && tc.ltx && result.AudioPrompt != tc.audio {
				t.Fatal("soundtrack lost")
			}
			if !tc.ltx && result.AudioPrompt != "" {
				t.Fatal("unexpected soundtrack")
			}
		})
	}
}

func TestLTXRequestWithoutImagesUsesVideoModelAndSchema(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:11434")
	client := NewClient(base, "small-text").WithVisionModel("video-model", 0, "0")
	video := VideoContext{Mode: VideoModeREF2VA, AIHubMode: "photo", AudioSource: "ltx", DurationSeconds: 5}
	prepared, err := client.PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "A vase in daylight", nil, video, false)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Model  string
		Format struct {
			Required   []string
			Properties map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(prepared.Body, &request); err != nil {
		t.Fatal(err)
	}
	if request.Model != "video-model" || prepared.Model != "video-model" {
		t.Fatal("video without images routed to small text model")
	}
	for _, key := range []string{"prompt", "audio_prompt", "references"} {
		found := false
		for _, required := range request.Format.Required {
			found = found || required == key
		}
		if !found || len(request.Format.Properties[key]) == 0 {
			t.Fatalf("schema does not require %s", key)
		}
	}
	selected := client.ForModels("approved-large-text", "", 0)
	prepared, err = selected.PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "A vase in daylight", nil, video, false)
	if err != nil || prepared.Model != "approved-large-text" {
		t.Fatalf("explicit approved model selection lost: %s %v", prepared.Model, err)
	}
}
