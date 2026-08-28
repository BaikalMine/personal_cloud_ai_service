package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGenerateTemplateRenders(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = templates.ExecuteTemplate(&output, "generate", map[string]any{
		"Title": "Быстрая генерация", "CSRF": "csrf", "AssetVersion": "asset",
		"Workflows":         []workflowView{{ID: "text-to-image", Name: "Текст в изображение", Description: "Описание"}},
		"GenerationPresets": []generationPreset{{ID: "photoflow-krea2-edit", TemplateID: "image-to-image", Name: "Krea 2: фото и промт", Family: modelFamilyKrea2, Available: true, ModelID: "krea2:test", ModelCount: 2, RequiresImage: true, MaxInputImages: 2}},
		"QuickModels":       []generationModel{{ID: "krea2:test", Name: "krea.safetensors", DisplayName: "Krea2 / krea", Family: modelFamilyKrea2, Available: true, DefaultSteps: 8, DefaultCFG: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "data-comfy-generation") || !strings.Contains(output.String(), "/static/generate.js") ||
		!strings.Contains(output.String(), "Krea 2: фото и промт") || !strings.Contains(output.String(), "Диффузионная модель") ||
		!strings.Contains(output.String(), "generation-editor-profile") || !strings.Contains(output.String(), "prompt-assistant-template") ||
		!strings.Contains(output.String(), "Перенос внешности и редактирование") || !strings.Contains(output.String(), "prompt-assistant-think") ||
		!strings.Contains(output.String(), "Максимум деталей · 4,7 Мп") {
		t.Fatal("generation template did not render the wizard")
	}
}

func TestGenerationDownloadDisposition(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/generate/output?download=1", nil)
	response := httptest.NewRecorder()
	setGenerationDownloadDisposition(response, request, `folder\\portrait.png`)
	if got := response.Header().Get("Content-Disposition"); !strings.Contains(got, `attachment; filename=portrait.png`) {
		t.Fatalf("Content-Disposition = %q", got)
	}

	previewRequest := httptest.NewRequest(http.MethodGet, "/generate/output", nil)
	previewResponse := httptest.NewRecorder()
	setGenerationDownloadDisposition(previewResponse, previewRequest, "portrait.png")
	if got := previewResponse.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("preview Content-Disposition = %q, want empty", got)
	}
}

func TestParseGenerationFloatAcceptsRussianDecimalSeparator(t *testing.T) {
	for raw, want := range map[string]float64{"0,22": 0.22, "1.5": 1.5, " 4,7 ": 4.7} {
		got, err := parseGenerationFloat(raw)
		if err != nil || got != want {
			t.Fatalf("parseGenerationFloat(%q) = %v, %v; want %v, nil", raw, got, err, want)
		}
	}
}

func TestParseGenerationFormAcceptsCommasInPhotoAndPromptControls(t *testing.T) {
	form := url.Values{
		"width":                 {"1024"},
		"height":                {"1024"},
		"steps":                 {"20"},
		"cfg":                   {"1,5"},
		"denoise":               {"0,75"},
		"reference_boost":       {"4,25"},
		"upscale_factor":        {"1,5"},
		"upscale_denoise":       {"0,15"},
		"skin_coolness":         {"0,22"},
		"skin_brightness":       {"0,12"},
		"skin_texture_preserve": {"0,88"},
		"post_denoise_edge":     {"0,05"},
		"lora_model_strength_1": {"0,8"},
		"lora_clip_strength_1":  {"1,0"},
		"output_megapixels":     {"1,9"},
		"source_megapixels":     {"1,5"},
		"flux_guidance":         {"3,5"},
		"flux_active_scale":     {"0,8"},
		"flux_token_whiten":     {"0,25"},
		"flux_norm_equalize":    {"0,5"},
		"color_strength":        {"0,6"},
		"template_id":           {"image-to-image"},
		"generation_workflow":   {"photoflow-krea2-edit"},
		"positive_prompt":       {"portrait"},
		"input_image":           {"gateway/input.png"},
		"model":                 {"krea2:test"},
		"skin_preset":           {"Natural"},
		"lut_name":              {"LC_Crushed_Blacks.cube"},
		"upscale_sampler":       {"deis"},
		"upscale_scheduler":     {"simple"},
		"sampler":               {"euler"},
		"scheduler":             {"simple"},
		"seed":                  {"42"},
	}
	request := httptest.NewRequest(http.MethodPost, "/generate/run", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	input, err := parseGenerationForm(request)
	if err != nil {
		t.Fatalf("parseGenerationForm() error = %v", err)
	}
	if input.CFG != 1.5 || input.Denoise != 0.75 || input.ReferenceBoost != 4.25 || input.UpscaleFactor != 1.5 || input.SkinCoolness != 0.22 || input.LoraModel[0] != 0.8 || input.FluxGuidance != 3.5 || input.FluxActiveScale != 0.8 || input.FluxTokenWhiten != 0.25 || input.FluxNormEqualize != 0.5 || input.ColorStrength != 0.6 {
		t.Fatalf("comma controls were not preserved: %#v", input)
	}
}

func TestWorkflowDefinitionsBuildTypedPrompt(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image")
	if !ok {
		t.Fatal("image-to-image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		TemplateID: "image-to-image", ModelName: "model.safetensors", ModelFamily: modelFamilyCheckpoint, InputImage: "gateway/gateway-0123456789abcdef01234567/input.png",
		Positive: "portrait", Negative: "blurry", Width: 1024, Height: 768, Steps: 20, CFG: 7, Denoise: 0.65, Seed: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	node, ok := prompt["3"].(map[string]any)
	if !ok {
		t.Fatalf("sampler node has type %T", prompt["3"])
	}
	inputs := node["inputs"].(map[string]any)
	if got, want := inputs["seed"], int64(42); got != want {
		t.Fatalf("seed = %v, want %v", got, want)
	}
	if got, want := inputs["denoise"], 0.65; got != want {
		t.Fatalf("denoise = %v, want %v", got, want)
	}
	imageNode := prompt["10"].(map[string]any)
	imageInputs := imageNode["inputs"].(map[string]any)
	if got, want := imageInputs["image"], "gateway/gateway-0123456789abcdef01234567/input.png"; got != want {
		t.Fatalf("image = %v, want %v", got, want)
	}
	if _, err := json.Marshal(prompt); err != nil {
		t.Fatal(err)
	}
}

func TestKrea2WorkflowUsesDiffusionDependencies(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "text-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "qwen3vl_4b_fp8_scaled.safetensors", VAE: "qwen_image_vae.safetensors",
		Positive: "portrait", Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Seed: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	loader := prompt["1"].(map[string]any)
	if loader["class_type"] != "UNETLoader" || loader["inputs"].(map[string]any)["unet_name"] != "Krea2/model.safetensors" {
		t.Fatalf("unexpected diffusion loader: %#v", loader)
	}
	clip := prompt["2"].(map[string]any)["inputs"].(map[string]any)
	if clip["type"] != "krea2" || clip["clip_name"] != "qwen3vl_4b_fp8_scaled.safetensors" {
		t.Fatalf("unexpected Krea 2 text encoder: %#v", clip)
	}
}

func TestKrea2WorkflowKeepsFullHDPortraitDimensions(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "text-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", TextEncoder: "encoder.safetensors",
		VAE: "qwen_image_vae.safetensors", Lora: "lenovo_krea2.safetensors", LoraStrength: 0.8,
		Positive: "portrait", Width: 1080, Height: 1920, Steps: 8, CFG: 1,
		Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		BaseMegapixels: 1.5, UpscaleSteps: 8, UpscaleDenoise: 0.22,
		DetailSteps: 3, DetailDenoise: 0.035,
	})
	if err != nil {
		t.Fatal(err)
	}
	upscale := prompt["11"].(map[string]any)["inputs"].(map[string]any)
	if upscale["width"] != 1080 || upscale["height"] != 1920 {
		t.Fatalf("final dimensions = %vx%v, want 1080x1920", upscale["width"], upscale["height"])
	}
	upscaleSampler := prompt["13"].(map[string]any)["inputs"].(map[string]any)
	if upscaleSampler["steps"] != 8 || upscaleSampler["denoise"] != 0.22 {
		t.Fatalf("unexpected upscale pass: %#v", upscaleSampler)
	}
	detailSampler := prompt["14"].(map[string]any)["inputs"].(map[string]any)
	if detailSampler["steps"] != 3 || detailSampler["denoise"] != 0.035 {
		t.Fatalf("unexpected detail pass: %#v", detailSampler)
	}
}

func TestGenerationDimensionsFollowAspectAndMegapixels(t *testing.T) {
	width, height, err := generationDimensions("3:4", 1.9, 16, 0)
	if err != nil {
		t.Fatal(err)
	}
	if width%16 != 0 || height%16 != 0 || math.Abs(float64(width)/float64(height)-0.75) > 0.02 {
		t.Fatalf("dimensions = %dx%d, want a 3:4 result aligned to 16 pixels", width, height)
	}
	megapixels := float64(width*height) / (1024 * 1024)
	if math.Abs(megapixels-1.9) > 0.08 {
		t.Fatalf("megapixels = %.2f, want approximately 1.9", megapixels)
	}
}

func TestKrea2WorkflowAppliesLoraChainAndColorTransfer(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "text-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", TextEncoder: "encoder.safetensors", VAE: "vae.safetensors", Lora: "fallback.safetensors",
		LorasConfigured: true, LoraNames: [maxGenerationLoraSlots]string{"lenovo_krea2.safetensors", "Krea2/detailer.safetensors", "", "", "Krea2/final-style.safetensors"},
		LoraModel: [maxGenerationLoraSlots]float64{0.8, 2, 0, 0, 0.45}, LoraClip: [maxGenerationLoraSlots]float64{1, 0.7, 0, 0, 0.55},
		Positive: "portrait", AspectRatio: "3:4", OutputMegapixels: 1.9, DimensionMultiple: 16,
		Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Seed: 42,
		ColorTransfer: true, ColorMethod: "mkl_lab", ColorMode: "uniform", ColorStrength: 0.75,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondLora := prompt["17"].(map[string]any)["inputs"].(map[string]any)
	if secondLora["lora_name"] != "Krea2/detailer.safetensors" || secondLora["strength_model"] != 2.0 || secondLora["strength_clip"] != 0.7 {
		t.Fatalf("unexpected second LoRA: %#v", secondLora)
	}
	fifthLora := prompt["gateway_lora_5"].(map[string]any)["inputs"].(map[string]any)
	if fifthLora["lora_name"] != "Krea2/final-style.safetensors" || fifthLora["model"].([]any)[0] != "19" {
		t.Fatalf("unexpected fifth LoRA: %#v", fifthLora)
	}
	if got := prompt["5"].(map[string]any)["inputs"].(map[string]any)["model"].([]any)[0]; got != "gateway_lora_5" {
		t.Fatalf("Krea2 model is not connected to the fifth LoRA: %v", got)
	}
	color := prompt["20"].(map[string]any)["inputs"].(map[string]any)
	if color["method"] != "mkl_lab" || color["source_stats"] != "uniform" || color["strength"] != 0.75 {
		t.Fatalf("unexpected color transfer: %#v", color)
	}
}

func TestFlux2ImageWorkflowUsesReferenceLatent(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", ModelFamily: modelFamilyFlux2,
		TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/gateway-0123456789abcdef01234567/input.png",
		Positive:   "replace the background", Negative: "artifacts", Width: 1024, Height: 768,
		Steps: 20, CFG: 5, Denoise: 0.7, Seed: 42,
		LUTName: "street.cube", LUTStrength: 0.28, LUTEnabled: true,
		LoraNames: [maxGenerationLoraSlots]string{"Flux2\\klein_snofs_v1_4.safetensors", "", "", "", "", "", "", "", "", "Flux2\\final-style.safetensors"}, LoraModel: [maxGenerationLoraSlots]float64{0.75, 0, 0, 0, 0, 0, 0, 0, 0, 0.4},
	})
	if err != nil {
		t.Fatal(err)
	}
	loadImage := prompt["1075"].(map[string]any)["inputs"].(map[string]any)
	if loadImage["image"] != "gateway/gateway-0123456789abcdef01234567/input.png" {
		t.Fatalf("unexpected input image: %#v", loadImage)
	}
	reference := prompt["1398"].(map[string]any)
	if reference["class_type"] != "LCReferenceLatent" {
		t.Fatalf("reference conditioning is missing: %#v", reference)
	}
	scaler := prompt["1061"].(map[string]any)["inputs"].(map[string]any)
	if got, want := scaler["megapixels"], float64(1); got != want {
		t.Fatalf("megapixels = %v, want %v", got, want)
	}
	aspect := prompt["1305"].(map[string]any)["inputs"].(map[string]any)
	if got, want := aspect["max_resolution"], 2160; got != want {
		t.Fatalf("max resolution = %v, want %v", got, want)
	}
	sampler := prompt["1328"].(map[string]any)["inputs"].(map[string]any)
	if got, want := sampler["scheduler"], "normal"; got != want {
		t.Fatalf("Flux2 scheduler = %v, want %q", got, want)
	}
	lut := prompt["1430"].(map[string]any)["inputs"].(map[string]any)
	if lut["image"].([]any)[0] != "1362" || lut["lut_name"] != "street.cube" || lut["strength"] != 0.28 {
		t.Fatalf("Flux2 LUT is not connected to final image: %#v", lut)
	}
	lora := prompt["539"].(map[string]any)["inputs"].(map[string]any)["lora_1"].(map[string]any)
	if got, want := lora["lora"], "Flux2\\klein_snofs_v1_4.safetensors"; got != want {
		t.Fatalf("Flux2 LoRA = %v, want %q", got, want)
	}
	if got, want := lora["strength"], 0.75; got != want {
		t.Fatalf("Flux2 LoRA strength = %v, want %v", got, want)
	}
	tenthLora := prompt["539"].(map[string]any)["inputs"].(map[string]any)["lora_10"].(map[string]any)
	if got, want := tenthLora["lora"], "Flux2\\final-style.safetensors"; got != want {
		t.Fatalf("Flux2 tenth LoRA = %v, want %q", got, want)
	}
	encoded, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "{{") {
		t.Fatalf("Flux2 prompt contains an unresolved workflow placeholder: %s", encoded)
	}
	for _, nodeID := range []string{"1076", "1065", "1066", "1077", "1063", "1072", "1316", "1317", "1318"} {
		if _, exists := prompt[nodeID]; exists {
			t.Fatalf("unused optional Flux reference node %s was not removed", nodeID)
		}
	}
}

func TestFlux2ImageWorkflowPreservesOriginalResolution(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", ModelFamily: modelFamilyFlux2,
		TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input.png", Positive: "edit the portrait", Width: 4032, Height: 3024,
		OutputMegapixels: 11.63, DimensionMultiple: 16, MaxLongestSide: 4096,
		Steps: 25, CFG: 1, Denoise: 0.9, Sampler: "euler", Scheduler: "normal", Seed: 42,
		EditUseCustomSize: true, PreserveOriginalSize: true, SourceMegapixels: 11.63,
	})
	if err != nil {
		t.Fatal(err)
	}
	inputScale := prompt["1061"].(map[string]any)["inputs"].(map[string]any)
	if got, want := inputScale["megapixels"], 11.63; got != want {
		t.Fatalf("Flux2 input megapixels = %v, want %v", got, want)
	}
	frame := prompt["1305"].(map[string]any)["inputs"].(map[string]any)
	if frame["custom_width"] != 4032 || frame["custom_height"] != 3024 || frame["resolution_source"] != true {
		t.Fatalf("Flux2 original frame is not preserved: %#v", frame)
	}
}

func TestKrea2ImageWorkflowPreservesOriginalResolution(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "encoder.safetensors", VAE: "vae.safetensors", IdentityLora: "identity.safetensors",
		InputImage: "gateway/input.png", Positive: "change the light", Width: 1920, Height: 1080,
		OutputMegapixels: 1.98, DimensionMultiple: 16, MaxLongestSide: 4096,
		Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		EditUseCustomSize: true, PreserveOriginalSize: true, ReferenceBoost: 4, GroundingPixels: 768,
		UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "deis", UpscaleScheduler: "simple",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := prompt["5"].(map[string]any)["inputs"].(map[string]any)
	if frame["custom_width"] != 1920 || frame["custom_height"] != 1080 || frame["resolution_source"] != true {
		t.Fatalf("Krea2 original frame is not preserved: %#v", frame)
	}
	if got, want := prompt["14"].(map[string]any)["inputs"].(map[string]any)["upscale_by"], 1.0; got != want {
		t.Fatalf("Krea2 upscale factor = %v, want %v", got, want)
	}
}

func TestFlux2ImageWorkflowNormalizesLegacySchedulerAndBindsMaxResolution(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input.png", Positive: "edit", Width: 1080, Height: 1352, Steps: 25, CFG: 1, Denoise: 0.9,
		Sampler: "euler", Scheduler: "flux2", Seed: -1, SourceMegapixels: 1, MaxLongestSide: 2160,
	})
	if err != nil {
		t.Fatal(err)
	}
	aspect := prompt["1305"].(map[string]any)["inputs"].(map[string]any)
	if got, want := aspect["max_resolution"], 2160; got != want {
		t.Fatalf("max resolution = %v, want %v", got, want)
	}
	sampler := prompt["1328"].(map[string]any)["inputs"].(map[string]any)
	if got, want := sampler["scheduler"], "normal"; got != want {
		t.Fatalf("legacy Flux2 scheduler = %v, want %q", got, want)
	}
	seed := prompt["404"].(map[string]any)["inputs"].(map[string]any)["seed"].(int64)
	if seed < 0 || seed > 1<<50 {
		t.Fatalf("Flux2 seed %d is outside rgthree range", seed)
	}
}

func TestFlux2ImageWorkflowBindsDedicatedConditioningControls(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input.png", Positive: "edit", Width: 1024, Height: 1024, Steps: 31, CFG: 1.4, Denoise: 0.82,
		Sampler: "euler", Scheduler: "normal", Seed: 42, SourceMegapixels: 1.5, FluxGuidance: 3.2, FluxDetailerSteps: 12,
		FluxActiveScale: 1.25, FluxTokenWhiten: 0.4, FluxNormEqualize: 0.2,
	})
	if err != nil {
		t.Fatal(err)
	}
	guidance := prompt["286"].(map[string]any)["inputs"].(map[string]any)
	if got, want := guidance["Xi"], 3.2; got != want {
		t.Fatalf("Flux guidance = %v, want %v", got, want)
	}
	enhancer := prompt["851"].(map[string]any)["inputs"].(map[string]any)
	if got, want := enhancer["active_scale"], 1.25; got != want {
		t.Fatalf("Flux active scale = %v, want %v", got, want)
	}
	if got, want := enhancer["per_token_whiten"], 0.4; got != want {
		t.Fatalf("Flux token whiten = %v, want %v", got, want)
	}
	sampler := prompt["1328"].(map[string]any)["inputs"].(map[string]any)
	if got, want := sampler["detailer_steps"], 12; got != want {
		t.Fatalf("Flux detailer steps = %v, want %v", got, want)
	}
}

func TestFlux2ImageWorkflowAppliesSelectedUpscaleBranches(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	base := generationForm{
		ModelName: "Flux2/model.safetensors", TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input.png", Positive: "edit", Width: 1024, Height: 1024, Steps: 25, CFG: 1, Denoise: 0.9,
		Sampler: "euler", Scheduler: "normal", Seed: 42, SourceMegapixels: 1,
	}
	for _, test := range []struct {
		mode       string
		wantOutput string
		wantFirst  string
		wantSecond bool
	}{
		{mode: "none", wantOutput: "1362"},
		{mode: "ultimate", wantOutput: "gateway_flux_ultimate", wantFirst: "1362"},
		{mode: "seedvr2", wantOutput: "gateway_flux_seedvr2", wantFirst: "1362", wantSecond: true},
		{mode: "both", wantOutput: "gateway_flux_seedvr2", wantFirst: "gateway_flux_ultimate", wantSecond: true},
	} {
		t.Run(test.mode, func(t *testing.T) {
			input := base
			input.FluxUpscaleMode = test.mode
			prompt, err := definition.buildPrompt(input)
			if err != nil {
				t.Fatal(err)
			}
			saved := prompt["1341"].(map[string]any)["inputs"].(map[string]any)["images"].([]any)
			if got := saved[0]; got != test.wantOutput {
				t.Fatalf("saved image = %v, want %q", got, test.wantOutput)
			}
			ultimate, hasUltimate := prompt["gateway_flux_ultimate"]
			if (test.mode == "ultimate" || test.mode == "both") != hasUltimate {
				t.Fatalf("Ultimate branch present = %t for mode %q", hasUltimate, test.mode)
			}
			if hasUltimate {
				if got := ultimate.(map[string]any)["inputs"].(map[string]any)["image"].([]any)[0]; got != "1362" {
					t.Fatalf("Ultimate input = %v, want core Flux2 output", got)
				}
			}
			seed, hasSeed := prompt["gateway_flux_seedvr2"]
			if test.wantSecond != hasSeed {
				t.Fatalf("SeedVR2 branch present = %t for mode %q", hasSeed, test.mode)
			}
			if hasSeed {
				if got := seed.(map[string]any)["inputs"].(map[string]any)["image"].([]any)[0]; got != test.wantFirst {
					t.Fatalf("SeedVR2 input = %v, want %q", got, test.wantFirst)
				}
				if got := seed.(map[string]any)["inputs"].(map[string]any)["resolution"].([]any)[0]; got != "gateway_flux_seedvr2_resolution" {
					t.Fatalf("SeedVR2 resolution is not derived from the current image: %v", got)
				}
			}
		})
	}
}

func TestFlux2ImageWorkflowRejectsUnknownUpscaleMode(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	_, err = definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", TextEncoder: "encoder.safetensors", VAE: "vae.safetensors", InputImage: "gateway/input.png",
		Positive: "edit", Width: 1024, Height: 1024, Steps: 25, CFG: 1, Denoise: 0.9, Sampler: "euler", Scheduler: "normal", Seed: 42,
		FluxUpscaleMode: "everything",
	})
	if err == nil || !strings.Contains(err.Error(), "режим апскейла Flux2") {
		t.Fatalf("error = %v, want Flux2 upscale mode validation error", err)
	}
}

func TestRandomSeedFitsRGThreeRange(t *testing.T) {
	for range 32 {
		seed, err := randomSeed()
		if err != nil {
			t.Fatal(err)
		}
		if seed < 0 || seed > 1<<50 {
			t.Fatalf("seed %d is outside rgthree range", seed)
		}
	}
}

func TestFlux2ImageWorkflowAcceptsFourImages(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", ModelFamily: modelFamilyFlux2,
		TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input-1.png", ReferenceImages: [3]string{"gateway/input-2.png", "gateway/input-3.png", "gateway/input-4.png"},
		Positive: "combine the reference photos", Width: 1024, Height: 768, Steps: 20, CFG: 5, Denoise: 0.7, Seed: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	for nodeID, image := range map[string]string{"1076": "gateway/input-2.png", "1077": "gateway/input-3.png", "1316": "gateway/input-4.png"} {
		inputs := prompt[nodeID].(map[string]any)["inputs"].(map[string]any)
		if got := inputs["image"]; got != image {
			t.Fatalf("%s image = %v, want %q", nodeID, got, image)
		}
	}
	referenceInputs := prompt["1398"].(map[string]any)["inputs"].(map[string]any)
	for _, name := range []string{"latent_1", "latent_2", "latent_3", "latent_4"} {
		if _, ok := referenceInputs[name]; !ok {
			t.Fatalf("reference latent %s is missing: %#v", name, referenceInputs)
		}
	}
}

func TestKrea2ImageWorkflowUsesIdentityEditAndQualityUpscale(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		TemplateID: "image-to-image", ModelName: "Krea2/gonzalomoKrea2_v40.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "qwen3VLInstruct4bHeretic_v10.safetensors", VAE: "qwen_image_vae.safetensors",
		IdentityLora: "krea2_identity_edit_v1_1.safetensors", InputImage: "gateway/input.png", Positive: "change the jacket to red",
		Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		ReferenceBoost: 4, GroundingPixels: 768, UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "euler_ancestral",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := prompt["7"].(map[string]any)["inputs"].(map[string]any)
	if got, want := identity["lora_name"], "krea2_identity_edit_v1_1.safetensors"; got != want {
		t.Fatalf("identity LoRA = %v, want %v", got, want)
	}
	patch := prompt["8"].(map[string]any)["inputs"].(map[string]any)
	if got, want := patch["ref_boost"], 4.0; got != want {
		t.Fatalf("reference boost = %v, want %v", got, want)
	}
	upscale := prompt["14"].(map[string]any)["inputs"].(map[string]any)
	if got, want := upscale["upscale_by"], 1.5; got != want {
		t.Fatalf("upscale factor = %v, want %v", got, want)
	}
	if prompt["19"].(map[string]any)["class_type"] != "SaveImage" {
		t.Fatalf("Krea 2 workflow does not save the final image: %#v", prompt["19"])
	}
	if _, exists := prompt["20"]; exists {
		t.Fatal("optional Krea reference image loader was not removed")
	}
}

func TestKrea2ImageWorkflowBindsDedicatedUpscaleAndPostProcessingControls(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", TextEncoder: "encoder.safetensors", VAE: "vae.safetensors", IdentityLora: "identity.safetensors",
		InputImage: "gateway/input.png", Positive: "edit", Width: 1024, Height: 1024, Steps: 20, CFG: 8, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		ReferenceBoost: 4, GroundingPixels: 768, UpscaleFactor: 1.75, UpscaleSteps: 6, UpscaleCFG: 1.5, UpscaleDenoise: 0.21, UpscaleSampler: "deis", UpscaleScheduler: "normal",
		PostDenoiseBlur: 1.5, PostDenoiseEdge: 0.1, PostDenoiseRadius: 1.2, PostDenoiseStrength: 0.6,
		SkinPreset: "Fresh", SkinStrength: 1.1, SkinCoolness: 0.3, SkinBrightness: 0.15, SkinRosy: 0.1, SkinEvenness: 0.2, SkinShadowLift: 0.2, SkinSmooth: 0.1, SkinTexturePreserve: 0.8, SkinSaturation: -0.05, SkinHighlightProtect: 0.7, SkinMaskSensitivity: 0.6, SkinMaskFeather: 0.4,
		AdjustHue: 0.1, AdjustSaturation: 0.2, AdjustBrightness: -0.1, AdjustContrast: 0.15, AdjustSharpness: 0.25, LUTName: "street.cube", LUTStrength: 0.4,
	})
	if err != nil {
		t.Fatal(err)
	}
	upscale := prompt["14"].(map[string]any)["inputs"].(map[string]any)
	if got, want := upscale["upscale_by"], 1.75; got != want {
		t.Fatalf("upscale factor = %v, want %v", got, want)
	}
	if got, want := upscale["scheduler"], "normal"; got != want {
		t.Fatalf("upscale scheduler = %v, want %q", got, want)
	}
	denoise := prompt["15"].(map[string]any)["inputs"].(map[string]any)
	if got, want := denoise["strength"], 0.6; got != want {
		t.Fatalf("post denoise strength = %v, want %v", got, want)
	}
	skin := prompt["16"].(map[string]any)["inputs"].(map[string]any)
	if got, want := skin["preset"], "Fresh"; got != want {
		t.Fatalf("skin preset = %v, want %q", got, want)
	}
	adjust := prompt["17"].(map[string]any)["inputs"].(map[string]any)
	if got, want := adjust["sharpness"], 0.25; got != want {
		t.Fatalf("sharpness = %v, want %v", got, want)
	}
	lut := prompt["18"].(map[string]any)["inputs"].(map[string]any)
	if got, want := lut["lut_name"], "street.cube"; got != want {
		t.Fatalf("LUT = %v, want %q", got, want)
	}
}

func TestNormalizeLUTMakesColorGradingOptional(t *testing.T) {
	disabled := generationForm{}
	normalizeLUT(&disabled)
	if disabled.LUTEnabled || disabled.LUTStrength != 0 || disabled.LUTName != "LC_Crushed_Blacks.cube" {
		t.Fatalf("disabled LUT = %#v", disabled)
	}
	legacy := generationForm{LUTName: "street.cube", LUTStrength: 0.4}
	normalizeLUT(&legacy)
	if !legacy.LUTEnabled || legacy.LUTStrength != 0.4 {
		t.Fatalf("legacy LUT = %#v", legacy)
	}
}

func TestKreaEditDefaultsDoNotEnableLUT(t *testing.T) {
	input := generationForm{}
	normalizeKreaEditDefaults(&input)
	normalizeLUT(&input)
	if input.LUTEnabled || input.LUTStrength != 0 || input.LUTName != "LC_Crushed_Blacks.cube" {
		t.Fatalf("Krea2 defaults enabled LUT: %#v", input)
	}
}

func TestKrea2ImageWorkflowAcceptsSecondImage(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		TemplateID: "image-to-image", ModelName: "Krea2/gonzalomoKrea2_v40.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "qwen3VLInstruct4bHeretic_v10.safetensors", VAE: "qwen_image_vae.safetensors", IdentityLora: "krea2_identity_edit_v1_1.safetensors",
		InputImage: "gateway/input-1.png", ReferenceImages: [3]string{"gateway/input-2.png"}, Positive: "use the second image as a style reference",
		Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		ReferenceBoost: 4, GroundingPixels: 768, UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "euler_ancestral",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := prompt["20"].(map[string]any)["inputs"].(map[string]any)["image"]; got != "gateway/input-2.png" {
		t.Fatalf("second Krea image = %v", got)
	}
	patchInputs := prompt["8"].(map[string]any)["inputs"].(map[string]any)
	for _, name := range []string{"source_latent_b", "source_image_b"} {
		if _, ok := patchInputs[name]; !ok {
			t.Fatalf("Krea optional input %s is missing: %#v", name, patchInputs)
		}
	}
}

func TestFlux2ImageWorkflowDoesNotValidateKrea2OnlySettings(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", ModelFamily: modelFamilyFlux2,
		TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input.png", Positive: "replace the background", Width: 1024, Height: 768,
		Steps: 20, CFG: 1, Denoise: 0.7, Seed: 42, SourceMegapixels: 1,
		// These are Krea2-specific fields and must not reject or alter Flux2 execution.
		ReferenceBoost: 99, GroundingPixels: 1, UpscaleFactor: 9,
	})
	if err != nil {
		t.Fatalf("Flux2 rejected Krea2-only values: %v", err)
	}
	encoded, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "99") {
		t.Fatalf("Krea2 reference boost leaked into Flux2 prompt: %s", encoded)
	}
}

func TestKrea2ImageWorkflowDoesNotValidateFlux2OnlySettings(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/gonzalomoKrea2_v40.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "qwen3VLInstruct4bHeretic_v10.safetensors", VAE: "qwen_image_vae.safetensors",
		IdentityLora: "krea2_identity_edit_v1_1.safetensors", InputImage: "gateway/input.png", Positive: "change the jacket",
		Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		ReferenceBoost: 4, GroundingPixels: 768, UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "euler_ancestral",
		// Source megapixels are a Flux2-only control and must not affect Krea2.
		SourceMegapixels: 99,
	})
	if err != nil {
		t.Fatalf("Krea2 rejected Flux2-only values: %v", err)
	}
	if got := prompt["8"].(map[string]any)["inputs"].(map[string]any)["ref_boost"]; got != 4.0 {
		t.Fatalf("unexpected Krea2 reference boost: %v", got)
	}
}

func TestImageEditWorkflowsEnforceTheirOwnReferenceLimits(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	krea, _ := findWorkflow(definitions, "image-to-image-krea2")
	_, err = krea.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", InputImage: "gateway/one.png", ReferenceImages: [3]string{"gateway/two.png", "gateway/three.png"},
		Positive: "edit", Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple",
		ReferenceBoost: 4, GroundingPixels: 768, UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "euler_ancestral",
	})
	if err == nil || !strings.Contains(err.Error(), "до двух") {
		t.Fatalf("Krea2 reference limit error = %v", err)
	}

	flux, _ := findWorkflow(definitions, "image-to-image-flux2")
	_, err = flux.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", InputImage: "gateway/one.png", ReferenceImages: [3]string{"gateway/two.png", "gateway/three.png", "gateway/four.png"},
		Positive: "edit", Width: 1024, Height: 1024, Steps: 20, CFG: 1, Denoise: 0.7, SourceMegapixels: 1,
	})
	if err != nil {
		t.Fatalf("Flux2 must accept four images: %v", err)
	}
}

func TestGenerationModelCatalogGroupsDiffusionModels(t *testing.T) {
	var info map[string]comfyNodeInfo
	fixture := `{
		"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["regular.safetensors"]]}}},
		"UNETLoader":{"input":{"required":{"unet_name":[["Krea2/krea2Turbo.safetensors","Flux2/flux2Klein_9b.safetensors"]]}}},
		"CLIPLoader":{"input":{"required":{"clip_name":[["Huihui-Qwen3-VL-4B-Instruct-abliterated-fp8_scaled.safetensors"]]}}},
		"VAELoader":{"input":{"required":{"vae_name":[["qwen_image_vae.safetensors"]]}}},
		"LoraLoader":{"input":{"required":{"lora_name":[["lenovo_krea2.safetensors"]]}}}
	}`
	if err := json.Unmarshal([]byte(fixture), &info); err != nil {
		t.Fatal(err)
	}
	catalog := buildGenerationModelCatalog(info)
	if got, want := catalog.AvailableCount, 2; got != want {
		t.Fatalf("available models = %d, want %d", got, want)
	}
	var krea, flux generationModel
	for _, group := range catalog.Groups {
		for _, model := range group.Models {
			switch model.Family {
			case modelFamilyKrea2:
				krea = model
			case modelFamilyFlux2:
				flux = model
			}
		}
	}
	if !krea.Available || krea.DefaultSteps != 8 || krea.TextEncoder == "" || krea.VAE == "" {
		t.Fatalf("unexpected Krea 2 model: %#v", krea)
	}
	if krea.SupportsImage {
		t.Fatalf("Krea 2 must not be offered for image editing: %#v", krea)
	}
	if flux.Available || !strings.Contains(flux.Reason, "Qwen3 8B") || !strings.Contains(flux.Reason, "Flux 2 VAE") {
		t.Fatalf("unexpected Flux 2 dependency state: %#v", flux)
	}
	if !flux.SupportsImage {
		t.Fatalf("Flux 2 must be offered for image editing when dependencies are installed: %#v", flux)
	}
}

func TestGenerationModelCatalogEnablesInstalledEditWorkflows(t *testing.T) {
	var info map[string]comfyNodeInfo
	fixture := `{
		"UNETLoader":{"input":{"required":{"unet_name":[["Flux.2 Klein 9B/base.safetensors","Krea2/gonzalomoKrea2_v40.safetensors"]]}}},
		"CLIPLoader":{"input":{"required":{"clip_name":[["qwen3VLInstruct4bHeretic_v10.safetensors"]]}}},
		"ClipLoaderGGUF":{"input":{"required":{"clip_name":[["Qwen3-8B-abliterated-bf16.gguf"]]}}},
		"VAELoader":{"input":{"required":{"vae_name":[["qwen_image_vae.safetensors","flux2-vae.safetensors"]]}}},
		"LoraLoader":{"input":{"required":{"lora_name":[["lenovo_krea2.safetensors","krea2_identity_edit_v1_1.safetensors"]]}}},
		"Krea2EditModelPatch":{},"Krea2EditGroundedEncode":{},"AspectRatioSimplifier":{},"UltimateSDUpscale":{},
		"LCAspectRatioPipeOut":{},"LCReferenceLatent":{},"LCPipeEdit":{},"LCSamplerConfigureSimplePipeOut":{},"Power Lora Loader (rgthree)":{}
	}`
	if err := json.Unmarshal([]byte(fixture), &info); err != nil {
		t.Fatal(err)
	}
	catalog := buildGenerationModelCatalog(info)
	presets := buildGenerationPresets(catalog)
	if _, ok := findGenerationPreset(presets, "photoflow-krea2-edit", "image-to-image"); !ok {
		t.Fatalf("Krea 2 edit preset is unavailable: %#v", presets)
	}
	if preset, ok := findGenerationPreset(presets, "photoflow-flux2-edit", "image-to-image"); !ok || !preset.Available {
		t.Fatalf("Flux 2 edit preset is unavailable: %#v", preset)
	}
}

func TestFlux2LoraGroupsExcludeOtherFamilies(t *testing.T) {
	groups := buildFlux2LoraGroups([]string{
		"Krea2/detailer.safetensors",
		"Flux2/klein_style.safetensors",
		"Flux.2/character.safetensors",
		"LTX2/video.safetensors",
	})
	if len(groups) != 1 || groups[0].Name != "Flux2" || len(groups[0].Loras) != 2 {
		t.Fatalf("unexpected Flux2 LoRA groups: %#v", groups)
	}
	if groups[0].Loras[0].Name != "Flux.2/character.safetensors" || groups[0].Loras[1].Name != "Flux2/klein_style.safetensors" {
		t.Fatalf("unexpected Flux2 LoRA entries: %#v", groups[0].Loras)
	}
}

func TestQuickGenerationPresetLimitsPhotoFlowToKreaFamily(t *testing.T) {
	catalog := generationModelCatalog{byID: map[string]generationModel{}}
	krea := generationModel{ID: "krea", Name: "Krea2/model-v40.safetensors", DisplayName: "Krea2 / model-v40", Family: modelFamilyKrea2, Available: true, DefaultSteps: 8, DefaultCFG: 1}
	flux := generationModel{ID: "flux", Name: "Flux2/model.safetensors", DisplayName: "Flux2 / model", Family: modelFamilyFlux2, Available: true}
	checkpoint := generationModel{ID: "checkpoint", Name: "regular.safetensors", Family: modelFamilyCheckpoint, Available: true}
	catalog.Groups = []generationModelGroup{{Name: "models", Models: []generationModel{krea, flux, checkpoint}}}
	for _, model := range catalog.Groups[0].Models {
		catalog.byID[model.ID] = model
	}
	presets := buildGenerationPresets(catalog)
	preset, ok := findGenerationPreset(presets, "photoflow-krea2", "text-to-image")
	if !ok || preset.Family != modelFamilyKrea2 || preset.ModelID != krea.ID {
		t.Fatalf("unexpected PhotoFlow preset: %#v", preset)
	}
	quickModels := quickGenerationModels(catalog)
	if len(quickModels) != 2 {
		t.Fatalf("quick model count = %d, want only Krea2 and Flux2", len(quickModels))
	}
	for _, model := range quickModels {
		if model.Family == modelFamilyCheckpoint {
			t.Fatalf("checkpoint leaked into the quick generation allowlist: %#v", model)
		}
	}
}

func TestValidateGenerationImageNamespace(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	own := comfyUploadNamespace(app.comfyClientID(7))
	if err := app.validateGenerationImage(own+"/input.png", 7); err != nil {
		t.Fatal(err)
	}
	if err := app.validateGenerationImage("gateway/gateway-aaaaaaaaaaaaaaaaaaaaaaaa/input.png", 7); err == nil {
		t.Fatal("foreign image namespace was accepted")
	}
}

func TestParseGenerationOutputs(t *testing.T) {
	outputs, err := parseGenerationOutputs(map[string]json.RawMessage{
		"9":  json.RawMessage(`{"images":[{"filename":"AI-Gateway_00001.png","subfolder":"","type":"output"}]}`),
		"10": json.RawMessage(`{"videos":[{"filename":"clip.mp4","subfolder":"","type":"output","format":"video/mp4"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 || outputs[0].URL == "" || outputs[1].URL == "" {
		t.Fatalf("unexpected outputs: %#v", outputs)
	}
	if !strings.Contains(outputs[0].URL, "/generate/output?") || !strings.Contains(outputs[1].URL, "/generate/output?") {
		t.Fatalf("missing image view URL: %#v", outputs)
	}
}

func TestSubmitComfyPromptInjectsUserClientID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/prompt" {
			t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var document struct {
			ClientID  string         `json:"client_id"`
			Prompt    map[string]any `json:"prompt"`
			ExtraData map[string]any `json:"extra_data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&document); err != nil {
			t.Fatal(err)
		}
		if document.ClientID == "" || len(document.Prompt) != 1 {
			t.Fatalf("gateway identity or prompt missing: %#v", document)
		}
		extra, ok := document.ExtraData["extra_pnginfo"].(map[string]any)
		if !ok {
			t.Fatalf("extra_pnginfo is missing: %#v", document.ExtraData)
		}
		if _, ok := extra["workflow"].(map[string]any); !ok {
			t.Fatalf("workflow metadata is missing: %#v", extra)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"prompt_id":"abcdef0123456789"}`))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, SessionSecret: "01234567890123456789012345678901"}}
	promptID, err := app.submitComfyPrompt(context.Background(), 17, map[string]any{"1": map[string]any{"class_type": "Test"}})
	if err != nil {
		t.Fatal(err)
	}
	if promptID != "abcdef0123456789" {
		t.Fatalf("prompt ID = %q", promptID)
	}
}

func TestFetchGenerationStatusParsesHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history/abcdef0123456789" {
			t.Fatalf("unexpected history path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"abcdef0123456789":{"status":{"status_str":"success","completed":true},"outputs":{"9":{"images":[{"filename":"result.png","subfolder":"","type":"output"}]}}}}`))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, SessionSecret: "01234567890123456789012345678901"}}
	status, err := app.fetchGenerationStatus(context.Background(), "abcdef0123456789", 17)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "completed" || len(status.Outputs) != 1 || status.Outputs[0].Filename != "result.png" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestFetchGenerationStatusReportsQueuePosition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/history/abcdef0123456789":
			_, _ = w.Write([]byte(`{}`))
		case "/queue":
			_, _ = w.Write([]byte(`{"queue_running":[],"queue_pending":[[1,"other",{},{}],[2,"abcdef0123456789",{},{}]]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, SessionSecret: "01234567890123456789012345678901"}}
	status, err := app.fetchGenerationStatus(context.Background(), "abcdef0123456789", 17)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "queued" || status.QueuePosition != 2 || status.QueueTotal != 2 {
		t.Fatalf("unexpected queue status: %#v", status)
	}
}

func TestGenerationQueueOverviewCountsRunningAndPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/queue" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"queue_running":[[1,"running-one",{},{}]],"queue_pending":[[2,"pending-one",{},{}],[3,"pending-two",{},{}]]}`))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, SessionSecret: "01234567890123456789012345678901"}}
	overview, err := app.generationQueueOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if overview.Running != 1 || overview.Pending != 2 {
		t.Fatalf("unexpected queue overview: %#v", overview)
	}
}

func TestReleaseComfyMemoryWhenQueueIsIdle(t *testing.T) {
	var freeRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			_, _ = w.Write([]byte(`{"queue_running":[],"queue_pending":[]}`))
		case "/free":
			freeRequests++
			if r.Method != http.MethodPost {
				t.Fatalf("free method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Basic test-token" {
				t.Fatalf("authorization = %q", got)
			}
			var payload map[string]bool
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if !payload["unload_models"] || !payload["free_memory"] {
				t.Fatalf("unexpected free payload: %#v", payload)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, ComfyUIUpstreamAuthHeader: "Basic test-token"}}
	app.releaseComfyMemoryIfIdle(context.Background())
	if freeRequests != 1 {
		t.Fatalf("free requests = %d, want 1", freeRequests)
	}
}

func TestReleaseComfyMemoryKeepsQueuedGenerationLoaded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			_, _ = w.Write([]byte(`{"queue_running":[[1,"running",{},{}]],"queue_pending":[]}`))
		case "/free":
			t.Fatal("memory release must not run while a generation is active")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream}}
	app.releaseComfyMemoryIfIdle(context.Background())
}

func TestMiniMaxH3WorkflowBuildsFrameAndReferenceGraphs(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "minimax-h3-video")
	if !ok || definition.Builder != "minimax_h3" || !definition.AdminOnly {
		t.Fatalf("MiniMax H3 definition is missing or not restricted: %#v", definition)
	}
	base := generationForm{
		ModelName:      "MiniMax\\MiniMax_H3_FL2VA_pruned_int8_convrot.safetensors",
		ReferenceModel: "MiniMax\\MiniMax_H3_Ref2VA_pruned_int8_convrot.safetensors",
		TextEncoder:    "MiniMax\\qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors",
		VAE:            "MiniMax\\minimax_h3_video_vae_fp16.safetensors",
		AudioVAE:       "MiniMax\\minimax_h3_audio_vae_fp32.safetensors",
		Positive:       "A dancer turns toward the camera in warm evening light.", InputImage: "gateway/input-1.png", Width: 768, Height: 1344,
		Steps: 25, CFG: 1, Denoise: 1, Sampler: "res_multistep", Scheduler: "simple", Seed: 42,
		VideoMode: miniMaxH3FrameMode, VideoResolution: "portrait", VideoDurationSeconds: 5, VideoSteps: 25,
	}
	prompt, err := definition.buildPrompt(base)
	if err != nil {
		t.Fatal(err)
	}
	frame := prompt["7"].(map[string]any)
	if got, want := frame["class_type"], "MiniMaxH3ImageToVideo"; got != want {
		t.Fatalf("frame node type = %v, want %v", got, want)
	}
	frameInputs := frame["inputs"].(map[string]any)
	if got, want := frameInputs["length"], 124; got != want {
		t.Fatalf("five second frame count = %v, want %v", got, want)
	}
	if _, ok := frameInputs["first_frame"]; !ok {
		t.Fatal("first frame is absent")
	}
	video := prompt["17"].(map[string]any)["inputs"].(map[string]any)
	if got, want := video["format"], "video/h264-mp4"; got != want {
		t.Fatalf("browser video format = %v, want %q", got, want)
	}

	base.VideoMode = miniMaxH3ReferenceMode
	base.InputImage = "gateway/input-1.png"
	base.ReferenceImages = [3]string{"gateway/input-2.png", "gateway/input-3.png"}
	prompt, err = definition.buildPrompt(base)
	if err != nil {
		t.Fatal(err)
	}
	references := prompt["7"].(map[string]any)
	if got, want := references["class_type"], "MiniMaxH3ReferenceToVideo"; got != want {
		t.Fatalf("reference node type = %v, want %v", got, want)
	}
	referenceInputs := references["inputs"].(map[string]any)
	if _, ok := referenceInputs["ref_images.ref_image_2"]; !ok {
		t.Fatalf("third reference image is absent: %#v", referenceInputs)
	}
	if got, want := prompt["1"].(map[string]any)["inputs"].(map[string]any)["unet_name"], base.ReferenceModel; got != want {
		t.Fatalf("reference model = %v, want %q", got, want)
	}
}

func TestMiniMaxH3RequiresFirstFrame(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := findWorkflow(definitions, "minimax-h3-video")
	_, err = definition.buildPrompt(generationForm{
		ModelName: "model", ReferenceModel: "reference", TextEncoder: "clip", VAE: "video-vae", AudioVAE: "audio-vae",
		Positive: "animate", Width: 768, Height: 1344, Steps: 25, CFG: 1, Denoise: 1, Sampler: "res_multistep", Scheduler: "simple",
		VideoMode: miniMaxH3FrameMode, VideoResolution: "portrait", VideoDurationSeconds: 5, VideoSteps: 25,
	})
	if err == nil || !strings.Contains(err.Error(), "первый кадр") {
		t.Fatalf("first frame error = %v", err)
	}
}
