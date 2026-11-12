package promptassistant

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestReferenceSourcePrioritiesAndDistinctImages(t *testing.T) {
	base, _ := url.Parse("http://localhost:11434")
	refs := []ImageReference{
		{Number: 1, Role: ImageReferenceIdentity, MIMEType: "image/jpeg", Image: []byte("face reference")},
		{Number: 2, Role: ImageReferenceWardrobeObject, MIMEType: "image/jpeg", Image: []byte("clothing reference")},
	}
	video := VideoContext{Mode: VideoModeREF2VA, AIHubMode: "video", ImageCount: 2, VideoReference: true, VideoStoryboard: []byte("video frames"), AudioSource: "original"}
	prepared, err := NewClient(base, "text").WithVisionModel("vision", 0, "0").PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "Animate the person from the first photo", refs, video, false)
	if err != nil {
		t.Fatal(err)
	}
	var body chatRequest
	if err := json.Unmarshal(prepared.Body, &body); err != nil {
		t.Fatal(err)
	}
	images := body.Messages[1].Images
	if len(images) != 3 {
		t.Fatalf("images = %d, want 3", len(images))
	}
	for i, want := range [][]byte{refs[0].Image, refs[1].Image, video.VideoStoryboard} {
		if images[i] != base64.StdEncoding.EncodeToString(want) {
			t.Fatalf("attachment %d substituted or reordered", i+1)
		}
	}
	system := body.Messages[0].Content
	for _, want := range []string{
		"Identity source(s): Picture 1. Wardrobe/object source(s): Picture 2.",
		"Clothing visible in an identity source is not the target outfit",
		"does not cancel the selected wardrobe role",
		"no audio listening/transcription has been performed",
		"BEFORE composing the prompt field",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("missing instruction %q", want)
		}
	}
	if strings.Contains(system, "For a person or base scene") || strings.Contains(system, "Give at least three") {
		t.Fatal("conflicting cross-role attribute instruction remains")
	}
	refs[0].Role, refs[1].Role = refs[1].Role, refs[0].Role
	if got := referenceSourcePriorityInstruction(ModeTextToVideo, refs, video); !strings.Contains(got, "Identity source(s): Picture 2. Wardrobe/object source(s): Picture 1.") {
		t.Fatal("priorities must follow roles, not positions")
	}
	video.Mode = VideoModeFL2VA
	if got := referenceSourcePriorityInstruction(ModeTextToVideo, refs, video); got != "" {
		t.Fatal("source priorities must not override exact keyframes")
	}
}
