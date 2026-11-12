package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ai-access-gateway/internal/modelmanager"
	"ai-access-gateway/internal/promptassistant"
)

func harnessMossFixture(t *testing.T, upscale, lora bool) (map[string]any, generationModelCatalog) {
	t.Helper()
	fixture := loadObjectInfoFixture(t, "moss-image.json")
	info := fixture.Nodes
	if file := os.Getenv("MOSS_OBJECT_INFO"); file != "" {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(body, &info); err != nil {
			t.Fatal(err)
		}
	}
	catalog := buildGenerationModelCatalog(info)
	input := mossTestInput()
	if os.Getenv("MOSS_OBJECT_INFO") != "" {
		models := harnessMossModels(catalog)
		if len(models) == 0 {
			t.Fatal("no installed MOSS model")
		}
		input.ModelName = models[0]["name"].(string)
		input.TextEncoder = models[0]["text_encoder"].(string)
		input.VAE = models[0]["vae"].(string)
	}
	input.MossSeedVR2, input.MossUpscaleResolution = upscale, 2560
	if lora {
		input.LoraNames[0], input.LoraModel[0] = "MOSSImage/style.safetensors", .65
	}
	graph := buildMossPrompt(input)
	save := graph["63"]
	delete(graph, "63")
	graph["4071"] = save
	save.(map[string]any)["inputs"].(map[string]any)["filename_prefix"] = "harness-ultra/" + ultraTestID + "/Harness-MOSS"
	body, _ := json.Marshal(graph)
	if err := json.Unmarshal(body, &graph); err != nil {
		t.Fatal(err)
	}
	return graph, catalog
}

func TestHarnessMossUsesWebsiteGraphAndIsolatedOutput(t *testing.T) {
	for _, upscale := range []bool{false, true} {
		for _, lora := range []bool{false, true} {
			if lora && os.Getenv("MOSS_OBJECT_INFO") != "" {
				continue
			}
			graph, catalog := harnessMossFixture(t, upscale, lora)
			if err := validateHarnessUltraGraph(graph, ultraTestID, catalog); err != nil {
				t.Fatal(upscale, lora, err)
			}
			if len(harnessMossModels(catalog)) == 0 {
				t.Fatal("installed MOSS missing")
			}
			if file := os.Getenv("HARNESS_MOSS_GRAPH"); file != "" && !lora && !upscale {
				body, _ := json.Marshal(graph)
				if err := os.WriteFile(file, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestHarnessMossRejectsForeignGraphAndUnsafeSettings(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"foreign output": func(g map[string]any) {
			g["4071"].(map[string]any)["inputs"].(map[string]any)["filename_prefix"] = "other/result"
		},
		"batch": func(g map[string]any) {
			g["126"].(map[string]any)["inputs"].(map[string]any)["batch_size"] = float64(8)
		},
		"huge canvas": func(g map[string]any) {
			g["126"].(map[string]any)["inputs"].(map[string]any)["width"] = float64(4096)
			g["126"].(map[string]any)["inputs"].(map[string]any)["height"] = float64(4096)
		},
		"wrong latent": func(g map[string]any) { g["126"].(map[string]any)["class_type"] = "EmptyLatentImage" },
		"foreign encoder": func(g map[string]any) {
			g["34"].(map[string]any)["inputs"].(map[string]any)["clip_name"] = "Krea2/encoder.safetensors"
		},
		"wrong encoder type": func(g map[string]any) { g["34"].(map[string]any)["inputs"].(map[string]any)["type"] = "flux2" },
		"denoise":            func(g map[string]any) { g["4"].(map[string]any)["inputs"].(map[string]any)["denoise"] = .5 },
		"extra node": func(g map[string]any) {
			g["extra"] = map[string]any{"class_type": "LoadImage", "inputs": map[string]any{"image": "private.png"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			graph, catalog := harnessMossFixture(t, false, false)
			mutate(graph)
			if validateHarnessUltraGraph(graph, ultraTestID, catalog) == nil {
				t.Fatal("unsafe graph accepted")
			}
		})
	}
}

func TestHarnessMossSharesActualAssistantGuide(t *testing.T) {
	w := httptest.NewRecorder()
	(&App{}).harnessImagePromptGuide(w, httptest.NewRequest("POST", "/moss/prompt-guide", strings.NewReader(`{}`)), modelFamilyMoss)
	var result struct {
		Instruction string  `json:"instruction"`
		CFG         float64 `json:"cfg"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	expected := promptassistant.EnhancementInstruction(promptassistant.ModeTextToImage, promptassistant.ProfileWorkflowDefault, nil, promptassistant.VideoContext{}, promptassistant.EditContext{CFG: 3, ImageFamily: "moss"})
	if result.Instruction != expected || result.CFG != 3 {
		t.Fatal("MOSS guide diverged from website")
	}
	for _, body := range []string{`{"mode":"inpaint"}`, `{"cfg":21}`, `{"references":[{"number":1,"role":"identity"}]}`} {
		w = httptest.NewRecorder()
		(&App{}).harnessImagePromptGuide(w, httptest.NewRequest("POST", "/moss/prompt-guide", strings.NewReader(body)), modelFamilyMoss)
		if w.Code != 400 {
			t.Fatal("invalid MOSS guide accepted", body)
		}
	}
}

func TestHarnessMossLoraFamilyNeverTrustsKreaOrFilename(t *testing.T) {
	items := []modelmanager.Item{{Path: "C:/loras/MOSSImage/style.safetensors", Base: "MOSS Image"}, {Path: "C:/loras/MOSSImage/unknown.safetensors"}, {Path: "C:/loras/MOSSImage/wrong.safetensors", Base: "Krea 2"}}
	cards := buildHarnessImageLoraCards([]string{"MOSSImage/style.safetensors", "MOSSImage/unknown.safetensors", "MOSSImage/wrong.safetensors"}, "C:/loras", items, "mossimage")
	for _, card := range cards {
		expected := "incompatible"
		if strings.HasSuffix(card.Name, "/style.safetensors") {
			expected = "verified_moss"
		}
		if strings.HasSuffix(card.Name, "/unknown.safetensors") {
			expected = "unknown"
		}
		if card.Compatibility != expected {
			t.Fatal(card.Name, card.Compatibility)
		}
	}
}
