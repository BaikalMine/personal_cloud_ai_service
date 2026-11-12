package gateway

import (
	"errors"
	"fmt"
	"strings"
)

const mossPresetID = "moss-image"
const modelFamilyMoss = "moss"

func mossWorkflowManifest() workflowManifest {
	parameters := commonImageWorkflowParameters(30, 3, 1)
	for i := range parameters {
		switch parameters[i].Name {
		case "scheduler":
			parameters[i].Default = "simple"
		case "output_megapixels":
			parameters[i].Default, parameters[i].Maximum = 1.0, workflowNumber(4)
		case "aspect_ratio":
			parameters[i].Default = "1:1"
		case "dimension_multiple":
			parameters[i].Default = 16
			parameters[i].Options = []workflowOption{option(16, "16"), option(32, "32"), option(64, "64")}
		}
	}
	parameters = append(parameters,
		booleanWorkflowParameter("moss_seedvr2", "MossSeedVR2", "SeedVR2", "finishing", false),
		integerWorkflowParameter("moss_upscale_resolution", "MossUpscaleResolution", "Длинная сторона", "finishing", 2560, 512, 4096, 64),
	)
	return workflowManifest{
		ID: mossPresetID, Version: "1", DefinitionID: "text-to-image-moss", TemplateID: "text-to-image", Family: modelFamilyMoss,
		Name: "MOSS Image", Description: "Генерация с нуля по workflow MOSS Image T2I.", ModelFamilies: []string{modelFamilyMoss},
		Modes:           []workflowModeManifest{{ID: "text", Name: "Текст в изображение", Default: true, InputLimits: map[string]workflowLimit{"image": {Minimum: 0, Maximum: 0}}}},
		Parameters:      parameters,
		RequiredClasses: []string{"UNETLoader", "CLIPLoader", "VAELoader", "CLIPTextEncode", "EmptyFlux2LatentImage", "KSampler", "VAEDecode", "SaveImage", "LoraLoaderModelOnly"},
		Branches:        []workflowBranchManifest{{ID: "seedvr2", Name: "SeedVR2", ToggleParameter: "moss_seedvr2", RequiredClasses: []string{"SeedVR2LoadDiTModel", "SeedVR2LoadVAEModel", "SeedVR2VideoUpscaler"}, Order: 10}},
		PromptAssistant: workflowAssistantManifest{Profile: "workflow-default", Allowed: true, Rules: []string{"Описать один законченный кадр на английском.", "Точный текст на изображении сохранить без перевода.", "Вернуть отдельный негативный промт при CFG больше 1."}},
		Output:          workflowOutputManifest{Kinds: []string{"image"}, Postprocessing: []string{"seedvr2"}},
	}
}

func isMossImageModelPath(name string) bool {
	normal := strings.ToLower(strings.ReplaceAll(name, "\\", "/"))
	folder, _, _ := strings.Cut(normal, "/")
	return folder == "mossimage" || folder == "moss-image" || folder == "moss image" || folder == "base_moss_image" || strings.Contains(normal, "moss_image_") || strings.Contains(normal, "pornmastermoss")
}

func appendMossModels(catalog *generationModelCatalog, info map[string]comfyNodeInfo, diffusion, encoders, vaes, loras []string) {
	encoder := firstMatchingModel(encoders, func(name string) bool {
		return strings.HasSuffix(normalizeGenerationModelPath(name), "qwen3.5_9b_int8_convrot.safetensors")
	}, func(name string) bool {
		return strings.HasSuffix(normalizeGenerationModelPath(name), "qwen3.5_9b_bf16.safetensors")
	})
	vae := firstMatchingModel(vaes, func(name string) bool {
		return strings.HasSuffix(normalizeGenerationModelPath(name), "flux2-vae.safetensors")
	})
	var models []generationModel
	for _, name := range diffusion {
		if !isMossImageModelPath(name) {
			continue
		}
		model := generationModel{ID: generationModelID(modelFamilyMoss, name), Name: name, DisplayName: generationModelDisplayName(name), Family: modelFamilyMoss,
			TextEncoder: encoder, VAE: vae, DefaultSteps: 30, DefaultCFG: 3, DefaultSampler: "euler", DefaultScheduler: "simple"}
		model.Available = encoder != "" && vae != "" && exactModel(comfyChoiceStrings(info, "CLIPLoader", "type"), "moss_image") != "" && hasGenerationNodes(info, mossWorkflowManifest().RequiredClasses...)
		if !model.Available {
			model.Reason = "Для MOSS нужны актуальный ComfyUI, Qwen3.5 9B и FLUX.2 VAE"
		}
		models = append(models, model)
	}
	if len(models) > 0 {
		catalog.Groups = append(catalog.Groups, generationModelGroup{Name: "MOSS Image", Models: models})
	}
	var adapters []generationLora
	for _, name := range loras {
		normal := normalizeGenerationModelPath(name)
		if strings.HasPrefix(normal, "mossimage\\") || strings.HasPrefix(normal, "trained\\mossimage\\") {
			adapters = append(adapters, generationLora{Name: name, DisplayName: generationModelDisplayName(name), DefaultStrength: 1})
		}
	}
	if len(adapters) > 0 {
		catalog.MossLoraGroups = []generationLoraGroup{{Name: "MOSS Image", Loras: adapters}}
	}
}

func normalizeMoss(input *generationForm) error {
	if input.Width%16 != 0 || input.Height%16 != 0 || input.Width*input.Height > 4*1024*1024 {
		return errors.New("MOSS: размеры должны быть кратны 16, не более 4 Мп")
	}
	if input.imageCount() != 0 {
		return errors.New("MOSS T2I создаёт изображение с нуля и не принимает фото")
	}
	input.Denoise = 1
	if input.MossUpscaleResolution == 0 {
		input.MossUpscaleResolution = 2560
	}
	if input.MossUpscaleResolution < 512 || input.MossUpscaleResolution > 4096 || input.MossUpscaleResolution%64 != 0 {
		return errors.New("MOSS: размер SeedVR2 должен быть от 512 до 4096 и кратен 64")
	}
	for i, name := range input.LoraNames {
		if name != "" && (input.LoraModel[i] < -10 || input.LoraModel[i] > 10) {
			return errors.New("MOSS: сила LoRA должна быть от -10 до 10")
		}
	}
	return nil
}

func buildMossPrompt(input generationForm) map[string]any {
	prompt := map[string]any{}
	add := func(id, class string, inputs map[string]any) {
		prompt[id] = map[string]any{"class_type": class, "inputs": inputs}
	}
	add("32", "UNETLoader", map[string]any{"unet_name": input.ModelName, "weight_dtype": "default"})
	add("34", "CLIPLoader", map[string]any{"clip_name": input.TextEncoder, "type": "moss_image", "device": "default"})
	add("15", "VAELoader", map[string]any{"vae_name": input.VAE})
	model := []any{"32", 0}
	for i, name := range input.LoraNames {
		if name == "" || input.LoraModel[i] == 0 {
			continue
		}
		id := fmt.Sprintf("moss_lora_%d", i+1)
		add(id, "LoraLoaderModelOnly", map[string]any{"model": model, "lora_name": name, "strength_model": input.LoraModel[i]})
		model = []any{id, 0}
	}
	add("positive", "CLIPTextEncode", map[string]any{"clip": []any{"34", 0}, "text": input.Positive})
	add("3", "CLIPTextEncode", map[string]any{"clip": []any{"34", 0}, "text": input.Negative})
	add("126", "EmptyFlux2LatentImage", map[string]any{"width": input.Width, "height": input.Height, "batch_size": 1})
	add("4", "KSampler", map[string]any{"model": model, "positive": []any{"positive", 0}, "negative": []any{"3", 0}, "latent_image": []any{"126", 0}, "seed": input.Seed, "steps": input.Steps, "cfg": input.CFG, "sampler_name": input.Sampler, "scheduler": input.Scheduler, "denoise": 1})
	add("6", "VAEDecode", map[string]any{"samples": []any{"4", 0}, "vae": []any{"15", 0}})
	image := []any{"6", 0}
	if input.MossSeedVR2 {
		// Reuse the installed mixed-FP8 sharp weights on the 32 GB host; no lazy model download inside a GPU job.
		add("56", "SeedVR2LoadDiTModel", map[string]any{"model": "seedvr2_ema_7b_sharp_fp8_e4m3fn_mixed_block35_fp16.safetensors", "device": "cuda:0", "blocks_to_swap": 36, "swap_io_components": false, "offload_device": "cpu", "cache_model": false, "attention_mode": "sageattn_2"})
		add("51", "SeedVR2LoadVAEModel", map[string]any{"model": "ema_vae_fp16.safetensors", "device": "cuda:0", "encode_tiled": true, "encode_tile_size": 1024, "encode_tile_overlap": 128, "decode_tiled": true, "decode_tile_size": 1024, "decode_tile_overlap": 128, "tile_debug": "false", "offload_device": "cpu", "cache_model": false})
		add("50", "SeedVR2VideoUpscaler", map[string]any{"image": image, "dit": []any{"56", 0}, "vae": []any{"51", 0}, "seed": input.Seed, "resolution": input.MossUpscaleResolution, "max_resolution": input.MossUpscaleResolution, "batch_size": 1, "uniform_batch_size": false, "color_correction": "lab", "temporal_overlap": 0, "prepend_frames": 0, "input_noise_scale": 0, "latent_noise_scale": 0, "offload_device": "cpu", "enable_debug": false})
		image = []any{"50", 0}
	}
	add("63", "SaveImage", map[string]any{"images": image, "filename_prefix": "AI-Gateway-MOSS"})
	return prompt
}
