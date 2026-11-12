package gateway

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const qwen21MaxOutputMegapixels = 4.27
const qwen21MaxOutputPixels = 4477419

func buildQwen21LoraGroups(names []string) []generationLoraGroup {
	var loras []generationLora
	seen := make(map[string]bool)
	for _, name := range names {
		key := normalizeGenerationModelPath(name)
		if !isQwen21ModelPath(name) || seen[key] {
			continue
		}
		seen[key] = true
		loras = append(loras, generationLora{Name: name, DisplayName: generationModelDisplayName(name), DefaultStrength: 1})
	}
	if len(loras) == 0 {
		return nil
	}
	sort.Slice(loras, func(i, j int) bool {
		return strings.ToLower(loras[i].DisplayName) < strings.ToLower(loras[j].DisplayName)
	})
	return []generationLoraGroup{{Name: "Qwen Image 2.1", Loras: loras}}
}

func applyQwen21Loras(prompt map[string]any, input generationForm) {
	model := []any{"1", 0}
	for index, name := range input.LoraNames {
		if name == "" || input.LoraModel[index] == 0 {
			continue
		}
		id := fmt.Sprintf("qwen_lora_%d", index+1)
		prompt[id] = map[string]any{"class_type": "LoraLoaderModelOnly", "inputs": map[string]any{
			"model": model, "lora_name": name, "strength_model": input.LoraModel[index],
		}}
		model = []any{id, 0}
	}
	prompt["6"].(map[string]any)["inputs"].(map[string]any)["model"] = model
}

func qwen21ReferenceSize(width, height int, megapixels float64) (int, int, int) {
	if width <= 0 || height <= 0 {
		return 0, 0, 0
	}
	if megapixels > qwen21MaxOutputMegapixels {
		megapixels = 1
	}
	resolution := int(math.RoundToEven(math.Sqrt(megapixels*1024*1024)/32)) * 32
	if resolution > 4096 {
		resolution = 4096
	}
	for ; resolution >= 256; resolution -= 32 {
		outputWidth := int(math.RoundToEven(math.Sqrt(float64(resolution*resolution)*float64(width)/float64(height))/32)) * 32
		outputHeight := int(math.RoundToEven(math.Sqrt(float64(resolution*resolution)*float64(height)/float64(width))/32)) * 32
		if outputWidth >= 256 && outputHeight >= 256 && outputWidth <= 4096 && outputHeight <= 4096 && outputWidth*outputHeight <= qwen21MaxOutputPixels {
			return outputWidth, outputHeight, resolution
		}
	}
	return 0, 0, 0
}

func buildQwen21EditPrompt(input generationForm) map[string]any {
	images := [10]string{input.InputImage, input.ReferenceImages[0], input.ReferenceImages[1], input.ReferenceImages[2]}
	copy(images[4:], input.QwenExtraImages[:])
	encodeInputs := map[string]any{
		"clip": []any{"2", 0}, "vae": []any{"3", 0},
		"prompt": input.Positive, "negative_prompt": input.Negative, "resolution": input.QwenResolution,
	}
	prompt := map[string]any{
		"1": map[string]any{"class_type": "UNETLoader", "inputs": map[string]any{"unet_name": input.ModelName, "weight_dtype": "default"}},
		"2": map[string]any{"class_type": "CLIPLoader", "inputs": map[string]any{"clip_name": input.TextEncoder, "type": "qwen_image", "device": "default"}},
		"3": map[string]any{"class_type": "VAELoader", "inputs": map[string]any{"vae_name": input.VAE}},
		"4": map[string]any{"class_type": "TextEncodeQwenImage21", "inputs": encodeInputs},
		"6": map[string]any{"class_type": "KSampler", "inputs": map[string]any{
			"model": []any{"1", 0}, "positive": []any{"4", 0}, "negative": []any{"4", 1},
			"latent_image": []any{"4", 2}, "seed": input.Seed, "steps": input.Steps,
			"cfg": input.CFG, "sampler_name": input.Sampler, "scheduler": input.Scheduler, "denoise": 1,
		}},
		"7": map[string]any{"class_type": "VAEDecode", "inputs": map[string]any{"samples": []any{"6", 0}, "vae": []any{"3", 0}}},
		"8": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{"images": []any{"7", 0}, "filename_prefix": "AI-Gateway-Qwen21-Edit"}},
	}
	if input.EditUseCustomSize && !input.PreserveOriginalSize {
		prompt["5"] = map[string]any{"class_type": "EmptyLatentImage", "inputs": map[string]any{"width": input.Width, "height": input.Height, "batch_size": 1}}
		prompt["6"].(map[string]any)["inputs"].(map[string]any)["latent_image"] = []any{"5", 0}
	}
	for index, image := range images {
		if strings.TrimSpace(image) == "" {
			continue
		}
		nodeID := fmt.Sprintf("%d", 10+index)
		prompt[nodeID] = map[string]any{"class_type": "LoadImage", "inputs": map[string]any{"image": image}}
		encodeInputs[fmt.Sprintf("images.image_%d", index+1)] = []any{nodeID, 0}
	}
	applyQwen21Loras(prompt, input)
	return prompt
}
