package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func ultraInput(t *testing.T) generationForm {
	t.Helper()
	var input generationForm
	if err := applyWorkflowManifestDefaults(kreaUltraWorkflowManifest(), &input); err != nil {
		t.Fatal(err)
	}
	input.PresetID, input.TemplateID = kreaUltraPresetID, "text-to-image"
	input.ModelName = "Krea2/krea2TurboFP8_krea2TURBO.safetensors"
	input.TextEncoder, input.VAE = "qwen3vl_4b_bf16.safetensors", "qwen_image_vae.safetensors"
	input.Positive, input.Seed = "An adult wearing a cotton jacket in a ceramics studio.", 424242
	return input
}

func TestKreaUltraGraphAndDefaults(t *testing.T) {
	input := ultraInput(t)
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, kreaUltraDefinitionID)
	if !ok {
		t.Fatal("missing workflow")
	}
	graph, err := definition.buildPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph) != 9 {
		t.Fatalf("unexpected graph size %d", len(graph))
	}
	sam := graph["7"].(map[string]any)["inputs"].(map[string]any)
	if sam["steps"] != 10 || sam["cfg_high"] != float64(2) || sam["cfg_low"] != float64(1) || sam["detail_amount"] != 0.34 || sam["speed_boost"] != false {
		t.Fatalf("unexpected sampler %#v", sam)
	}
	for _, node := range graph {
		kind := node.(map[string]any)["class_type"]
		if kind == "LoraLoader" || kind == "ImageScale" || kind == "ColorTransfer" {
			t.Fatalf("unrequested stage %v", kind)
		}
	}
	if err := validateWorkflowManifest(kreaUltraWorkflowManifest()); err != nil {
		t.Fatal(err)
	}
}

func TestKreaUltraBoundsAndRawRejected(t *testing.T) {
	for _, change := range []func(*generationForm){
		func(v *generationForm) { v.KreaUltraStepSwap = v.Steps },
		func(v *generationForm) { v.KreaUltraSpeedBoost = true; v.KreaUltraGrowStep = v.KreaUltraStepSwap },
		func(v *generationForm) { v.Width = 4096; v.Height = 4096 },
		func(v *generationForm) { v.ModelName = "Krea2/krea2Raw_v10.safetensors" },
	} {
		input := ultraInput(t)
		change(&input)
		if err := normalizeKreaUltra(&input); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}

func TestKreaUltraFormFieldsRoundTrip(t *testing.T) {
	values := url.Values{"template_id": {"text-to-image"}, "generation_workflow": {kreaUltraPresetID}, "positive_prompt": {"a ceramic cup"},
		"steps": {"14"}, "cfg": {"2"}, "scheduler": {"simple"}, "krea_ultra_step_swap": {"8"}, "krea_ultra_low_cfg": {"1.2"},
		"krea_ultra_detail_amount": {"0.5"}, "krea_ultra_speed_boost": {"true"}, "krea_ultra_grow_step": {"4"}}
	r := httptest.NewRequest("POST", "/generate/run", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	input, err := parseGenerationForm(r)
	if err != nil {
		t.Fatal(err)
	}
	if input.KreaUltraStepSwap != 8 || input.KreaUltraDetailAmount != 0.5 || !input.KreaUltraSpeedBoost || input.KreaUltraLowCFG != 1.2 {
		t.Fatalf("lost fields %#v", input)
	}
	serialized, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var restored generationForm
	if err := json.Unmarshal(serialized, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.KreaUltraStepSwap != 8 || !restored.KreaUltraSpeedBoost {
		t.Fatal("job persistence lost ultra fields")
	}
}
