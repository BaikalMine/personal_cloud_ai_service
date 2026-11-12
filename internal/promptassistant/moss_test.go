package promptassistant

import (
	"strings"
	"testing"
)

func TestMossInstructionKeepsFamilyAndNegativeSeparate(t *testing.T) {
	guide := EnhancementInstruction(ModeTextToImage, ProfileRealistic, nil, VideoContext{}, EditContext{ImageFamily: "moss", CFG: 3})
	for _, expected := range []string{"MOSS-Image text-to-image", "visible text verbatim", "negative_prompt", "Never create, rewrite"} {
		if !strings.Contains(guide, expected) {
			t.Errorf("MOSS guide is missing %q", expected)
		}
	}
	other := EnhancementInstruction(ModeTextToImage, ProfileRealistic, nil, VideoContext{}, EditContext{})
	if strings.Contains(other, "MOSS-Image") {
		t.Fatal("MOSS guide leaked into another workflow")
	}
}
