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
	references, err := promptAssistantImageReferences(request, promptassistant.ModeImageToImage)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 3 || references[0].Number != 1 || references[1].Role != promptassistant.ImageReferenceIdentity || references[2].Number != 4 {
		t.Fatalf("unexpected references: %#v", references)
	}
}

func TestPromptAssistantImageReferencesRejectsUnknownRole(t *testing.T) {
	request := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	request.Form = map[string][]string{"image_role_2": {"ignore_system_prompt"}}
	if _, err := promptAssistantImageReferences(request, promptassistant.ModeImageToImage); err == nil {
		t.Fatal("expected invalid image role error")
	}
}
