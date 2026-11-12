package gateway

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
)

func mossTestInput() generationForm {
	return generationForm{TemplateID: "text-to-image", PresetID: mossPresetID, ModelFamily: modelFamilyMoss,
		ModelName: "MOSSImage/pornmasterMoss_v1.safetensors", TextEncoder: "MOSSImage/qwen3.5_9b_int8_convrot.safetensors", VAE: "flux2-vae.safetensors",
		Positive: "A blue ceramic teapot on a wooden table, natural window light", Negative: "watermark, blurry",
		Width: 1024, Height: 1024, OutputMegapixels: 1, Steps: 30, CFG: 3, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42}
}

func mossTestDefinition(t *testing.T) workflowDefinition {
	t.Helper()
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "text-to-image-moss")
	if !ok {
		t.Fatal("MOSS workflow missing")
	}
	return definition
}

func TestMossGraphPreservesAuthorT2ISettings(t *testing.T) {
	prompt, err := mossTestDefinition(t).buildPrompt(mossTestInput())
	if err != nil {
		t.Fatal(err)
	}
	sampler := prompt["4"].(map[string]any)["inputs"].(map[string]any)
	if sampler["steps"] != 30 || sampler["cfg"] != 3.0 || sampler["sampler_name"] != "euler" || sampler["scheduler"] != "simple" || sampler["denoise"] != 1 {
		t.Fatalf("sampling = %#v", sampler)
	}
	if prompt["126"].(map[string]any)["class_type"] != "EmptyFlux2LatentImage" {
		t.Fatal("MOSS must use the FLUX.2 128-channel latent")
	}
	clip := prompt["34"].(map[string]any)["inputs"].(map[string]any)
	if clip["type"] != "moss_image" {
		t.Fatal("wrong text encoder type")
	}
	if _, ok := prompt["50"]; ok {
		t.Fatal("SeedVR2 was enabled implicitly")
	}
	for _, raw := range prompt {
		if node, _ := promptNode(raw); node["class_type"] == "LoadImage" {
			t.Fatal("T2I unexpectedly requires a photo")
		}
	}
}

func TestMossOptionalUpscaleAndLora(t *testing.T) {
	input := mossTestInput()
	input.MossSeedVR2, input.MossUpscaleResolution = true, 2560
	input.LoraNames[0], input.LoraModel[0] = "MOSSImage/style.safetensors", 0.75
	prompt, err := mossTestDefinition(t).buildPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	lora := prompt["moss_lora_1"].(map[string]any)
	if lora["class_type"] != "LoraLoaderModelOnly" || lora["inputs"].(map[string]any)["strength_model"] != 0.75 {
		t.Fatal("MOSS LoRA was not bound")
	}
	if source := prompt["63"].(map[string]any)["inputs"].(map[string]any)["images"].([]any); source[0] != "50" {
		t.Fatal("save bypasses selected SeedVR2")
	}
	upscale := prompt["50"].(map[string]any)["inputs"].(map[string]any)
	if upscale["resolution"] != 2560 || upscale["batch_size"] != 1 || upscale["prepend_frames"] != 0 {
		t.Fatalf("upscale = %#v", upscale)
	}
}

func TestMossResolutionLimitsAndNoPhotoInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*generationForm)
	}{
		{"too many pixels", func(input *generationForm) { input.Width, input.Height = 3024, 4032 }},
		{"wrong alignment", func(input *generationForm) { input.Width = 1032 }},
		{"photo", func(input *generationForm) { input.InputImage = "gateway/source.png" }},
		{"upscale ceiling", func(input *generationForm) { input.MossUpscaleResolution = 8192 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := mossTestInput()
			test.mutate(&input)
			if _, err := mossTestDefinition(t).buildPrompt(input); err == nil {
				t.Fatal("invalid MOSS input accepted")
			}
		})
	}
	input := mossTestInput()
	input.AspectRatio, input.OutputMegapixels = "3:4", 1.9
	prompt, err := mossTestDefinition(t).buildPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	size := prompt["126"].(map[string]any)["inputs"].(map[string]any)
	width, height := size["width"].(int), size["height"].(int)
	if width%16 != 0 || height%16 != 0 || width >= height || width*height > 4*1024*1024 {
		t.Fatalf("adaptive dimensions = %dx%d", width, height)
	}
}

func TestMossDiscoveryReplayAndForm(t *testing.T) {
	info := map[string]comfyNodeInfo{
		"UNETLoader": comfyNodeInfoFromJSON(t, `{"input":{"required":{"unet_name":[["MOSSImage/pornmasterMoss_v1.safetensors"]]}}}`),
		"CLIPLoader": comfyNodeInfoFromJSON(t, `{"input":{"required":{"clip_name":[["MOSSImage/qwen3.5_9b_int8_convrot.safetensors"]],"type":[["moss_image","qwen_image"]]}}}`),
		"VAELoader":  comfyNodeInfoFromJSON(t, `{"input":{"required":{"vae_name":[["flux2-vae.safetensors"]]}}}`),
		"LoraLoader": comfyNodeInfoFromJSON(t, `{"input":{"required":{"lora_name":[["MOSSImage/style.safetensors","Krea2/style.safetensors"]]}}}`),
	}
	for _, class := range mossWorkflowManifest().RequiredClasses {
		if _, ok := info[class]; !ok {
			info[class] = comfyNodeInfo{}
		}
	}
	catalog := buildGenerationModelCatalog(info)
	model, ok := catalog.byID[generationModelID(modelFamilyMoss, mossTestInput().ModelName)]
	if !ok || !model.Available || model.SupportsImage {
		t.Fatalf("MOSS discovery = %#v", model)
	}
	preset, ok := findGenerationPreset(buildGenerationPresets(catalog), mossPresetID, "text-to-image")
	if !ok || !preset.Available || preset.RequiresImage || preset.AllowsImages {
		t.Fatalf("MOSS preset = %#v", preset)
	}
	if !generationLoraAllowed(catalog.MossLoraGroups, "MOSSImage/style.safetensors") || generationLoraAllowed(catalog.MossLoraGroups, "Krea2/style.safetensors") {
		t.Fatal("LoRA family isolation failed")
	}
	definition, family, err := generationJobDefinition(domain.GenerationJob{TemplateID: "text-to-image", WorkflowID: mossPresetID})
	if err != nil || definition.ID != "text-to-image-moss" || family != modelFamilyMoss {
		t.Fatalf("replay = %s/%s/%v", definition.ID, family, err)
	}
	if !allowedGenerationRecipeField("moss_seedvr2") || !allowedGenerationRecipeField("moss_upscale_resolution") || allowedGenerationRecipeField("moss_unknown") {
		t.Fatal("MOSS parameter persistence is not scoped")
	}
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var html bytes.Buffer
	if err := templates.ExecuteTemplate(&html, "studio_moss_loras", map[string]any{"MossLoraSlots": []int{1, 2}, "MossLoraGroups": catalog.MossLoraGroups}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.String(), `data-lora-slots="moss"`) || strings.Contains(html.String(), `Krea2/style`) {
		t.Fatal("MOSS LoRA controls use the wrong catalog")
	}
}

func TestMossAgainstInstalledSchema(t *testing.T) {
	file := os.Getenv("MOSS_OBJECT_INFO")
	if file == "" {
		t.Skip("set MOSS_OBJECT_INFO to a live schema snapshot; no GPU work submitted")
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]comfyNodeInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	catalog := buildGenerationModelCatalog(info)
	checked := 0
	for _, result := range buildWorkflowCompatibilityResults(catalog) {
		if result.Family != "MOSS Image" {
			continue
		}
		checked++
		if result.Status != workflowCompatibilityOK {
			t.Errorf("%s: %s", result.Scenario, result.Description)
			for _, issue := range result.Issues {
				t.Error(issue.Message)
			}
		}
	}
	if checked < 2 {
		t.Fatal("installed MOSS model or required companions not discovered")
	}
}

func TestMossUpdateInstalledWorkflowMatrix(t *testing.T) {
	file := os.Getenv("MOSS_OBJECT_INFO")
	if file == "" {
		t.Skip("set MOSS_OBJECT_INFO for read-only post-update compatibility validation")
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]comfyNodeInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, result := range buildWorkflowCompatibilityResults(buildGenerationModelCatalog(info)) {
		if result.Status == workflowCompatibilityUnavailable {
			continue
		}
		if strings.HasPrefix(result.ID, "minimax-turbo:") && exactModel(comfyChoiceStrings(info, "MiniMaxH3TurboLoRA", "lora_name"), miniMaxH3TurboLoraName) == "" {
			t.Log("optional MiniMax Turbo LoRA file is not installed; its scenario is not an installed workflow")
			continue
		}
		checked++
		if result.Status != workflowCompatibilityOK {
			for _, issue := range result.Issues {
				t.Errorf("%s / %s: %s", result.Family, result.Scenario, issue.Message)
			}
		}
	}
	t.Logf("validated %d installed workflow scenarios without GPU submission", checked)
}
