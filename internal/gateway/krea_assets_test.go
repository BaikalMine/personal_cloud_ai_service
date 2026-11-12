package gateway

import (
	"encoding/json"
	"os"
	"testing"
)

func TestHarnessUltraUsesTheWebsiteCompanionResolver(t *testing.T) {
	fixture := loadObjectInfoFixture(t, "krea2-pro.json")
	catalog := buildGenerationModelCatalog(fixture.Nodes)
	models := harnessUltraModels(catalog)
	if len(models) == 0 {
		t.Fatal("missing installed models")
	}
	encoder, vae := kreaCompanionAssets(
		comfyChoiceStrings(catalog.ObjectInfo.Info, "GatewayKreaProOptimizer", "text_encoder"),
		comfyChoiceStrings(catalog.ObjectInfo.Info, "GatewayKreaProOptimizer", "vae"))
	for _, model := range models {
		if model["text_encoder"] != encoder || model["vae"] != vae {
			t.Fatalf("default assets diverge: %#v", model)
		}
	}
	if encoder != "qwen3VLInstruct4bHeretic_v10.safetensors" || vae != "qwen_image_vae.safetensors" {
		t.Fatal(encoder, vae)
	}
}

func TestHarnessUltraCompanionOverrideStaysWithinInstalledKreaAssets(t *testing.T) {
	for _, test := range []struct {
		encoder, vae string
		valid        bool
	}{
		{"qwen3vl_4b_bf16.safetensors", "krea2RealVae_v10.safetensors", true},
		{"qwen3VLInstruct4bHeretic_v10.safetensors", "qwen_image_vae.safetensors", true},
		{"QwenImage2.1\\qwen3vl_8b_int8_convrot.safetensors", "qwen_image_vae.safetensors", false},
		{"qwen3vl_4b_bf16.safetensors", "flux2-vae.safetensors", false},
	} {
		graph, catalog := ultraGraphFixture(t, "text")
		inputs := graph["3857"].(map[string]any)["inputs"].(map[string]any)
		inputs["text_encoder"], inputs["vae"] = test.encoder, test.vae
		if err := validateHarnessUltraGraph(graph, ultraTestID, catalog); (err == nil) != test.valid {
			t.Fatal(test, err)
		}
	}
}

func TestHarnessUltraBuilderParityFixture(t *testing.T) {
	path := os.Getenv("ULTRA_PARITY_GRAPH")
	if path == "" {
		return
	}
	input := kreaProTestInput()
	input.Positive, input.Negative = "Product photograph of a green ceramic vase on a gray table, soft window light.", "blurry, watermark"
	input.Width, input.Height, input.Seed = 512, 512, 42
	input.Steps, input.KreaUltraStepSwap, input.CFG, input.KreaUltraLowCFG = 10, 6, 2, 1
	input.KreaUltraDetailAmount, input.KreaUltraSpeedBoost = .34, false
	input.Denoise, input.KreaProUpscale, input.KreaProMode = 1, 2, "text"
	graph, err := buildKreaProPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
