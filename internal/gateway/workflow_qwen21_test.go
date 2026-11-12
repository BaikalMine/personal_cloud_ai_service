package gateway

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestQwen21EditUsesFirstImageLatentAndAllReferences(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-qwen21")
	if !ok {
		t.Fatal("Qwen edit workflow is missing")
	}
	input := generationForm{
		TemplateID: "image-to-image", PresetID: "qwen-image21-edit",
		ModelName: "QwenImage2.1/model.safetensors", TextEncoder: "QwenImage2.1/encoder.safetensors", VAE: "QwenImage2.1/vae.safetensors",
		InputImage: "first.png", ReferenceImages: [3]string{"second.png", "", "fourth.png"},
		QwenExtraImages: [6]string{"fifth.png", "", "", "", "", "tenth.png"},
		Positive:        "Change <image1> using <image2> and <image10>", Width: 1024, Height: 1024,
		Steps: 25, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 1,
		PreserveOriginalSize: true, EditUseCustomSize: true,
	}
	if input.imageCount() != 5 || len(input.images()) != 5 {
		t.Fatalf("input image count = %d, want 5", input.imageCount())
	}
	prompt, err := definition.buildPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	encode := prompt["4"].(map[string]any)["inputs"].(map[string]any)
	for number, wanted := range map[int]string{1: "first.png", 2: "second.png", 4: "fourth.png", 5: "fifth.png", 10: "tenth.png"} {
		link, ok := encode["images.image_"+strconv.Itoa(number)].([]any)
		if !ok {
			t.Fatalf("image %d link missing", number)
		}
		node := prompt[link[0].(string)].(map[string]any)
		if got := node["inputs"].(map[string]any)["image"]; got != wanted {
			t.Fatalf("image %d = %v, want %s", number, got, wanted)
		}
	}
	if _, ok := prompt["5"]; ok {
		t.Fatal("preserve source size must not add an empty latent")
	}
	sampler := prompt["6"].(map[string]any)["inputs"].(map[string]any)
	if sampler["latent_image"].([]any)[0] != "4" {
		t.Fatal("edit sampler must use Qwen encoder latent")
	}
	input.PreserveOriginalSize = false
	prompt, err = definition.buildPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	if prompt["6"].(map[string]any)["inputs"].(map[string]any)["latent_image"].([]any)[0] != "5" {
		t.Fatal("custom size must use explicit canvas")
	}
}

func TestQwen21EditCapsLargeSourceBeforeComfyUI(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-qwen21")
	if !ok {
		t.Fatal("Qwen edit workflow is missing")
	}
	input := generationForm{
		TemplateID: "image-to-image", PresetID: "qwen-image21-edit",
		ModelName: "QwenImage2.1/model.safetensors", TextEncoder: "QwenImage2.1/encoder.safetensors", VAE: "QwenImage2.1/vae.safetensors",
		InputImage: "source.png", Positive: "Edit the image", Width: 3072, Height: 4096,
		OutputMegapixels: 12, DimensionMultiple: 32, Steps: 25, CFG: 1, Denoise: 1,
		Sampler: "euler", Scheduler: "simple", Seed: 1, PreserveOriginalSize: true,
	}
	if err := definition.normalizeAndValidate(&input); err != nil {
		t.Fatal(err)
	}
	if input.Width != 896 || input.Height != 1184 || input.QwenResolution != 1024 {
		t.Fatalf("unsafe Qwen source size: %dx%d, reference resolution %d", input.Width, input.Height, input.QwenResolution)
	}
	prompt, err := definition.buildPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := prompt["4"].(map[string]any)["inputs"].(map[string]any)["resolution"]; got != 1024 {
		t.Fatalf("reference resolution = %v, want 1024", got)
	}
	input.Width, input.Height = 3072, 4096
	input.OutputMegapixels = 1.5
	input.QwenResolution = 0
	if err := definition.normalizeAndValidate(&input); err != nil {
		t.Fatal(err)
	}
	if input.Width != 1088 || input.Height != 1440 || input.QwenResolution != 1248 {
		t.Fatalf("high-detail Qwen size: %dx%d, reference resolution %d", input.Width, input.Height, input.QwenResolution)
	}
	input.PreserveOriginalSize = false
	input.EditUseCustomSize = true
	input.QwenResolution = 0
	input.Width, input.Height = 3072, 4096
	if err := definition.normalizeAndValidate(&input); err == nil {
		t.Fatal("unsafe custom Qwen size was accepted")
	}
}

func TestQwen21EditFormRetainsTenthImage(t *testing.T) {
	form := url.Values{
		"template_id": {"image-to-image"}, "generation_workflow": {"qwen-image21-edit"},
		"positive_prompt": {"Edit <image1> with <image10>"}, "input_image": {"first.png"},
		"input_image_10": {"tenth.png"}, "image_role_10": {"style"},
	}
	request := httptest.NewRequest("POST", "/generate", nil)
	request.Form = form
	input, err := parseGenerationForm(request)
	if err != nil {
		t.Fatal(err)
	}
	if input.QwenExtraImages[5] != "tenth.png" || input.imageCount() != 2 {
		t.Fatalf("tenth reference lost: %#v", input.QwenExtraImages)
	}
	if refs := input.references(); len(refs) != 2 || refs[1].Number != 10 || refs[1].Role != "style" {
		t.Fatalf("reference metadata lost: %#v", refs)
	}
}

func TestQwen21AssistantAcceptsTenReferences(t *testing.T) {
	request := httptest.NewRequest("POST", "/generate/prompt-assistant", nil)
	request.Form = url.Values{"generation_workflow": {"qwen-image21-edit"}}
	if got := promptAssistantReferenceLimit(request, "image-to-image"); got != 10 {
		t.Fatalf("Qwen assistant reference limit = %d, want 10", got)
	}
	request.Form.Set("generation_workflow", "photoflow-flux2-edit")
	if got := promptAssistantReferenceLimit(request, "image-to-image"); got != 4 {
		t.Fatalf("Flux assistant reference limit = %d, want 4", got)
	}
}
