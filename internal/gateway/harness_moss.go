package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
)

func harnessMossAssets(catalog generationModelCatalog) generationModelCatalog {
	info := catalog.ObjectInfo.Info
	result := generationModelCatalog{}
	appendMossModels(&result, info, comfyChoiceStrings(info, "UNETLoader", "unet_name"), comfyChoiceStrings(info, "CLIPLoader", "clip_name"), comfyChoiceStrings(info, "VAELoader", "vae_name"), comfyChoiceStrings(info, "LoraLoaderModelOnly", "lora_name"))
	return result
}

func harnessMossModels(catalog generationModelCatalog) []map[string]any {
	models := []map[string]any{}
	for _, group := range harnessMossAssets(catalog).Groups {
		for _, model := range group.Models {
			if model.Available {
				models = append(models, map[string]any{"name": model.Name, "text_encoder": model.TextEncoder, "vae": model.VAE})
			}
		}
	}
	return models
}

func harnessMossLoras(catalog generationModelCatalog) []string {
	names := []string{}
	for _, group := range harnessMossAssets(catalog).MossLoraGroups {
		for _, lora := range group.Loras {
			names = append(names, lora.Name)
		}
	}
	return names
}

func harnessMossUpscaleAvailable(catalog generationModelCatalog) bool {
	info := catalog.ObjectInfo.Info
	return hasGenerationNodes(info, "SeedVR2LoadDiTModel", "SeedVR2LoadVAEModel", "SeedVR2VideoUpscaler") &&
		exactModel(comfyChoiceStrings(info, "SeedVR2LoadDiTModel", "model"), "seedvr2_ema_7b_sharp_fp8_e4m3fn_mixed_block35_fp16.safetensors") != "" &&
		exactModel(comfyChoiceStrings(info, "SeedVR2LoadVAEModel", "model"), "ema_vae_fp16.safetensors") != ""
}

func (a *App) harnessMossCatalog(w http.ResponseWriter, r *http.Request) {
	catalog := a.harnessUltraExecutorCatalog(r.Context())
	models := harnessMossModels(catalog)
	if !catalog.Online || len(models) == 0 {
		http.Error(w, "MOSS Image executor unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow": mossPresetID, "version": "1", "models": models, "loras": harnessMossLoras(catalog),
		"samplers": comfyChoiceStrings(catalog.ObjectInfo.Info, "KSampler", "sampler_name"), "schedulers": comfyChoiceStrings(catalog.ObjectInfo.Info, "KSampler", "scheduler"),
		"seedvr2_available": harnessMossUpscaleAvailable(catalog)})
}

// Reconstruct the website graph instead of accepting an editable arbitrary graph.
// The only difference is the independent Harness output namespace and node ID.
func validateHarnessMossGraph(graph map[string]any, id string, catalog generationModelCatalog) error {
	if !harnessUltraID.MatchString(id) || len(graph) < 9 || len(graph) > 20 {
		return errors.New("invalid MOSS workflow identity or size")
	}
	fields := func(id string) map[string]any {
		node, _ := graph[id].(map[string]any)
		inputs, _ := node["inputs"].(map[string]any)
		return inputs
	}
	text := func(id, name string) string { value, _ := fields(id)[name].(string); return value }
	number := func(id, name string) float64 {
		value, ok := fields(id)[name].(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			return math.NaN()
		}
		return value
	}
	width, height, steps, seed, cfg := number("126", "width"), number("126", "height"), number("4", "steps"), number("4", "seed"), number("4", "cfg")
	integer := func(value, low, high float64) bool {
		return value >= low && value <= high && value == math.Trunc(value)
	}
	if !integer(width, 256, 4096) || !integer(height, 256, 4096) || int(width)%16 != 0 || int(height)%16 != 0 || width*height > 4*1024*1024 ||
		!integer(steps, 1, 100) || !integer(seed, 0, 9007199254740991) || !(cfg >= 1 && cfg <= 20) {
		return errors.New("invalid MOSS canvas or sampling parameters")
	}
	input := generationForm{ModelName: text("32", "unet_name"), TextEncoder: text("34", "clip_name"), VAE: text("15", "vae_name"),
		Positive: text("positive", "text"), Negative: text("3", "text"), Width: int(width), Height: int(height), Steps: int(steps), Seed: int64(seed), CFG: cfg,
		Sampler: text("4", "sampler_name"), Scheduler: text("4", "scheduler"), MossUpscaleResolution: 2560}
	if input.Positive == "" || len(input.Positive) > 128000 || len(input.Negative) > 64000 {
		return errors.New("invalid MOSS prompt")
	}
	available := false
	for _, model := range harnessMossModels(catalog) {
		if input.ModelName == model["name"] && input.TextEncoder == model["text_encoder"] && input.VAE == model["vae"] {
			available = true
		}
	}
	if !available {
		return errors.New("MOSS model or companion assets unavailable")
	}
	if graph["50"] != nil {
		resolution := number("50", "resolution")
		if !integer(resolution, 512, 4096) || int(resolution)%64 != 0 || !harnessMossUpscaleAvailable(catalog) {
			return errors.New("MOSS SeedVR2 assets or resolution unavailable")
		}
		input.MossSeedVR2, input.MossUpscaleResolution = true, int(resolution)
	}
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("moss_lora_%d", i+1)
		if graph[key] == nil {
			continue
		}
		name, strength := text(key, "lora_name"), number(key, "strength_model")
		if exactModel(harnessMossLoras(catalog), name) == "" || seen[name] || !(strength >= -10 && strength <= 10) {
			return errors.New("invalid MOSS LoRA")
		}
		seen[name], input.LoraNames[i], input.LoraModel[i] = true, name, strength
	}
	expected := buildMossPrompt(input)
	saver := expected["63"].(map[string]any)
	saver["inputs"].(map[string]any)["filename_prefix"] = "harness-ultra/" + id + "/Harness-MOSS"
	delete(expected, "63")
	expected["4071"] = saver
	actualJSON, err := json.Marshal(graph)
	expectedJSON, _ := json.Marshal(expected)
	if err != nil || !bytes.Equal(actualJSON, expectedJSON) {
		return errors.New("graph is outside the supported MOSS T2I workflow")
	}
	if issues := validateComfyPrompt(catalog.ObjectInfo.Schema, graph); len(issues) > 0 {
		return fmt.Errorf("MOSS workflow incompatible: %s", issues[0].Message)
	}
	return nil
}
