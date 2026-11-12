package gateway

import (
	"reflect"
	"testing"
)

func TestAIHubPreservesReferenceTailAndTrimsCarouselPadding(t *testing.T) {
	in := aiHubTestInput(t)
	in.AIHubMode = "carousel"
	in.AIHubLatentModel = "minimax_h3_latent_upscaler_3d_bf16.safetensors"
	graph, err := buildAIHubPrompt(in)
	if err != nil {
		t.Fatal(err)
	}
	inputs := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
	for key, want := range map[string][]any{"ref_videos.ref_video_0": {"31", 0}, "length": {"31", 1}} {
		if !reflect.DeepEqual(inputs("7")[key], want) {
			t.Fatal("H3 receives unaligned reference", key)
		}
	}
	if !reflect.DeepEqual(inputs("66")["length"], []any{"31", 2}) || !reflect.DeepEqual(inputs("65")["images"], []any{"66", 0}) {
		t.Fatal("padded frames leaked into carousel")
	}
}

func TestAIHubGalaxyAceRemainsOptional(t *testing.T) {
	in := aiHubTestInput(t)
	in.AIHubMode = "carousel"
	in.AIHubLatentModel = "minimax_h3_latent_upscaler_3d_bf16.safetensors"
	for _, enabled := range []bool{false, true} {
		if enabled {
			in.LoraNames[0], in.LoraModel[0] = "MiniMaxH3/H3-GalaxyAce.safetensors", 0.7
		}
		graph, err := buildAIHubPrompt(in)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, raw := range graph {
			n := raw.(map[string]any)
			if n["class_type"] == "LoraLoaderModelOnly" {
				found = true
			}
		}
		if found != enabled {
			t.Fatalf("implicit LoRA selection: enabled=%v found=%v", enabled, found)
		}
	}
}

func TestAIHubAuthorNRProfiles(t *testing.T) {
	v, p := aiHubDLSSSettings(1, false), aiHubDLSSSettings(1, true)
	for k, want := range map[string]any{"local_tone_strength": 1.0, "local_structure_strength": 0.5, "skin_structure_strength": 0.0, "warmup_frames": 8, "scene_change_threshold": 0.15, "nr_intensity": 1.0, "dlss_model_preset": "Default"} {
		if v[k] != want {
			t.Errorf("video %s=%v, want %v", k, v[k], want)
		}
	}
	for k, want := range map[string]any{"nr_style": "Natural", "nr_intensity": 1.15, "local_tone_strength": 0.75, "local_structure_strength": 0.7, "skin_structure_strength": 0.4, "motion": "none", "warmup_frames": 0} {
		if p[k] != want {
			t.Errorf("photo %s=%v, want %v", k, p[k], want)
		}
	}
}
