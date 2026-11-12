package gateway

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMiniMaxErosVersion(t *testing.T) {
	for name, want := range map[string]int{
		"MiniMax\\h3ErosMax_beta6_3283276.safetensors": 6,
		"MiniMax/h3ErosMax-beta6.safetensors":          6,
		"MiniMax/h3ErosMax_beta4.safetensors":          4,
		"MiniMax/h3ErosMax_beta60.safetensors":         60,
		"MiniMax/h3ErosMax_beta6fake.safetensors":      0,
		"MiniMax/random_TURBO.safetensors":             0,
		"h3ErosMax_beta6/not-a-model.safetensors":      0,
	} {
		if got := miniMaxErosVersion(name); got != want {
			t.Errorf("%s: got %d want %d", name, got, want)
		}
	}
}

func TestMiniMaxErosBeta6CatalogAndGraph(t *testing.T) {
	info := aiHubTestSchema(t)
	if _, ok := info["MiniMaxH3SigmaShift"]; !ok {
		body, err := os.ReadFile("testdata/object_info/minimax_h3_v4.json")
		if err != nil {
			t.Fatal(err)
		}
		var fixture struct {
			Nodes map[string]comfyNodeInfo `json:"nodes"`
		}
		if err := json.Unmarshal(body, &fixture); err != nil {
			t.Fatal(err)
		}
		info["MiniMaxH3SigmaShift"] = fixture.Nodes["MiniMaxH3SigmaShift"]
	}
	name := "MiniMax\\h3ErosMax_beta6_3283276.safetensors"
	loader := info["UNETLoader"]
	loader.Input.Required["unet_name"], _ = json.Marshal([]any{[]string{name}})
	info["UNETLoader"] = loader
	var found *generationModel
	for _, group := range buildGenerationModelCatalog(info).Groups {
		for _, model := range group.Models {
			if model.Name == name && !model.AIHubOnly {
				copy := model
				found = &copy
			}
		}
	}
	if found == nil || found.AIHubOnly || !found.VideoIntegratedTurbo || !found.VideoReferenceOnly {
		t.Fatal("beta6 missing from ordinary video catalog", found)
	}
	if found.DefaultSteps != 10 || found.DefaultVideoShift != 10 || found.DefaultAudioShift != 3 {
		t.Fatal("beta6 inherited an older sampling profile", found)
	}
	input := generationForm{PresetID: "minimax-h3-video", ModelName: name, ReferenceModel: name,
		TextEncoder: found.TextEncoder, VAE: found.VAE, AudioVAE: found.AudioVAE, Positive: "A vase on a table.",
		Width: 384, Height: 704, VideoDurationSeconds: 5, VideoQuality: 480,
		VideoIntegratedTurbo: true, VideoReferenceOnly: true, VideoTurbo: true}
	if err := normalizeMiniMaxH3(&input); err != nil {
		t.Fatal(err)
	}
	if input.VideoSteps != 10 || input.VideoShiftVideo != 10 || input.VideoShiftAudio != 3 || input.VideoTurbo {
		t.Fatal("incorrect beta6 normalization", input.VideoSteps, input.VideoShiftVideo, input.VideoShiftAudio)
	}
	manifest := miniMaxH3WorkflowManifest()
	if err := validateWorkflowManifestInput(manifest, input); err != nil {
		t.Fatal(err)
	}
	graph, err := buildMiniMaxH3Prompt(input)
	if err != nil {
		t.Fatal(err)
	}
	if issues := validateComfyPrompt(buildComfySchemaCatalog(info, time.Now(), "beta6-test"), graph); len(issues) != 0 {
		t.Fatalf("graph incompatible: %+v", issues)
	}
	for _, raw := range graph {
		if raw.(map[string]any)["class_type"] == "MiniMaxH3TurboLoRA" {
			t.Fatal("duplicate external Turbo applied")
		}
	}
	encoded, _ := json.Marshal(graph)
	if !strings.Contains(string(encoded), "MiniMaxH3SigmaShift") {
		t.Fatal("beta6 requires explicit 10/3 shift")
	}
	for _, steps := range []int{8, 10, 12} {
		copy := input
		copy.VideoSteps = steps
		if err := normalizeMiniMaxH3(&copy); err != nil {
			t.Fatalf("valid step count %d rejected: %v", steps, err)
		}
		if err := validateWorkflowManifestInput(manifest, copy); err != nil {
			t.Fatal(err)
		}
	}
	for _, steps := range []int{6, 7, 13} {
		copy := input
		copy.VideoSteps = steps
		if normalizeMiniMaxH3(&copy) == nil {
			t.Errorf("invalid beta6 step count %d accepted", steps)
		}
	}
}

func TestMiniMaxErosBeta6AvailableInAIHubAndOrdinaryVideo(t *testing.T) {
	info := aiHubTestSchema(t)
	info["MiniMaxH3SigmaShift"] = loadObjectInfoFixture(t, "minimax_h3_v4.json").Nodes["MiniMaxH3SigmaShift"]
	name := "MiniMax\\h3ErosMax_beta6_3283276.safetensors"
	loader := info["UNETLoader"]
	loader.Input.Required["unet_name"], _ = json.Marshal([]any{[]string{name, "MiniMax/MiniMax_H3_Ref2VA_pruned_int8_convrot.safetensors"}})
	info["UNETLoader"] = loader
	catalog := buildGenerationModelCatalog(info)
	ordinary, aiHub := 0, 0
	for _, group := range catalog.Groups {
		for _, model := range group.Models {
			if model.Name != name {
				continue
			}
			if model.AIHubOnly {
				aiHub++
				if !model.Available || model.DefaultSteps != 10 || !model.VideoIntegratedTurbo {
					t.Fatalf("AIHUB beta6 unavailable or misconfigured: %+v", model)
				}
			} else {
				ordinary++
			}
		}
	}
	if ordinary != 1 || aiHub != 1 {
		t.Fatalf("beta6 profiles: ordinary=%d, AIHUB=%d", ordinary, aiHub)
	}
	preset, ok := findGenerationPreset(buildGenerationPresets(catalog), aiHubWorkflowID, "minimax-h3-video")
	if !ok || !preset.Available || preset.ModelID != generationModelID(modelFamilyMiniMaxH3, name)+":aihub" || preset.DefaultSteps != 10 {
		t.Fatalf("AIHUB did not select installed beta6: %+v", preset)
	}
}

func TestAIHubBeta6SamplingAndStepLimits(t *testing.T) {
	for _, mode := range []string{"photo", "video", "carousel"} {
		input := aiHubTestInput(t)
		input.ModelName = "MiniMax/h3ErosMax_beta6_3283276.safetensors"
		input.AIHubMode, input.AIHubSteps = mode, 0
		input.AIHubLatentModel = "minimax_h3_latent_upscaler_3d_bf16.safetensors"
		if mode == "photo" {
			input.InputVideo = ""
		}
		if err := normalizeAIHub(&input); err != nil {
			t.Fatal(err)
		}
		if input.AIHubSteps != 10 {
			t.Fatal("beta6 inherited beta5 default steps", input.AIHubSteps)
		}
		for _, steps := range []int{8, 10, 12} {
			copy := input
			copy.AIHubSteps = steps
			if err := normalizeAIHub(&copy); err != nil {
				t.Fatal(err)
			}
			graph, err := buildAIHubPrompt(copy)
			if err != nil {
				t.Fatal(err)
			}
			fields := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
			if fields("11")["sampler_name"] != "euler" || fields("12")["scheduler"] != "simple" || fields("12")["steps"] != steps {
				t.Fatalf("%s beta6 received beta5 sampling", mode)
			}
			if fields("9")["shift_video"] != 10.0 || fields("9")["shift_audio"] != 3.0 {
				t.Fatal("beta6 shifts ignored")
			}
		}
		for _, steps := range []int{6, 7, 13, 25} {
			copy := input
			copy.AIHubSteps = steps
			if normalizeAIHub(&copy) == nil {
				t.Fatal("invalid beta6 steps accepted", steps)
			}
		}
		if beta5, _ := workflowConditionValue(miniMaxH3WorkflowManifest(), generationForm{ModelName: input.ModelName, VideoIntegratedTurbo: true}, "video_beta5"); beta5 != false {
			t.Fatal("beta6 misclassified as beta5")
		}
	}
}
