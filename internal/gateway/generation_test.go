package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowDefinitionsBuildTypedPrompt(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image")
	if !ok {
		t.Fatal("image-to-image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		TemplateID: "image-to-image", Checkpoint: "model.safetensors", InputImage: "gateway/gateway-0123456789abcdef01234567/input.png",
		Positive: "portrait", Negative: "blurry", Width: 1024, Height: 768, Steps: 20, CFG: 7, Denoise: 0.65, Seed: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	node, ok := prompt["3"].(map[string]any)
	if !ok {
		t.Fatalf("sampler node has type %T", prompt["3"])
	}
	inputs := node["inputs"].(map[string]any)
	if got, want := inputs["seed"], int64(42); got != want {
		t.Fatalf("seed = %v, want %v", got, want)
	}
	if got, want := inputs["denoise"], 0.65; got != want {
		t.Fatalf("denoise = %v, want %v", got, want)
	}
	imageNode := prompt["10"].(map[string]any)
	imageInputs := imageNode["inputs"].(map[string]any)
	if got, want := imageInputs["image"], "gateway/gateway-0123456789abcdef01234567/input.png"; got != want {
		t.Fatalf("image = %v, want %v", got, want)
	}
	if _, err := json.Marshal(prompt); err != nil {
		t.Fatal(err)
	}
}

func TestValidateGenerationImageNamespace(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	own := comfyUploadNamespace(app.comfyClientID(7))
	if err := app.validateGenerationImage(own+"/input.png", 7); err != nil {
		t.Fatal(err)
	}
	if err := app.validateGenerationImage("gateway/gateway-aaaaaaaaaaaaaaaaaaaaaaaa/input.png", 7); err == nil {
		t.Fatal("foreign image namespace was accepted")
	}
}

func TestParseGenerationOutputs(t *testing.T) {
	outputs, err := parseGenerationOutputs(map[string]json.RawMessage{
		"9":  json.RawMessage(`{"images":[{"filename":"AI-Gateway_00001.png","subfolder":"","type":"output"}]}`),
		"10": json.RawMessage(`{"videos":[{"filename":"clip.mp4","subfolder":"","type":"output","format":"video/mp4"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 || outputs[0].URL == "" || outputs[1].URL == "" {
		t.Fatalf("unexpected outputs: %#v", outputs)
	}
	if !strings.Contains(outputs[0].URL, "/comfyui/view?") && !strings.Contains(outputs[1].URL, "/comfyui/view?") {
		t.Fatalf("missing image view URL: %#v", outputs)
	}
}
