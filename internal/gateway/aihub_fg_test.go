package gateway

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestAIHubFGGraph(t *testing.T) {
	info := aiHubTestSchema(t)
	body, err := os.ReadFile("testdata/dlss_fg_object_info.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, node := range decodeTestObjectInfo(t, string(body)) {
		info[name] = node
	}
	schema := buildComfySchemaCatalog(info, time.Time{}, "fg")
	for name, node := range schema.Nodes {
		for _, key := range []string{"image", "video", "audio", "unet_name", "clip_name", "vae_name", "ckpt_name", "checkpoint", "lora_name"} {
			if field, ok := node.Required[key]; ok {
				field.Choices = nil
				node.Required[key] = field
			}
		}
		schema.Nodes[name] = node
	}
	for _, mode := range []string{"photo", "video"} {
		for _, audio := range []string{"native", "original", "ltx"} {
			if mode == "photo" && audio == "original" {
				continue
			}
			for _, multiplier := range []int{2, 3, 4} {
				for _, enhance := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%dx/enhance=%t", mode, audio, multiplier, enhance), func(t *testing.T) {
						in := aiHubTestInput(t)
						in.AIHubMode, in.AIHubAudioSource = mode, audio
						if mode == "photo" {
							in.InputVideo = ""
						}
						in.AIHubAudioPrompt = "Wind blowing softly."
						in.Width, in.Height = 256, 256
						in.AIHubDLSSEnabled, in.VideoRTXEnabled = enhance, enhance
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
						if base["73"] != nil {
							t.Fatal("FG enabled by default")
						}
						in.AIHubFGEnabled, in.AIHubFGMultiplier = true, multiplier
						graph, err := buildAIHubPrompt(in)
						if err != nil {
							t.Fatal(err)
						}
						inputs := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
						if inputs("90")["fg_multiplier"] != multiplier || graph["90"].(map[string]any)["class_type"] != "GatewayAIHubVideoStream" {
							t.Fatal("wrong output rate")
						}
						if !reflect.DeepEqual(inputs("90")["images"], []any{"15", 0}) {
							t.Fatal("FG must precede enhancement/upscale")
						}
						for _, id := range []string{"70", "72", "73", "74", "75", "76"} {
							if graph[id] != nil {
								t.Fatalf("full output batch node remains: %s", id)
							}
						}
						if inputs("90")["nr_enabled"] != enhance || inputs("90")["rtx_enabled"] != enhance {
							t.Fatal("enhancement choices lost")
						}
						if !reflect.DeepEqual(inputs("90")["audio"], base["90"].(map[string]any)["inputs"].(map[string]any)["audio"]) {
							t.Fatal("audio wiring changed")
						}
						if issues := validateComfyPrompt(schema, graph); len(issues) > 0 {
							t.Fatalf("schema issues: %+v", issues)
						}
						if !slices.Contains(generationJobDependencies(in, nil), "dlss-frame-generation") {
							t.Fatal("dependency missing")
						}
					})
				}
			}
		}
	}
}

func TestAIHubFGReferenceDuration(t *testing.T) {
	in := aiHubTestInput(t)
	in.AIHubFGEnabled, in.AIHubFGMultiplier = true, 2
	in.AIHubMatchDuration, in.AIHubOutputFrames = true, 241
	if err := normalizeAIHub(&in); err != nil {
		t.Fatal(err)
	}
	graph, err := buildAIHubPrompt(in)
	if err != nil {
		t.Fatal(err)
	}
	inputs := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
	if !reflect.DeepEqual(inputs("90")["images"], []any{"17", 0}) || inputs("90")["fg_multiplier"] != 2 {
		t.Fatal("interpolation must follow exact reference trim")
	}
	if inputs("18")["duration"] != float64(241)/24 {
		t.Fatal("audio duration changed")
	}
}

func TestAIHubFGValidation(t *testing.T) {
	for _, change := range []func(*generationForm){
		func(in *generationForm) { in.AIHubMode = "carousel" },
		func(in *generationForm) { in.AIHubFGMultiplier = 1 },
		func(in *generationForm) { in.AIHubFGMultiplier = 5 },
		func(in *generationForm) {
			in.Width, in.Height, in.VideoDurationSeconds = 1920, 1088, 30
			in.AIHubHalfResolution = false
		},
		func(in *generationForm) { in.AIHubMatchDuration, in.AIHubOutputFrames = true, 721 },
	} {
		in := aiHubTestInput(t)
		in.AIHubFGEnabled, in.AIHubFGMultiplier = true, 2
		change(&in)
		if validateAIHubFG(in) == nil {
			t.Fatalf("invalid input accepted: %+v", in)
		}
		if _, err := buildAIHubPrompt(in); err == nil {
			t.Fatal("builder bypasses validation")
		}
		in.AIHubFGEnabled = false
		if err := validateAIHubFG(in); err != nil {
			t.Fatal("disabled FG imposes limits", err)
		}
	}
}

func TestAIHubFGFormReplay(t *testing.T) {
	form := url.Values{"template_id": {"minimax-h3-video"}, "generation_workflow": {aiHubWorkflowID}, "model_family": {modelFamilyMiniMaxH3},
		"model_name": {"MiniMax/10Eros_Max_h3_TURBO-hybrid_beta5_int8.safetensors"}, "positive_prompt": {"A vase."}, "input_image": {"vase.png"},
		"aihub_mode": {"photo"}, "aihub_fg_enabled": {"true"}, "aihub_fg_multiplier": {"3"}}
	request := httptest.NewRequest("POST", "/", nil)
	request.Form = generationJobFormValues(generationJobValues(form, 42))
	input, err := parseGenerationForm(request)
	if err != nil {
		t.Fatal(err)
	}
	if !input.AIHubFGEnabled || input.AIHubFGMultiplier != 3 || input.AIHubDLSSEnabled {
		t.Fatal("independent FG choice lost")
	}
}

func TestAIHubFGNormalizesBeforeReferenceProbe(t *testing.T) {
	in := aiHubTestInput(t)
	in.AIHubFGEnabled, in.AIHubMatchDuration = true, true
	if err := normalizeAIHub(&in); err != nil {
		t.Fatal("reference cannot be probed after normalization", err)
	}
	if _, err := buildAIHubPrompt(in); err == nil {
		t.Fatal("unresolved reference reached graph dispatch")
	}
	in.AIHubOutputFrames = 240
	if _, err := buildAIHubPrompt(in); err != nil {
		t.Fatal(err)
	}
}
