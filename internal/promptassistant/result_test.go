package promptassistant

import (
	"strings"
	"testing"
	"time"
)

func TestParseModelResultBuildsAuthoritativeReferenceMap(t *testing.T) {
	references := []ImageReference{
		{Number: 1, Role: ImageReferenceBaseScene},
		{Number: 2, Role: ImageReferenceWardrobeObject},
	}
	raw := `{"prompt":"The subject turns toward camera.","references":[{"id":"<Picture 1>","summary":"A woman in a sunlit studio.","use":"Keep the studio and framing."},{"id":"Picture 2","summary":"A red leather jacket with silver zips.","use":"Transfer the jacket."},{"id":"Video 1","summary":"Invented video details.","use":"Follow motion."},{"id":"Audio 1","summary":"Invented audio details.","use":"Follow voice."}]}`
	result, err := parseModelResult(raw, ModeTextToVideo, references, VideoContext{
		Mode: "references", VideoReference: true, AudioReference: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.References) != 4 {
		t.Fatalf("reference map length = %d, want 4", len(result.References))
	}
	if result.References[0].Identifier != "Picture 1" || !result.References[0].Inspected ||
		result.References[1].Identifier != "Picture 2" || !result.References[1].Inspected {
		t.Fatalf("image references were not normalized: %+v", result.References)
	}
	if result.References[2].Identifier != "Video 1" || result.References[2].Inspected || strings.Contains(result.References[2].Summary, "Invented") {
		t.Fatalf("video reference must keep the authoritative uninspected summary: %+v", result.References[2])
	}
	if result.References[3].Identifier != "Audio 1" || result.References[3].Inspected || strings.Contains(result.References[3].Summary, "Invented") {
		t.Fatalf("audio reference must keep the authoritative uninspected summary: %+v", result.References[3])
	}
}

func TestParseModelResultRequiresEveryImageUnderstanding(t *testing.T) {
	_, err := parseModelResult(`{"prompt":"A final prompt.","references":[]}`, ModeImageToImage, []ImageReference{
		{Number: 1, Role: ImageReferenceIdentity},
	}, VideoContext{})
	if err == nil || !strings.Contains(err.Error(), "Picture 1") {
		t.Fatalf("expected missing reference error, got %v", err)
	}
}

func TestModelPolicySeparatesImageAndVideoBudgets(t *testing.T) {
	policy := ModelPolicy{
		ImageNumPredict: 700, ImageThinkNumPredict: 1300,
		VideoNumPredict: 2200, VideoThinkNumPredict: 3600,
		ImageTimeout: 80 * time.Second, VideoTimeout: 5 * time.Minute, KeepAlive: "20s",
	}
	image := policy.request(ModeTextToImage, ProfilePhotographic, true)
	video := policy.request(ModeTextToVideo, ProfileMiniMaxH3REF2VA, false)
	if image.NumPredict != 1300 || image.Timeout != 80*time.Second || image.KeepAlive != "20s" {
		t.Fatalf("unexpected image policy: %+v", image)
	}
	if video.NumPredict != 2200 || video.Timeout != 5*time.Minute || video.KeepAlive != "20s" {
		t.Fatalf("unexpected video policy: %+v", video)
	}
}

func TestParseModelResultUsesVideoSpecificCharacterLimit(t *testing.T) {
	videoPrompt := strings.Repeat("v", 5000)
	result, err := parseModelResult(videoPrompt, ModeTextToVideo, nil, VideoContext{})
	if err != nil || result.Prompt != videoPrompt {
		t.Fatalf("5000-character video prompt = %d chars, err = %v", len(result.Prompt), err)
	}
	if _, err := parseModelResult(videoPrompt, ModeTextToImage, nil, VideoContext{}); err == nil || !strings.Contains(err.Error(), "4000") {
		t.Fatalf("image prompt must keep the 4000-character limit, got %v", err)
	}
	if _, err := parseModelResult(strings.Repeat("v", 7001), ModeTextToVideo, nil, VideoContext{}); err == nil || !strings.Contains(err.Error(), "7000") {
		t.Fatalf("video prompt must reject more than 7000 characters, got %v", err)
	}
}

func TestParseModelResultCountsUnicodeCharactersNotBytes(t *testing.T) {
	prompt := strings.Repeat("я", MaxImagePromptCharacters)
	result, err := parseModelResult(prompt, ModeTextToImage, nil, VideoContext{})
	if err != nil || result.Prompt != prompt {
		t.Fatalf("unicode prompt at the visible character limit was rejected: %v", err)
	}
}
