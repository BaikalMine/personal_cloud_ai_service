package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"ai-access-gateway/internal/promptassistant"
)

func TestPromptAssistantImageCorrectionIsScopedAndBounded(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Form = map[string][]string{"image_correction_2": {"  Light hair, not dark.  "}}
	value, err := promptAssistantImageCorrection(r, 2)
	if err != nil || value != "Light hair, not dark." {
		t.Fatalf("correction %q: %v", value, err)
	}
	value, err = promptAssistantImageCorrection(r, 1)
	if err != nil || value != "" {
		t.Fatalf("correction leaked to another image: %q %v", value, err)
	}
	r.Form.Set("image_correction_2", strings.Repeat("x", 501))
	if _, err = promptAssistantImageCorrection(r, 2); err == nil {
		t.Fatal("unbounded correction")
	}
}

func TestPromptAssistantImageReferencesAcceptsKnownRolesInOrder(t *testing.T) {
	request := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	request.Form = map[string][]string{
		"image_role_1": {"base_scene"},
		"image_role_2": {"identity"},
		"image_role_4": {"details"},
	}
	role1, err := promptAssistantImageRole(request, 1)
	if err != nil || role1 != promptassistant.ImageReferenceBaseScene {
		t.Fatalf("first role = %q, err = %v", role1, err)
	}
	role2, err := promptAssistantImageRole(request, 2)
	if err != nil || role2 != promptassistant.ImageReferenceIdentity {
		t.Fatalf("second role = %q, err = %v", role2, err)
	}
	role4, err := promptAssistantImageRole(request, 4)
	if err != nil || role4 != promptassistant.ImageReferenceDetails {
		t.Fatalf("fourth role = %q, err = %v", role4, err)
	}
}

func TestPromptAssistantImageReferencesRejectsUnknownRole(t *testing.T) {
	request := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	request.Form = map[string][]string{"image_role_2": {"ignore_system_prompt"}}
	if _, err := promptAssistantImageRole(request, 2); err == nil {
		t.Fatal("expected invalid image role error")
	}
}

func TestReleasePromptAssistantImagesClearsBuffers(t *testing.T) {
	references := []promptassistant.ImageReference{
		{Number: 1, Image: []byte("first image")},
		{Number: 2, Image: []byte("second image")},
	}
	releasePromptAssistantImages(references)
	for index, reference := range references {
		if reference.Image != nil {
			t.Fatalf("reference %d image buffer was not released", index+1)
		}
	}
}

func TestPromptAssistantVideoContextAcceptsReferenceAudioOnlyInReferenceMode(t *testing.T) {
	referenceRequest := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	referenceRequest.Form = map[string][]string{
		"video_mode": {"references"}, "video_duration_seconds": {"10"}, "video_has_audio": {"true"}, "video_has_video": {"true"},
	}
	context, err := promptAssistantVideoContext(referenceRequest, promptassistant.ModeTextToVideo, 3)
	if err != nil || !context.AudioReference || !context.VideoReference || context.ImageCount != 3 || context.DurationSeconds != 10 {
		t.Fatalf("context = %#v, err = %v", context, err)
	}
	frameRequest := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	frameRequest.Form = map[string][]string{"video_mode": {"frames"}, "video_has_audio": {"true"}}
	if _, err := promptAssistantVideoContext(frameRequest, promptassistant.ModeTextToVideo, 0); err == nil {
		t.Fatal("frame mode must reject a standalone audio reference")
	}
}

func TestPromptAssistantVideoContextSupportsTextToVideoWithoutImages(t *testing.T) {
	request := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	request.Form = map[string][]string{
		"video_mode": {"frames"}, "video_duration_seconds": {"60"},
	}
	context, err := promptAssistantVideoContext(request, promptassistant.ModeTextToVideo, 0)
	if err != nil || context.ImageCount != 0 || context.DurationSeconds != 60 {
		t.Fatalf("context = %#v, err = %v", context, err)
	}
}

func TestPromptAssistantVideoContextSupportsPromptOnlyReferenceMode(t *testing.T) {
	request := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	request.Form = map[string][]string{"video_mode": {"references"}}
	context, err := promptAssistantVideoContext(request, promptassistant.ModeTextToVideo, 0)
	if err != nil || context.Mode != miniMaxH3ReferenceMode || context.ImageCount != 0 || context.AudioReference || context.VideoReference {
		t.Fatalf("context = %#v, err = %v", context, err)
	}
}
