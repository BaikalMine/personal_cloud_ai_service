package gateway

import (
	"errors"
	"fmt"
	"strings"
)

const kreaUltraPresetID = "krea2-ultra"
const kreaUltraDefinitionID = "text-to-image-krea2-ultra"

func kreaUltraWorkflowManifest() workflowManifest {
	parameters := commonImageWorkflowParameters(10, 2, 1)
	for i := range parameters {
		p := &parameters[i]
		switch p.Name {
		case "steps":
			*p = integerWorkflowParameter("steps", "Steps", "Шаги генерации", "generation", 10, 4, 40, 1)
		case "cfg":
			*p = numberWorkflowParameter("cfg", "CFG", "CFG первого прохода", "generation", 2, 1, 5, 0.1)
		case "scheduler":
			*p = enumWorkflowParameter("scheduler", "Scheduler", "Планировщик", "generation", "simple", option("simple", "Simple"), option("beta", "Beta"))
		case "output_megapixels":
			*p = numberWorkflowParameter("output_megapixels", "OutputMegapixels", "Разрешение", "source", 1.9, 0.5, 4.27, 0.05)
		}
	}
	parameters = append(parameters,
		integerWorkflowParameter("krea_ultra_step_swap", "KreaUltraStepSwap", "Начало второго прохода", "generation", 6, 1, 39, 1),
		numberWorkflowParameter("krea_ultra_low_cfg", "KreaUltraLowCFG", "CFG второго прохода", "generation", 1, 1, 3, 0.1),
		numberWorkflowParameter("krea_ultra_detail_amount", "KreaUltraDetailAmount", "Сила детализации", "detail", 0.34, 0, 2, 0.01),
		booleanWorkflowParameter("krea_ultra_speed_boost", "KreaUltraSpeedBoost", "Speed Boost", "optimization", false),
		integerWorkflowParameter("krea_ultra_grow_step", "KreaUltraGrowStep", "Шаг увеличения", "optimization", 4, 1, 39, 1),
	)
	return workflowManifest{
		ID: kreaUltraPresetID, Version: "26.3-core.1", DefinitionID: kreaUltraDefinitionID,
		TemplateID: "text-to-image", Family: modelFamilyKrea2, ModelFamilies: []string{modelFamilyKrea2},
		Name: "Krea2 · Ультра качество", Description: "Экспериментальный профиль Lonecat: два прохода и Detail Daemon.",
		Modes:           []workflowModeManifest{{ID: "text", Name: "Текст в изображение", Default: true, InputLimits: map[string]workflowLimit{"image": {Minimum: 0, Maximum: 0}}}},
		Parameters:      parameters,
		RequiredClasses: []string{"UNETLoader", "CLIPLoader", "VAELoader", "CLIPTextEncode", "EmptyLatentImage", "GatewayKreaUltraSampler", "VAEDecodeTiled", "SaveImage"},
		QualityProfiles: []workflowQualityProfileManifest{{ID: "ultra", Name: "Ультра качество", Default: true, Description: "Lonecat split-sigma, без обязательных стилевых LoRA.", Parameters: map[string]workflowProfileParameterManifest{"steps": profileValue(10), "cfg": profileValue(2), "krea_ultra_step_swap": profileValue(6), "krea_ultra_detail_amount": profileValue(0.34)}}},
		PromptAssistant: workflowAssistantManifest{Profile: "workflow-default", Allowed: true, Rules: []string{"Вернуть цельный позитивный промт для Krea2.", "Не добавлять персонажную LoRA или нежелательную стилизацию."}},
		Output:          workflowOutputManifest{Kinds: []string{"image"}, Postprocessing: []string{"split_sigma_detail"}},
	}
}

func normalizeKreaUltra(input *generationForm) error {
	if input.KreaUltraStepSwap == 0 {
		input.KreaUltraStepSwap = 6
	}
	if input.KreaUltraLowCFG == 0 {
		input.KreaUltraLowCFG = 1
	}
	if input.KreaUltraGrowStep == 0 {
		input.KreaUltraGrowStep = 4
	}
	if input.KreaUltraStepSwap >= input.Steps {
		return errors.New("Ультра качество: второй проход должен начинаться до последнего шага")
	}
	if input.KreaUltraSpeedBoost && input.KreaUltraGrowStep >= input.KreaUltraStepSwap {
		return errors.New("Ультра качество: увеличение должно происходить до второго прохода")
	}
	if input.Width*input.Height > qwen21MaxOutputPixels {
		return errors.New("Ультра качество: максимальный размер результата 4,27 Мп")
	}
	if strings.Contains(strings.ToLower(input.ModelName), "raw") {
		return errors.New("Ультра качество: выберите Krea2 Turbo или совместимую Turbo-модель, не Raw")
	}
	return nil
}

func buildKreaUltraPrompt(input generationForm) map[string]any {
	node := func(kind string, fields map[string]any) any {
		return map[string]any{"class_type": kind, "inputs": fields}
	}
	link := func(id string, output int) []any { return []any{id, output} }
	graph := map[string]any{
		"1": node("UNETLoader", map[string]any{"unet_name": input.ModelName, "weight_dtype": "default"}),
		"2": node("CLIPLoader", map[string]any{"clip_name": input.TextEncoder, "type": "krea2", "device": "default"}),
		"3": node("VAELoader", map[string]any{"vae_name": input.VAE}),
		"4": node("CLIPTextEncode", map[string]any{"text": input.Positive, "clip": link("2", 0)}),
		"5": node("CLIPTextEncode", map[string]any{"text": input.Negative, "clip": link("2", 0)}),
		"6": node("EmptyLatentImage", map[string]any{"width": input.Width, "height": input.Height, "batch_size": 1}),
		"7": node("GatewayKreaUltraSampler", map[string]any{
			"model": link("1", 0), "positive": link("4", 0), "negative": link("5", 0), "latent_image": link("6", 0),
			"seed": input.Seed, "steps": input.Steps, "step_swap": input.KreaUltraStepSwap, "cfg_high": input.CFG,
			"cfg_low": input.KreaUltraLowCFG, "detail_amount": input.KreaUltraDetailAmount,
			"sampler_name": input.Sampler, "scheduler": input.Scheduler,
			"speed_boost": input.KreaUltraSpeedBoost, "grow_at_step": input.KreaUltraGrowStep,
		}),
		"8": node("VAEDecodeTiled", map[string]any{"samples": link("7", 0), "vae": link("3", 0), "tile_size": 512, "overlap": 64, "temporal_size": 64, "temporal_overlap": 8}),
		"9": node("SaveImage", map[string]any{"images": link("8", 0), "filename_prefix": "AI-Gateway-Krea2-Ultra"}),
	}
	modelTail, clipTail := link("1", 0), link("2", 0)
	for i, name := range input.LoraNames {
		if name == "" {
			continue
		}
		id := fmt.Sprint(100 + i)
		graph[id] = node("LoraLoader", map[string]any{"model": modelTail, "clip": clipTail, "lora_name": name, "strength_model": input.LoraModel[i], "strength_clip": input.LoraClip[i]})
		modelTail, clipTail = link(id, 0), link(id, 1)
	}
	graph["7"].(map[string]any)["inputs"].(map[string]any)["model"] = modelTail
	for _, id := range []string{"4", "5"} {
		graph[id].(map[string]any)["inputs"].(map[string]any)["clip"] = clipTail
	}
	return graph
}
