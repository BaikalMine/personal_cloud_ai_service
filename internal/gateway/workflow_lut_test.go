package gateway

import (
	"testing"
	"time"
)

func TestImageEditLUTIncludesRequiredInterpolation(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	// LC123 now requires interpolation even when LUT strength is zero.
	info := map[string]comfyNodeInfo{
		"LCApplyLUT": comfyNodeInfoFromJSON(t, `{
			"input":{"required":{
				"image":["IMAGE"],
				"lut_name":[["LC_Crushed_Blacks.cube","street.cube"]],
				"strength":["FLOAT",{"min":0,"max":2}],
				"log":["BOOLEAN",{"default":false}],
				"interpolation":[["tetrahedral","trilinear"],{"default":"tetrahedral"}]
			}},"output":["IMAGE"]
		}`),
		"FixtureImage": comfyNodeInfoFromJSON(t, `{"output":["IMAGE"]}`),
	}
	catalog := buildComfySchemaCatalog(info, time.Time{}, "lc123-interpolation")
	for _, id := range []string{"image-to-image-flux2", "image-to-image-krea2"} {
		definition, ok := findWorkflow(definitions, id)
		if !ok {
			t.Fatalf("missing workflow %s", id)
		}
		for _, mode := range []string{"disabled", "enabled"} {
			t.Run(id+"/"+mode, func(t *testing.T) {
				input := generationForm{
					ModelName: "model.safetensors", TextEncoder: "encoder.safetensors", VAE: "vae.safetensors",
					IdentityLora: "identity.safetensors", InputImage: "gateway/test/input.png",
					Positive: "Change the blue teapot to green", Width: 1216, Height: 1632,
					Steps: 20, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
				}
				wantStrength := 0.0
				if mode == "enabled" {
					input.LUTEnabled, input.LUTName, input.LUTStrength = true, "street.cube", 0.28
					wantStrength = input.LUTStrength
				}
				prompt, err := definition.buildPrompt(input)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for nodeID, raw := range prompt {
					node, ok := promptNode(raw)
					if !ok || node["class_type"] != "LCApplyLUT" {
						continue
					}
					count++
					inputs := node["inputs"].(map[string]any)
					if inputs["interpolation"] != "tetrahedral" || inputs["strength"] != wantStrength {
						t.Fatalf("LUT interpolation/strength = %v/%v", inputs["interpolation"], inputs["strength"])
					}
					source, ok := comfyPromptReference(inputs["image"])
					if !ok {
						t.Fatal("LUT image source is not connected")
					}
					// Validate the built LUT subgraph without requiring installed model files.
					lutPrompt := map[string]any{
						nodeID:        node,
						source.NodeID: map[string]any{"class_type": "FixtureImage", "inputs": map[string]any{}},
					}
					if issues := validateComfyPrompt(catalog, lutPrompt); len(issues) != 0 {
						t.Fatalf("LUT schema validation: %#v", issues)
					}
					delete(inputs, "interpolation")
					if issues := validateComfyPrompt(catalog, lutPrompt); !hasCompatibilityIssue(issues, "missing_required_input", "") {
						t.Fatal("missing interpolation was not detected")
					}
				}
				if count != 1 {
					t.Fatalf("LUT node count = %d, want 1", count)
				}
			})
		}
	}
}
