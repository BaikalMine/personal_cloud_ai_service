package gateway

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMiniMaxBeta5StandardAndAIHubProfiles(t *testing.T) {
	info := aiHubTestSchema(t)
	catalog := buildGenerationModelCatalog(info)
	var standard, hub *generationModel
	for _, group := range catalog.Groups {
		for _, model := range group.Models {
			if !strings.Contains(model.Name, "10Eros") {
				continue
			}
			copy := model
			if model.AIHubOnly {
				hub = &copy
			} else {
				standard = &copy
			}
		}
	}
	if standard == nil || hub == nil || standard.ID == hub.ID || standard.Name != hub.Name || !standard.VideoIntegratedTurbo || !standard.VideoReferenceOnly {
		t.Fatal("missing independent workflow profiles", standard, hub)
	}
	if standard.DefaultSampler != "er_sde" || standard.DefaultScheduler != "beta57" || standard.DefaultSteps != 6 {
		t.Fatal("wrong beta5 defaults")
	}
	// The archived AIHUB-only schema omits optional standard-workflow LoRA nodes.
	if os.Getenv("AIHUB_SCHEMA_TEST_FILE") != "" && !standard.Available {
		t.Fatal(standard.Reason)
	}
	input := generationForm{PresetID: "minimax-h3-video", ModelName: standard.Name, ReferenceModel: standard.Name, TextEncoder: standard.TextEncoder, VAE: standard.VAE, AudioVAE: standard.AudioVAE,
		Positive: "A vase on a table.", Width: 384, Height: 704, VideoDurationSeconds: 5, VideoQuality: 480, VideoIntegratedTurbo: true, VideoReferenceOnly: true, VideoTurbo: true, VideoSteps: 6}
	if err := normalizeMiniMaxH3(&input); err != nil {
		t.Fatal(err)
	}
	if input.VideoTurbo || input.VideoMode != "references" || input.Sampler != "er_sde" || input.Scheduler != "beta57" {
		t.Fatal("incorrect beta5 normalization")
	}
	if err := validateWorkflowManifestInput(miniMaxH3WorkflowManifest(), input); err != nil {
		t.Fatal(err)
	}
	graph, err := buildMiniMaxH3Prompt(input)
	if err != nil {
		t.Fatal(err)
	}
	if issues := validateComfyPrompt(buildComfySchemaCatalog(info, time.Now(), "beta5-test"), graph); len(issues) != 0 {
		t.Fatalf("graph incompatible: %+v", issues)
	}
	for _, raw := range graph {
		if class := raw.(map[string]any)["class_type"]; class == "MiniMaxH3TurboLoRA" || class == "MiniMaxH3SigmaShift" {
			t.Fatal("beta4/external turbo leaked into beta5", class)
		}
	}
	scheduler := graph["12"].(map[string]any)["inputs"].(map[string]any)
	guider := graph["13"].(map[string]any)["inputs"].(map[string]any)
	if scheduler["scheduler"] != "beta57" || !reflect.DeepEqual(scheduler["model"], guider["model"]) {
		t.Fatal("sampling model mismatch")
	}
	if graph["11"].(map[string]any)["inputs"].(map[string]any)["sampler_name"] != "er_sde" {
		t.Fatal("wrong sampler")
	}
}
