package gateway

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestQwen21LoraCatalogIsIsolatedAndDeduplicated(t *testing.T) {
	names := []string{`QwenImage2.1\portrait.safetensors`, `qwenimage2.1/portrait.safetensors`,
		`Qwen 2.1/detail.safetensors`, `qwen2.1/style.safetensors`, `Base_Qwen_2_1/adapter.safetensors`,
		`Qwen2.0/wrong.safetensors`, `QwenImage/wrong.safetensors`, `Flux2/wrong.safetensors`, `Krea2/wrong.safetensors`}
	groups := buildQwen21LoraGroups(names)
	if len(groups) != 1 || len(groups[0].Loras) != 4 {
		t.Fatalf("wrong catalog: %#v", groups)
	}
	if generationLoraAllowed(groups, "Flux2/wrong.safetensors") || len(buildQwen21LoraGroups(names[5:])) != 0 {
		t.Fatal("catalog isolation failed")
	}
	info := map[string]comfyNodeInfo{
		"LoraLoaderModelOnly": comfyNodeInfoFromJSON(t, `{"input":{"required":{"lora_name":[["QwenImage2.1/only.safetensors"]]}}}`),
	}
	if !generationLoraAllowed(buildGenerationModelCatalog(info).QwenLoraGroups, "QwenImage2.1/only.safetensors") {
		t.Fatal("model-only loader catalog was ignored")
	}
}

func TestQwen21LorasConnectOnlyDiffusionModel(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"text-to-image-qwen21", "image-to-image-qwen21"} {
		t.Run(id, func(t *testing.T) {
			definition, ok := findWorkflow(definitions, id)
			if !ok {
				t.Fatal("workflow missing")
			}
			input := generationForm{Positive: "A blue ceramic vase", Width: 1024, Height: 1024, Steps: 25, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple",
				OutputMegapixels: 1, ModelName: "model.safetensors", TextEncoder: "encoder.safetensors", VAE: "vae.safetensors"}
			if id == "image-to-image-qwen21" {
				input.InputImage = "source.png"
			}
			input.LoraNames[0], input.LoraModel[0] = "QwenImage2.1/first.safetensors", 0.75
			input.LoraNames[1], input.LoraModel[1] = "QwenImage2.1/disabled.safetensors", 0
			input.LoraNames[9], input.LoraModel[9] = "QwenImage2.1/last.safetensors", -0.25
			prompt, err := definition.buildPrompt(input)
			if err != nil {
				t.Fatal(err)
			}
			inputs := func(id string) map[string]any { return prompt[id].(map[string]any)["inputs"].(map[string]any) }
			if prompt["qwen_lora_1"].(map[string]any)["class_type"] != "LoraLoaderModelOnly" || inputs("qwen_lora_1")["strength_model"] != 0.75 {
				t.Fatal("wrong loader or strength")
			}
			if prompt["qwen_lora_2"] != nil {
				t.Fatal("zero-weight LoRA was loaded")
			}
			if !reflect.DeepEqual(inputs("qwen_lora_10")["model"], []any{"qwen_lora_1", 0}) || !reflect.DeepEqual(inputs("6")["model"], []any{"qwen_lora_10", 0}) {
				t.Fatal("stack is disconnected from sampler")
			}
			if fmt.Sprint(inputs("4")["clip"]) != "[2 0]" {
				t.Fatal("Qwen3-VL encoder was patched")
			}
			input.LoraNames = [maxGenerationLoraSlots]string{}
			plain, err := definition.buildPrompt(input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plain["6"].(map[string]any)["inputs"].(map[string]any)["model"], []any{"1", 0}) {
				t.Fatal("no-LoRA path changed")
			}
		})
	}
}

func TestQwen21LoraControlsAndEmptyState(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, installed := range []bool{false, true} {
		groups := buildQwen21LoraGroups([]string{"QwenImage2.1/portrait.safetensors"})
		if !installed {
			groups = nil
		}
		var html bytes.Buffer
		if err := templates.ExecuteTemplate(&html, "studio_qwen_loras", map[string]any{"QwenLoraSlots": []int{1, 2}, "QwenLoraGroups": groups}); err != nil {
			t.Fatal(err)
		}
		if installed != strings.Contains(html.String(), `data-lora-slots="qwen21"`) || installed == strings.Contains(html.String(), "не установлены") {
			t.Fatal("wrong LoRA controls or empty state")
		}
	}
}
