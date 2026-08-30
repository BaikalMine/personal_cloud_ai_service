package gateway

import (
	"net/http/httptest"
	"testing"

	"ai-access-gateway/internal/promptassistant"
)

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

func TestPromptAssistantVideoContextAcceptsReferenceAudioOnlyInReferenceMode(t *testing.T) {
	referenceRequest := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	referenceRequest.Form = map[string][]string{
		"video_mode": {"references"}, "video_duration_seconds": {"10"}, "video_image_count": {"3"}, "video_has_audio": {"true"}, "video_has_video": {"true"},
	}
	context, err := promptAssistantVideoContext(referenceRequest, promptassistant.ModeTextToVideo)
	if err != nil || !context.AudioReference || !context.VideoReference || context.ImageCount != 3 || context.DurationSeconds != 10 {
		t.Fatalf("context = %#v, err = %v", context, err)
	}
	frameRequest := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	frameRequest.Form = map[string][]string{"video_mode": {"frames"}, "video_has_audio": {"true"}}
	if _, err := promptAssistantVideoContext(frameRequest, promptassistant.ModeTextToVideo); err == nil {
		t.Fatal("frame mode must reject a standalone audio reference")
	}
}

func TestPromptAssistantVideoContextSupportsTextToVideoWithoutImages(t *testing.T) {
	request := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	request.Form = map[string][]string{
		"video_mode": {"frames"}, "video_duration_seconds": {"60"}, "video_image_count": {"0"},
	}
	context, err := promptAssistantVideoContext(request, promptassistant.ModeTextToVideo)
	if err != nil || context.ImageCount != 0 || context.DurationSeconds != 60 {
		t.Fatalf("context = %#v, err = %v", context, err)
	}
}
