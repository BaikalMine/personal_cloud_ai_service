package promptassistant

import (
	"strings"
	"testing"
)

func TestImageEditProfilesUseDistinctInstructions(t *testing.T) {
	if !ValidProfile(ModeImageToImage, ProfileKrea2Edit) || ValidProfile(ModeTextToImage, ProfileKrea2Edit) {
		t.Fatal("Krea2 edit profile must only apply to image editing")
	}
	refs := []ImageReference{{Number: 1, Role: ImageReferenceBaseScene}, {Number: 2, Role: ImageReferenceWardrobeObject}}
	for _, tc := range []struct {
		profile Profile
		want    string
		avoid   string
	}{
		{ProfileKrea2Edit, "The grounded encoder already sees the source image", "generator of edit prompts for FLUX"},
		{ProfileFluxEdit, "Other images contribute only their assigned roles", "Krea 2 Identity Edit"},
		{ProfileQwen21Edit, "Lead with the requested operation", "generator of edit prompts for FLUX"},
	} {
		got := SystemPromptWithReferences(ModeImageToImage, tc.profile, refs)
		if !strings.Contains(got, tc.want) || strings.Contains(got, tc.avoid) {
			t.Fatalf("wrong %s instruction: %s", tc.profile, got)
		}
	}
}

func TestQwen21SingleImageUsesNaturalReference(t *testing.T) {
	got := SystemPromptWithReferences(ModeImageToImage, ProfileQwen21Edit, []ImageReference{{Number: 1, Role: ImageReferenceBaseScene}})
	if !strings.Contains(got, "do not emit <image1>") || strings.Contains(got, "- <image1>:") {
		t.Fatalf("single-image Qwen edit must not require numbered tags: %s", got)
	}
}
