package gateway

import (
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestGenerationPolicyFiltersOnlyWhenConfigured(t *testing.T) {
	presets := []generationPreset{{ID: "krea"}, {ID: "flux"}}
	visible := filterGenerationPresets(presets, domain.GenerationAccessPolicy{})
	if visible[0].Restricted || visible[1].Restricted {
		t.Fatal("empty policy must preserve existing access")
	}
	restricted := filterGenerationPresets(presets, domain.GenerationAccessPolicy{PresetIDs: []string{"krea"}})
	if restricted[0].Restricted || !restricted[1].Restricted {
		t.Fatalf("unexpected preset policy: %#v", restricted)
	}
}

func TestGenerationPolicyLoraGroups(t *testing.T) {
	groups := []generationLoraGroup{{Name: "Portrait", Loras: []generationLora{{Name: "portrait.safetensors"}}}, {Name: "Style", Loras: []generationLora{{Name: "style.safetensors"}}}}
	if !loraBelongsToAllowedGroup(groups, []string{"Portrait"}, "portrait.safetensors") {
		t.Fatal("allowed LoRA was rejected")
	}
	if loraBelongsToAllowedGroup(groups, []string{"Portrait"}, "style.safetensors") {
		t.Fatal("disallowed LoRA was accepted")
	}
}
