package gateway

import (
	"reflect"
	"strings"
	"testing"
)

func TestAIHubAudioConditioningIsExplicitAndIndependentOfT8(t *testing.T) {
	for _, source := range []string{"original", "ltx"} {
		for _, reference := range []bool{false, true} {
			for _, t8 := range []bool{false, true} {
				in := aiHubTestInput(t)
				in.AIHubAudioSource, in.AIHubAudioReference, in.AIHubT8 = source, reference, t8
				in.AIHubAudioPrompt = "Quiet street ambience."
				in.AIHubLTXModel, in.AIHubGemma, in.AIHubAudioVAE = "ltx", "gemma", "vae"
				graph, err := buildAIHubPrompt(in)
				if err != nil {
					t.Fatal(err)
				}
				inputs := graph["7"].(map[string]any)["inputs"].(map[string]any)
				key, other, link := "ref_video_audios.ref_video_audio_0", "ref_audios.ref_audio_0", []any{"20", 2}
				if source == "ltx" {
					key, other, link = other, key, []any{"118", 0}
				}
				if reference && !reflect.DeepEqual(inputs[key], link) {
					t.Fatalf("%s reference: %v", source, inputs)
				}
				if !reference && inputs[key] != nil || inputs[other] != nil {
					t.Fatal("unexpected audio reference", inputs)
				}
				if (graph["120"] != nil) != t8 {
					t.Fatal("reference changed T8 selection")
				}
			}
		}
	}
}

func TestAIHubMaskProfilesMatchAuthor(t *testing.T) {
	for _, tc := range []struct {
		mask         string
		expand, blur int
	}{{"sam3", 18, 4}, {"sapiens", 28, 8}} {
		in := aiHubTestInput(t)
		in.AIHubMask, in.AIHubMaskModel = tc.mask, "mask-model"
		graph, err := buildAIHubPrompt(in)
		if err != nil {
			t.Fatal(err)
		}
		inputs := graph["28"].(map[string]any)["inputs"].(map[string]any)
		if inputs["expand"] != tc.expand || inputs["blur_radius"] != tc.blur {
			t.Fatal(tc.mask, inputs)
		}
	}
}

func TestAIHubEditingValidationAndPrompt(t *testing.T) {
	for _, task := range []string{"replace_character", "replace_clothing"} {
		in := aiHubTestInput(t)
		in.AIHubTask = task
		if normalizeAIHub(&in) == nil {
			t.Fatal("replacement without an explicit photo role accepted")
		}
		in.ReferenceMetadata[0].Role = "identity"
		if task == "replace_clothing" {
			in.ReferenceMetadata[0].Role = "wardrobe_object"
		}
		if err := normalizeAIHub(&in); err != nil {
			t.Fatal(err)
		}
		graph, err := buildAIHubPrompt(in)
		if err != nil {
			t.Fatal(err)
		}
		prompt := graph["7"].(map[string]any)["inputs"].(map[string]any)["prompt"].(string)
		if !strings.HasPrefix(prompt, "Video editing:") || !strings.HasSuffix(prompt, in.Positive) {
			t.Fatal(prompt)
		}
		in.InputImage = ""
		if normalizeAIHub(&in) == nil {
			t.Fatal("replacement without photo accepted")
		}
		in.InputImage, in.AIHubMode, in.InputVideo = "photo.png", "photo", ""
		if normalizeAIHub(&in) == nil {
			t.Fatal("replacement without video accepted")
		}
	}
	in := aiHubTestInput(t)
	in.AIHubAudioReference = true
	if normalizeAIHub(&in) == nil {
		t.Fatal("native audio reference accepted")
	}
}
