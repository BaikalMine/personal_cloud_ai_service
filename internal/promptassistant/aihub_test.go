package promptassistant

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestAIHubAllPicturesStoryboardAndVoice(t *testing.T) {
	refs := make([]ImageReference, 9)
	for i := range refs {
		refs[i] = ImageReference{Number: i + 1, Role: ImageReferenceIdentity, MIMEType: "image/png", Image: []byte{1, 2, 3}}
	}
	video := VideoContext{Mode: VideoModeREF2VA, AIHubMode: "video", DurationSeconds: 5, ImageCount: 9, VideoReference: true, VideoStoryboard: []byte{4, 5, 6}, VideoSampleDuration: 5, AudioSource: "ltx", AudioPrompt: "Welcome back.", VoiceReference: true}
	base, _ := url.Parse("http://localhost:11434")
	prepared, err := NewClient(base, "text").WithVisionModel("vision", 0, "0").PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "Preserve the face", refs, video, false)
	if err != nil {
		t.Fatal(err)
	}
	var body chatRequest
	if err := json.Unmarshal(prepared.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "vision" || len(body.Messages[1].Images) != 10 {
		t.Fatal("not all visual inputs were sent")
	}
	for i := 1; i <= 9; i++ {
		if !strings.Contains(body.Messages[0].Content, fmt.Sprintf("- <Picture %d>", i)) {
			t.Fatalf("missing role %d", i)
		}
	}
	for _, text := range []string{"BASE_PROMPT", "Never combine all pictures", "LTX 2.5", "Seed-VC"} {
		if !strings.Contains(body.Messages[0].Content, text) {
			t.Fatal("missing workflow rule:", text)
		}
	}
	if !strings.Contains(body.Messages[1].Content, "Welcome back.") || strings.Contains(body.Messages[0].Content, "Welcome back.") {
		t.Fatal("user sound specification must stay in the user message")
	}
	if !strings.Contains(body.Messages[1].Content, "NOT Picture 10") {
		t.Fatal("storyboard confused with picture")
	}
	if len(prepared.References) != 11 || !prepared.References[9].Inspected || prepared.References[10].Identifier != "Voice 1" || prepared.References[10].Inspected {
		t.Fatal("incorrect media coverage", prepared.References)
	}
	var summaries []map[string]string
	for _, ref := range prepared.References {
		summaries = append(summaries, map[string]string{"id": ref.Identifier, "summary": "Concrete visible attributes", "use": "Declared role"})
	}
	resultBody, _ := json.Marshal(map[string]any{"prompt": "A calm portrait scene.", "references": summaries})
	result, err := parseModelResultWithReferences(string(resultBody), ModeTextToVideo, prepared.References)
	if err != nil || result.References[9].Summary != "Concrete visible attributes" || result.References[10].Inspected {
		t.Fatal("incorrect parsed reference evidence", err)
	}
}

func TestAIHubVideoOnlyRequiresVision(t *testing.T) {
	base, _ := url.Parse("http://localhost:11434")
	_, err := NewClient(base, "text").ForModels("text", "", 0).PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "Follow the motion", nil, VideoContext{Mode: VideoModeREF2VA, VideoReference: true, VideoStoryboard: []byte{1}}, false)
	if err == nil {
		t.Fatal("text-only model accepted video frames")
	}
}
