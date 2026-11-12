package gateway

import (
	"ai-access-gateway/internal/promptassistant"
	"testing"
)

func TestPromptAssistantNegativeMatchesSupportedGraphModes(t *testing.T) {
	for _, tc := range []struct {
		workflow string
		mode     promptassistant.Mode
		profile  promptassistant.Profile
		want     bool
	}{
		{kreaUltraPresetID, promptassistant.ModeTextToImage, promptassistant.ProfilePhotographic, true},
		{kreaProPresetID, promptassistant.ModeTextToImage, promptassistant.ProfileAnime, true},
		{kreaProPresetID, promptassistant.ModeImageToImage, promptassistant.ProfileKrea2Edit, true},
		{mossPresetID, promptassistant.ModeTextToImage, promptassistant.ProfileWorkflowDefault, true},
		{"qwen-image21", promptassistant.ModeTextToImage, promptassistant.ProfileWorkflowDefault, true},
		{"qwen-image21-edit", promptassistant.ModeImageToImage, promptassistant.ProfileQwen21Edit, true},
		{"", promptassistant.ModeImageToImage, promptassistant.ProfileQwen21Edit, true},
		{"photoflow-krea2", promptassistant.ModeTextToImage, promptassistant.ProfileWorkflowDefault, false},
		{"photoflow-krea2-edit", promptassistant.ModeImageToImage, promptassistant.ProfileKrea2Edit, false},
		{"photoflow-flux2-edit", promptassistant.ModeImageToImage, promptassistant.ProfileFluxEdit, false},
		{"qwen-image21", promptassistant.ModeImageToImage, promptassistant.ProfileWorkflowDefault, false},
		{mossPresetID, promptassistant.ModeTextToVideo, promptassistant.ProfileMiniMaxH3, false},
	} {
		if got := promptAssistantSupportsNegative(tc.mode, tc.profile, tc.workflow); got != tc.want {
			t.Errorf("%s/%s/%s = %v, want %v", tc.workflow, tc.mode, tc.profile, got, tc.want)
		}
	}
}
