package gateway

import (
	"math"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestAIHubDLSSGraphAndSchema(t *testing.T) {
	info := aiHubTestSchema(t)
	body, err := os.ReadFile("testdata/dlss5_object_info.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, node := range decodeTestObjectInfo(t, string(body)) {
		info[name] = node
	}
	schema := buildComfySchemaCatalog(info, time.Time{}, "dlss5")
	for name, node := range schema.Nodes {
		for _, key := range []string{"image", "video", "audio", "unet_name", "clip_name", "vae_name", "ckpt_name", "checkpoint", "lora_name"} {
			if field, ok := node.Required[key]; ok {
				field.Choices = nil
				node.Required[key] = field
			}
		}
		schema.Nodes[name] = node
	}
	for _, mode := range []string{"photo", "video", "carousel"} {
		for _, rtx := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/native", true: "/rtx"}[rtx], func(t *testing.T) {
				in := aiHubTestInput(t)
				in.AIHubMode, in.VideoRTXEnabled = mode, rtx
				if mode == "photo" {
					in.InputVideo = ""
				}
				if mode == "video" {
					in.AIHubAudioSource = "original"
				}
				if err := normalizeAIHub(&in); err != nil {
					t.Fatal(err)
				}
				if err := resolveAIHubModels(&in, info); err != nil {
					t.Fatal(err)
				}
				base, err := buildAIHubPrompt(in)
				if err != nil {
					t.Fatal(err)
				}
				if base["71"] != nil || base["72"] != nil {
					t.Fatal("DLSS enabled by default")
				}
				in.AIHubDLSSEnabled = true
				graph, err := buildAIHubPrompt(in)
				if err != nil {
					t.Fatal(err)
				}
				inputs := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
				if mode == "carousel" && graph["72"].(map[string]any)["class_type"] != "GatewayAIHubEnhanceImages" {
					t.Fatal("independent photo enhancement missing")
				}
				if inputs("71")["upscaling_mode"] != "1x (DLAA / native)" {
					t.Fatal("incorrect settings")
				}
				if !reflect.DeepEqual(inputs("71"), aiHubDLSSSettings(in.AIHubDLSSStrength, mode == "carousel")) {
					t.Fatal("quality profile regressed")
				}
				if mode != "carousel" {
					if graph["90"].(map[string]any)["class_type"] != "GatewayAIHubVideoStream" || inputs("90")["nr_enabled"] != true || inputs("90")["rtx_enabled"] != rtx {
						t.Fatal("streaming enhancement choices lost")
					}
					if !reflect.DeepEqual(inputs("90")["settings"], []any{"71", 0}) || graph["72"] != nil || graph["70"] != nil {
						t.Fatal("streaming settings or bounded output regressed")
					}
				}
				if mode == "carousel" && (!reflect.DeepEqual(inputs("90")["images"], []any{"72", 0}) || rtx && !reflect.DeepEqual(inputs("72")["images"], []any{"70", 0})) {
					t.Fatal("photo processing must run RTX before NR before output")
				}
				if mode != "carousel" && !reflect.DeepEqual(inputs("90")["audio"], base["90"].(map[string]any)["inputs"].(map[string]any)["audio"]) {
					t.Fatal("audio wiring changed")
				}
				if issues := validateComfyPrompt(schema, graph); len(issues) > 0 {
					t.Fatalf("schema issues: %+v", issues)
				}
				if !slices.Contains(generationJobDependencies(in, nil), "dlss5") {
					t.Fatal("missing dependency")
				}
			})
		}
	}
}

func TestAIHubDLSSValidation(t *testing.T) {
	for _, strength := range []float64{0, 0.09, 1.01, math.NaN(), math.Inf(1)} {
		in := aiHubTestInput(t)
		in.AIHubDLSSEnabled = true
		in.AIHubDLSSStrength = strength
		if validateAIHubDLSS(in) == nil {
			t.Fatalf("accepted strength %v", strength)
		}
	}
	in := aiHubTestInput(t)
	in.AIHubDLSSEnabled = true
	in.AIHubHalfResolution = false
	in.Width, in.Height, in.VideoDurationSeconds = 1920, 1088, 30
	if validateAIHubDLSS(in) == nil {
		t.Fatal("oversized batch accepted")
	}
	in.Width, in.Height = 512, 768
	if err := validateAIHubDLSS(in); err != nil {
		t.Fatal(err)
	}
	in.Width = 32
	if validateAIHubDLSS(in) == nil {
		t.Fatal("undersized frame accepted")
	}
	in.AIHubDLSSEnabled = false
	if err := validateAIHubDLSS(in); err != nil {
		t.Fatal("disabled DLSS imposes limits", err)
	}
}

func TestAIHubDLSSFormRoundTrip(t *testing.T) {
	form := url.Values{
		"template_id": {"minimax-h3-video"}, "generation_workflow": {aiHubWorkflowID},
		"model_family": {modelFamilyMiniMaxH3}, "model_name": {"MiniMax/10Eros_Max_h3_TURBO-hybrid_beta5_int8.safetensors"},
		"positive_prompt": {"A toy car on a desk."}, "input_image": {"photo.png"},
		"aihub_mode": {"photo"}, "aihub_dlss_enabled": {"true"}, "aihub_dlss_strength": {"0.55"},
	}
	request := httptest.NewRequest("POST", "/", nil)
	request.Form = form
	input, err := parseGenerationForm(request)
	if err != nil {
		t.Fatal(err)
	}
	if !input.AIHubDLSSEnabled || input.AIHubDLSSStrength != 0.55 {
		t.Fatal("DLSS parameters lost")
	}
	saved := generationJobValues(form, 42)
	request.Form = generationJobFormValues(saved)
	replayed, err := parseGenerationForm(request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.AIHubDLSSEnabled || replayed.AIHubDLSSStrength != input.AIHubDLSSStrength {
		t.Fatal("saved DLSS parameters lost")
	}
}
