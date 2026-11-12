package gateway

import (
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/promptassistant"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

func aiHubTestInput(t *testing.T) generationForm {
	t.Helper()
	input := generationForm{PresetID: aiHubWorkflowID, TemplateID: "minimax-h3-video", ModelFamily: modelFamilyMiniMaxH3,
		ModelName:   "MiniMax/10Eros_Max_h3_TURBO-hybrid_beta5_int8.safetensors",
		TextEncoder: "MiniMax/qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors",
		VAE:         "MiniMax/minimax_h3_video_vae_fp16.safetensors", AudioVAE: "MiniMax/minimax_h3_audio_vae_fp32.safetensors",
		Positive: "A red toy car travels across a tabletop.", InputImage: "photo.png", InputVideo: "reference.mp4", Width: 352, Height: 640, Seed: 42}
	if err := applyWorkflowManifestDefaults(aiHubWorkflowManifest(), &input); err != nil {
		t.Fatal(err)
	}
	return input
}

func aiHubTestSchema(t *testing.T) map[string]comfyNodeInfo {
	t.Helper()
	path := os.Getenv("AIHUB_SCHEMA_TEST_FILE")
	if path == "" {
		path = "testdata/aihub_object_info.json"
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info := decodeTestObjectInfo(t, string(body))
	for _, file := range []string{"testdata/dlss5_object_info.json", "testdata/dlss_fg_object_info.json", "testdata/gateway_video_stream_object_info.json"} {
		dlss, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for name, node := range decodeTestObjectInfo(t, string(dlss)) {
			info[name] = node
		}
	}
	info["GatewayAIHubVideoStream"] = info["GatewayVideoStream"]
	for name, node := range decodeTestObjectInfo(t, `{"GatewayAIHubReferenceFrames":{"input":{"required":{"images":["IMAGE"]}},"output":["IMAGE","INT","INT"],"output_node":false}}`) {
		info[name] = node
	}
	for name, node := range decodeTestObjectInfo(t, `{"GatewayAIHubEnhanceImages":{"input":{"required":{"images":["IMAGE"],"settings":["DLSS5_SETTINGS"]}},"output":["IMAGE"],"output_node":false}}`) {
		info[name] = node
	}
	return info
}

func TestAIHubCompatibilityUsesOwnGraphForRegularAndTurbo(t *testing.T) {
	info := aiHubTestSchema(t)
	catalog := buildGenerationModelCatalog(info)
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]workflowDefinition{}
	for _, definition := range definitions {
		byID[definition.ID] = definition
	}
	for _, name := range []string{"MiniMax/10Eros_Max_h3_TURBO-hybrid_beta5_int8.safetensors", "MiniMax/MiniMax_H3_Ref2VA_pruned_int8_convrot.safetensors"} {
		model := generationModel{ID: name, Name: name, Family: modelFamilyMiniMaxH3, AIHubOnly: true,
			TextEncoder: "encoder", VAE: "video-vae", AudioVAE: "audio-vae", DefaultSteps: 25}
		if turbo, _ := aiHubModelProfile(name); turbo {
			model.DefaultSteps = 6
		}
		scenarios := compatibilityScenariosForModel(model, catalog, byID)
		if len(scenarios) != 2 {
			t.Fatalf("expected two AIHUB core scenarios, got %d", len(scenarios))
		}
		for _, scenario := range scenarios {
			if scenario.Definition.ID != aiHubWorkflowID || scenario.Input.AIHubSteps != model.DefaultSteps {
				t.Fatal("AIHUB model routed to old workflow", scenario.ID)
			}
			if _, err := scenario.Definition.buildPrompt(scenario.Input); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestAIHubBranchesMatchInstalledSchema(t *testing.T) {
	info := aiHubTestSchema(t)
	schema := buildComfySchemaCatalog(info, time.Time{}, "aihub")
	// File choices are environment-specific; field contracts and edge types are not.
	for name, node := range schema.Nodes {
		for _, key := range []string{"image", "video", "audio", "unet_name", "clip_name", "vae_name", "ckpt_name", "checkpoint", "lora_name"} {
			if field, ok := node.Required[key]; ok {
				field.Choices = nil
				node.Required[key] = field
			}
		}
		schema.Nodes[name] = node
	}
	for _, mode := range []string{"video", "photo", "carousel"} {
		for _, source := range []string{"native", "original", "ltx"} {
			if mode == "carousel" && source != "native" {
				continue
			}
			if mode == "photo" && source == "original" {
				continue
			}
			for _, mask := range []string{"none", "sam3", "sapiens"} {
				if mode != "video" && mask != "none" {
					continue
				}
				t.Run(mode+"/"+source+"/"+mask, func(t *testing.T) {
					input := aiHubTestInput(t)
					input.AIHubMode = mode
					if mode == "photo" {
						input.InputVideo = ""
					}
					input.AIHubAudioSource = source
					input.AIHubMask = mask
					input.AIHubAudioPrompt = "Soft electronic music."
					input.AIHubMaskPrompt = "car"
					input.VideoRTXEnabled = true
					input.AIHubAudioEnhance = source == "ltx"
					input.AIHubT8 = source != "native"
					input.AIHubAudioReference = source != "native"
					input.AIHubVoiceConversion = mode != "carousel"
					if input.AIHubVoiceConversion {
						input.InputAudio = "voice.wav"
					}
					input.AIHubRemoveBackground = mode != "carousel"
					if err := normalizeAIHub(&input); err != nil {
						t.Fatal(err)
					}
					if err := resolveAIHubModels(&input, info); err != nil {
						t.Fatal(err)
					}
					prompt, err := buildAIHubPrompt(input)
					if err != nil {
						t.Fatal(err)
					}
					if issues := validateComfyPrompt(schema, prompt); len(issues) > 0 {
						t.Fatalf("schema issues: %+v", issues)
					}
					if mode == "photo" {
						if _, ok := prompt["20"]; ok {
							t.Fatal("photo mode loads a video")
						}
						conditioning := prompt["7"].(map[string]any)["inputs"].(map[string]any)
						if _, ok := conditioning["ref_videos.ref_video_0"]; ok {
							t.Fatal("photo mode conditions on a video")
						}
						if conditioning["ref_images.ref_image_0"] == nil {
							t.Fatal("photo reference missing")
						}
						if source == "ltx" {
							for _, id := range []string{"100", "118", "120", "81"} {
								if prompt[id] == nil {
									t.Fatalf("photo audio branch missing node %s", id)
								}
							}
						}
					}
					for _, raw := range prompt {
						node := raw.(map[string]any)
						kind := node["class_type"].(string)
						if kind == "DLSS5Enhance" || kind == "LTXVGemmaCLIPModelLoader" {
							t.Fatalf("unexpected branch: %s", kind)
						}
					}
					if mode == "carousel" {
						if _, ok := prompt["100"]; ok {
							t.Fatal("carousel loads LTX")
						}
						if prompt["90"].(map[string]any)["class_type"] != "SaveImage" {
							t.Fatal("carousel output is not images")
						}
					}
				})
			}
		}
	}
}

func TestAIHubPhotoValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*generationForm)
	}{
		{"no photo", func(in *generationForm) { in.InputImage = "" }},
		{"hidden video", func(in *generationForm) { in.InputVideo = "old.mp4" }},
		{"original audio", func(in *generationForm) { in.AIHubAudioSource = "original" }},
		{"video mask", func(in *generationForm) { in.AIHubMask = "sapiens" }},
		{"empty LTX prompt", func(in *generationForm) { in.AIHubAudioSource = "ltx" }},
		{"missing voice", func(in *generationForm) { in.AIHubVoiceConversion = true }},
		{"native T8", func(in *generationForm) { in.AIHubT8 = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := aiHubTestInput(t)
			in.AIHubMode, in.InputVideo = "photo", ""
			tc.change(&in)
			if normalizeAIHub(&in) == nil {
				t.Fatal("invalid photo request accepted")
			}
		})
	}
	in := aiHubTestInput(t)
	in.AIHubMode, in.InputVideo = "photo", ""
	if err := normalizeAIHub(&in); err != nil {
		t.Fatal(err)
	}
	if in.AIHubHalfResolution {
		t.Fatal("draft resolution must be opt-in")
	}
	if w, h := aiHubBaseDimensions(in); w != 352 || h != 640 {
		t.Fatalf("full base: %dx%d", w, h)
	}
	in.AIHubHalfResolution = true
	if w, h := aiHubBaseDimensions(in); w != 192 || h != 320 {
		t.Fatalf("half base: %dx%d", w, h)
	}
	manifest := aiHubWorkflowManifest()
	in.AIHubAudioSource = "ltx"
	in.AIHubAudioPrompt = "Quiet music."
	if err := normalizeAIHub(&in); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "13" {
		t.Fatal("photo mode contract version missing")
	}
}

func TestAIHubRegularModelUsesRegularSampling(t *testing.T) {
	for _, mode := range []string{"photo", "video", "carousel"} {
		in := aiHubTestInput(t)
		in.ModelName = "MiniMax/MiniMax_H3_Ref2VA_pruned_int8_convrot.safetensors"
		in.AIHubMode, in.AIHubSteps = mode, 25
		if mode == "photo" {
			in.InputVideo = ""
		}
		in.AIHubLatentModel = "minimax_h3_latent_upscaler_3d_bf16.safetensors"
		if err := normalizeAIHub(&in); err != nil {
			t.Fatal(err)
		}
		graph, err := buildAIHubPrompt(in)
		if err != nil {
			t.Fatal(err)
		}
		inputs := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
		if inputs("11")["sampler_name"] != "euler" || inputs("12")["scheduler"] != "simple" || inputs("12")["steps"] != 25 {
			t.Fatal("regular model received turbo sampling")
		}
		if inputs("9")["shift_video"] != 11.0 || inputs("9")["shift_audio"] != 3.0 {
			t.Fatal("missing regular shifts")
		}
		in.AIHubSteps = 6
		if normalizeAIHub(&in) == nil {
			t.Fatal("regular accepted turbo steps")
		}
	}
	if _, ok := aiHubModelProfile("MiniMax_H3_FL2VA.safetensors"); ok {
		t.Fatal("FL2VA accepted as Ref2VA")
	}
}

func TestAIHubReferenceLimits(t *testing.T) {
	input := aiHubTestInput(t)
	input.ReferenceImages = [3]string{"2.png", "3.png", "4.png"}
	input.AIHubExtraImages = [5]string{"5.png", "6.png", "7.png", "8.png", "9.png"}
	if input.imageCount() != 9 {
		t.Fatal(input.images())
	}
	if err := normalizeAIHub(&input); err != nil {
		t.Fatal(err)
	}
	input.AIHubMode = "carousel"
	if normalizeAIHub(&input) == nil {
		t.Fatal("carousel accepted nine references")
	}
	input.AIHubExtraImages = [5]string{"5.png"}
	if err := normalizeAIHub(&input); err != nil {
		t.Fatal(err)
	}
	input.PresetID = "minimax-h3-video"
	if input.imageCount() != 4 {
		t.Fatal("extra slots leaked into existing workflow")
	}
}

func TestAIHubInstalledModelProducesDedicatedPreset(t *testing.T) {
	catalog := buildGenerationModelCatalog(aiHubTestSchema(t))
	presets := buildGenerationPresets(catalog)
	preset, ok := findGenerationPreset(presets, aiHubWorkflowID, "minimax-h3-video")
	if !ok || !preset.Available || preset.MaxInputImages != 9 {
		t.Fatalf("AIHUB preset unavailable: %+v", presets)
	}
	if model := catalog.byID[preset.ModelID]; !model.AIHubOnly || model.TextEncoder == "" || model.AudioVAE == "" {
		t.Fatalf("incomplete model: %+v", model)
	}
	for _, other := range presets {
		if other.ID != aiHubWorkflowID && other.ModelID == preset.ModelID {
			t.Fatal("AIHUB model leaked into old default preset")
		}
	}
}

func TestAIHubGalleryDestinationsKeepModelsSeparate(t *testing.T) {
	models := []generationModel{
		{ID: "h3:old", Family: modelFamilyMiniMaxH3, Available: true},
		{ID: "h3:aihub", Name: "10Eros_Max_h3_TURBO-hybrid_beta5_int8.safetensors", Family: modelFamilyMiniMaxH3, Available: true, AIHubOnly: true, VideoReferenceOnly: true},
	}
	catalog := generationModelCatalog{Online: true, Groups: []generationModelGroup{{Models: models}}}
	user := &User{Role: "user", CanUseQuickGeneration: true, CanGenerateVideo: true}
	destinations := galleryCompatibleDestinations(catalog, domain.GenerationAccessPolicy{}, user)
	if len(destinations) != 2 {
		t.Fatalf("expected one destination per workflow: %+v", destinations)
	}
	for _, destination := range destinations {
		if (destination.ID == aiHubWorkflowID) != (destination.ModelID == "h3:aihub") {
			t.Fatalf("incompatible workflow/model pair: %+v", destination)
		}
		if destination.ID == aiHubWorkflowID && (destination.Frames || destination.Maximum != 9) {
			t.Fatalf("incorrect AIHUB reference capabilities: %+v", destination)
		}
	}
}

func TestAIHubHistoryDefinitionAndAssistantLimits(t *testing.T) {
	definition, family, err := generationJobDefinition(domain.GenerationJob{TemplateID: "minimax-h3-video", WorkflowID: aiHubWorkflowID})
	if err != nil || family != modelFamilyMiniMaxH3 || definition.Builder != "aihub_minimax" {
		t.Fatalf("definition=%+v family=%s err=%v", definition, family, err)
	}
	request := httptest.NewRequest("POST", "/", nil)
	request.Form = url.Values{"generation_workflow": {aiHubWorkflowID}, "video_mode": {"references"}}
	if _, err := promptAssistantVideoContext(request, promptassistant.ModeTextToVideo, 9); err != nil {
		t.Fatal(err)
	}
	request.Form.Set("generation_workflow", "minimax-h3-video")
	if _, err := promptAssistantVideoContext(request, promptassistant.ModeTextToVideo, 9); err == nil {
		t.Fatal("existing workflow accepted nine assistant images")
	}
}

func TestAIHubSubmissionParsesExtraImagesAndSettings(t *testing.T) {
	request := httptest.NewRequest("POST", "/", nil)
	request.Form = url.Values{"template_id": {"minimax-h3-video"}, "generation_workflow": {aiHubWorkflowID}, "positive_prompt": {"A red toy car."}, "aihub_mode": {"carousel"}, "input_image": {"first.png"}, "input_image_5": {"fifth.png"}, "input_video": {"clip.mp4"}, "aihub_photo_stride": {"9"}, "aihub_photo_skip": {"0"}, "image_role_5": {"identity"}}
	request.Form.Set("aihub_task", "replace_character")
	request.Form.Set("aihub_audio_reference", "true")
	input, err := parseGenerationForm(request)
	if err != nil {
		t.Fatal(err)
	}
	if input.AIHubPhotoStride != 9 || input.AIHubPhotoSkip != 0 || input.AIHubExtraImages[0] != "fifth.png" || input.VideoMode != "references" {
		t.Fatalf("incorrect parsed AIHUB settings: %+v", input)
	}
	if refs := input.references(); len(refs) != 2 || refs[1].Number != 5 || refs[1].Role != "identity" {
		t.Fatalf("reference metadata: %+v", refs)
	}
	saved := generationJobValues(request.Form, 42)
	if input.AIHubTask != "replace_character" || !input.AIHubAudioReference || saved["aihub_task"] != "replace_character" || saved["aihub_audio_reference"] != "true" {
		t.Fatal("lost editing/audio settings")
	}
	if saved["aihub_photo_stride"] != "9" || saved["input_image_5"] != "fifth.png" {
		t.Fatalf("lost saved values: %+v", saved)
	}
}
