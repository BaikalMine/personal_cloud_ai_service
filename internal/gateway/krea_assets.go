package gateway

import "strings"

// Both entry points must resolve the same companion assets for a fair comparison.
func kreaCompanionAssets(encoders, vaes []string) (string, string) {
	encoder := firstMatchingModel(encoders,
		func(name string) bool { return strings.EqualFold(name, "qwen3vl_4b_fp8_scaled.safetensors") },
		func(name string) bool {
			value := strings.ToLower(name)
			return strings.Contains(value, "qwen3vl") && strings.Contains(value, "4b") && strings.Contains(value, "heretic")
		},
		func(name string) bool {
			value := strings.ToLower(name)
			return strings.Contains(value, "qwen3-vl-4b") && strings.Contains(value, "fp8")
		},
		func(name string) bool {
			value := strings.ToLower(name)
			return strings.Contains(value, "qwen3vl") && strings.Contains(value, "4b")
		},
	)
	return encoder, exactModel(vaes, "qwen_image_vae.safetensors")
}

func harnessUltraCompanions(catalog generationModelCatalog) map[string][]string {
	result := map[string][]string{"text_encoders": {}, "vaes": {}}
	for _, name := range comfyChoiceStrings(catalog.ObjectInfo.Info, "GatewayKreaProOptimizer", "text_encoder") {
		lower := strings.ToLower(name)
		if (strings.Contains(lower, "qwen3vl") || strings.Contains(lower, "qwen3-vl")) && strings.Contains(lower, "4b") {
			result["text_encoders"] = append(result["text_encoders"], name)
		}
	}
	for _, name := range comfyChoiceStrings(catalog.ObjectInfo.Info, "GatewayKreaProOptimizer", "vae") {
		if strings.EqualFold(name, "qwen_image_vae.safetensors") || strings.EqualFold(name, "krea2RealVae_v10.safetensors") {
			result["vaes"] = append(result["vaes"], name)
		}
	}
	return result
}
