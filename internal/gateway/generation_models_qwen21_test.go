package gateway

import (
	"encoding/json"
	"testing"
)

func TestQwen21CatalogRecognizesUpdatedModelFolders(t *testing.T) {
	for _, name := range []string{
		`Qwen 2.1\realistic\redqw21UNLOCKEDV2TI2I_redqw21UNLOCKEDV2_3258921.safetensors`,
		`Qwen 2.1/realistic/updated.safetensors`,
		`qwen2.1/updated.safetensors`,
		`QwenImage2.1\qwen-image-2.1-UC-int8_convrot.safetensors`,
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(map[string]any{"input": map[string]any{"required": map[string]any{"unet_name": [][]string{{name}}}}})
			if err != nil {
				t.Fatal(err)
			}
			info := map[string]comfyNodeInfo{
				"UNETLoader": comfyNodeInfoFromJSON(t, string(encoded)),
				"CLIPLoader": comfyNodeInfoFromJSON(t, `{"input":{"required":{"clip_name":[["QwenImage2.1\\qwen3vl_8b_int8_convrot.safetensors"]]}}}`),
				"VAELoader":  comfyNodeInfoFromJSON(t, `{"input":{"required":{"vae_name":[["QwenImage2.1\\qwen_image_2.1_vae_bf16.safetensors"]]}}}`),
			}
			for _, node := range []string{"TextEncodeQwenImage21", "LoadImage", "EmptyLatentImage", "KSampler", "VAEDecode", "SaveImage"} {
				info[node] = comfyNodeInfo{}
			}
			catalog := buildGenerationModelCatalog(info)
			model, ok := catalog.byID[generationModelID(modelFamilyQwen21, name)]
			if !ok || !model.Available || !model.SupportsImage || model.Name != name {
				t.Fatalf("updated model missing from generation/edit catalog: %#v, found=%v", model, ok)
			}
		})
	}
}

func TestQwen21ModelPathDoesNotMatchOtherVersionsOrEncoders(t *testing.T) {
	for _, name := range []string{`Qwen 2.0\model.safetensors`, `Qwen\model.safetensors`, `qwen3vl_8b.safetensors`, `other\Qwen 2.1\model.safetensors`} {
		if isQwen21ModelPath(name) {
			t.Errorf("unrelated model classified as Qwen Image 2.1: %s", name)
		}
	}
}

func TestQwen21CatalogIncludesCivitaiBaseDirectory(t *testing.T) {
	const modelName = `Base_Qwen_2_1\Civitai-3353689\redqw21UNLOCKEDREDGPTK2tAE_redqw21UNLOCKEDV1T2I_3241129.safetensors`
	info := map[string]comfyNodeInfo{
		"UNETLoader": comfyNodeInfoFromJSON(t, `{"input":{"required":{"unet_name":[["Base_Qwen_2_1\\Civitai-3353689\\redqw21UNLOCKEDREDGPTK2tAE_redqw21UNLOCKEDV1T2I_3241129.safetensors"]]}}}`),
		"CLIPLoader": comfyNodeInfoFromJSON(t, `{"input":{"required":{"clip_name":[["QwenImage2.1\\qwen3vl_8b_int8_convrot.safetensors"]]}}}`),
		"VAELoader":  comfyNodeInfoFromJSON(t, `{"input":{"required":{"vae_name":[["QwenImage2.1\\qwen_image_2.1_vae_bf16.safetensors"]]}}}`),
	}
	for _, node := range []string{"TextEncodeQwenImage21", "LoadImage", "EmptyLatentImage", "KSampler", "VAEDecode", "SaveImage"} {
		info[node] = comfyNodeInfo{}
	}
	catalog := buildGenerationModelCatalog(info)
	model, ok := catalog.byID[generationModelID(modelFamilyQwen21, modelName)]
	if !ok || !model.Available || !model.SupportsImage || model.Name != modelName {
		t.Fatalf("Civitai Qwen 2.1 model not available for generation and editing: %#v, found=%v", model, ok)
	}
}
