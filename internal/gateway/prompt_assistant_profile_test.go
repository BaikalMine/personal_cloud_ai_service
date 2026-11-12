package gateway

import (
	"testing"

	"ai-access-gateway/internal/promptassistant"
)

func TestImageEditAssistantProfileMatchesWorkflow(t *testing.T) {
	for _, tc := range []struct {
		workflow string
		profile  promptassistant.Profile
		want     promptassistant.Profile
	}{
		{"photoflow-krea2-edit", promptassistant.ProfileWorkflowDefault, promptassistant.ProfileKrea2Edit},
		{"photoflow-krea2-edit", promptassistant.ProfileFluxEdit, promptassistant.ProfileKrea2Edit},
		{"photoflow-flux2-edit", promptassistant.ProfileWorkflowDefault, promptassistant.ProfileFluxEdit},
		{"photoflow-flux2-edit", promptassistant.ProfileKrea2Edit, promptassistant.ProfileFluxEdit},
		{"qwen-image21-edit", promptassistant.ProfileWorkflowDefault, promptassistant.ProfileQwen21Edit},
		{"photoflow-krea2-edit", promptassistant.ProfilePhotographic, promptassistant.ProfilePhotographic},
		{"", promptassistant.ProfileWorkflowDefault, promptassistant.ProfileWorkflowDefault},
	} {
		if got := imageEditAssistantProfile(tc.workflow, tc.profile); got != tc.want {
			t.Errorf("%s / %s: got %s, want %s", tc.workflow, tc.profile, got, tc.want)
		}
	}
	if got := krea2EditWorkflowManifest().PromptAssistant.Profile; got != "krea2-edit" {
		t.Fatalf("Krea2 manifest advertises %s instead of its own edit profile", got)
	}
}
