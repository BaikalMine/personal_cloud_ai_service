package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGenerateTemplateRenders(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = templates.ExecuteTemplate(&output, "generate", map[string]any{
		"Title": "Быстрая генерация", "CSRF": "csrf", "AssetVersion": "asset",
		"Workflows": []workflowView{{ID: "text-to-image", Name: "Текст в изображение", Description: "Описание"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "data-comfy-generation") || !strings.Contains(output.String(), "/static/generate.js") {
		t.Fatal("generation template did not render the wizard")
	}
}

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

func TestSubmitComfyPromptInjectsUserClientID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/prompt" {
			t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var document struct {
			ClientID string         `json:"client_id"`
			Prompt   map[string]any `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&document); err != nil {
			t.Fatal(err)
		}
		if document.ClientID == "" || len(document.Prompt) != 1 {
			t.Fatalf("gateway identity or prompt missing: %#v", document)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"prompt_id":"abcdef0123456789"}`))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, SessionSecret: "01234567890123456789012345678901"}}
	promptID, err := app.submitComfyPrompt(context.Background(), 17, map[string]any{"1": map[string]any{"class_type": "Test"}})
	if err != nil {
		t.Fatal(err)
	}
	if promptID != "abcdef0123456789" {
		t.Fatalf("prompt ID = %q", promptID)
	}
}

func TestFetchGenerationStatusParsesHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history/abcdef0123456789" {
			t.Fatalf("unexpected history path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"abcdef0123456789":{"status":{"status_str":"success","completed":true},"outputs":{"9":{"images":[{"filename":"result.png","subfolder":"","type":"output"}]}}}}`))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, SessionSecret: "01234567890123456789012345678901"}}
	status, err := app.fetchGenerationStatus(context.Background(), "abcdef0123456789", 17)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "completed" || len(status.Outputs) != 1 || status.Outputs[0].Filename != "result.png" {
		t.Fatalf("unexpected status: %#v", status)
	}
}
