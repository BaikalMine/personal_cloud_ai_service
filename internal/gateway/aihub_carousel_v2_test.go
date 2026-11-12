package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func aiHubV2TestSchema(t *testing.T) map[string]comfyNodeInfo {
	t.Helper()
	body, err := os.ReadFile("testdata/aihub_v2_object_info.json")
	if err != nil {
		t.Fatal(err)
	}
	return decodeTestObjectInfo(t, string(body))
}

func TestAIHubCarouselV2SequentialRefinement(t *testing.T) {
	info := aiHubV2TestSchema(t)
	for _, source := range []string{"video", "prompt"} {
		t.Run(source, func(t *testing.T) {
			in := aiHubTestInput(t)
			in.ModelName = "MiniMax\\10Eros_Max_h3_TURBO-hybrid_beta5_int8.safetensors"
			in.TextEncoder = "MiniMax\\qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors"
			in.VAE = "MiniMax\\minimax_h3_video_vae_fp16.safetensors"
			in.AudioVAE = "MiniMax\\minimax_h3_audio_vae_fp32.safetensors"
			in.AIHubMode, in.AIHubCarouselPipeline, in.AIHubCarouselSource = "carousel", "v2", source
			in.AIHubPhotoCount, in.AIHubPhotoRandomness = 2, 0
			if source == "prompt" {
				in.InputVideo = ""
			}
			if err := normalizeAIHub(&in); err != nil {
				t.Fatal(err)
			}
			if err := resolveAIHubModels(&in, info); err != nil {
				t.Fatal(err)
			}
			graph, err := buildAIHubPrompt(in)
			if err != nil {
				t.Fatal(err)
			}
			if issues := validateComfyPrompt(buildComfySchemaCatalog(info, time.Time{}, "v2"), graph); len(issues) > 0 {
				t.Fatalf("schema: %+v", issues)
			}
			for _, id := range []string{"61", "64", "65", "66", "100", "200"} {
				if graph[id] != nil {
					t.Fatal("legacy/implicit branch", id)
				}
			}
			inputs := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
			if inputs("304")["batch_size"] != 1 || inputs("315")["steps"] != 8 || inputs("315")["cfg"] != 1.0 || inputs("315")["scheduler"] != "simple" {
				t.Fatal("refinement diverges from V2")
			}
			if !reflect.DeepEqual(inputs("317")["image_ref"], []any{"304", 0}) {
				t.Fatal("color reference lost")
			}
			if inputs("318")["grain_intensity"] != 0.006 {
				t.Fatal("wrong grain")
			}
			if source == "prompt" && graph["20"] != nil {
				t.Fatal("prompt source loads video")
			}
			if graph["306"].(map[string]any)["class_type"] != "CLIPLoaderGGUF" {
				t.Fatal("existing GGUF encoder not used")
			}
		})
	}
}

func TestAIHubCarouselV2DoesNotAcceptVideoPostprocessing(t *testing.T) {
	for _, enable := range []func(*generationForm){func(in *generationForm) { in.VideoRTXEnabled = true }, func(in *generationForm) { in.AIHubDLSSEnabled = true }, func(in *generationForm) { in.AIHubFGEnabled = true }} {
		in := aiHubTestInput(t)
		in.AIHubMode = "carousel"
		in.AIHubCarouselPipeline = "v2"
		in.AIHubCarouselSource = "video"
		enable(&in)
		if normalizeAIHub(&in) == nil {
			t.Fatal("video postprocessing accepted")
		}
	}
}

func TestAIHubCarouselV2QwenRefinement(t *testing.T) {
	info := aiHubV2TestSchema(t)
	delete(info, "CLIPLoaderGGUF")
	delete(info, "ReferenceLatent")
	delete(info, "EmptyFlux2LatentImage")
	for _, source := range []string{"video", "prompt"} {
		t.Run(source, func(t *testing.T) {
			in := aiHubTestInput(t)
			for _, name := range []*string{&in.ModelName, &in.TextEncoder, &in.VAE, &in.AudioVAE} {
				*name = strings.ReplaceAll(*name, "/", "\\")
			}
			in.AIHubMode, in.AIHubCarouselPipeline, in.AIHubCarouselSource = "carousel", "v2", source
			in.AIHubCarouselRefiner = "qwen21"
			in.AIHubPhotoCount = 2
			if source == "prompt" {
				in.InputVideo = ""
			}
			if err := normalizeAIHub(&in); err != nil {
				t.Fatal(err)
			}
			if err := resolveAIHubModels(&in, info); err != nil {
				t.Fatal(err)
			}
			if in.AIHubFluxModel != "" || !strings.Contains(in.AIHubQwenModel, "int8_convrot") {
				t.Fatal("Qwen branch depends on FLUX or selected the wrong profile")
			}
			graph, err := buildAIHubPrompt(in)
			if err != nil {
				t.Fatal(err)
			}
			if issues := validateComfyPrompt(buildComfySchemaCatalog(info, time.Time{}, "qwen-refiner"), graph); len(issues) > 0 {
				t.Fatalf("schema: %+v", issues)
			}
			inputs := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
			if inputs("308")["resolution"] != 0 || inputs("315")["steps"] != 25 || !reflect.DeepEqual(inputs("315")["latent_image"], []any{"308", 2}) {
				t.Fatal("native Qwen reference geometry or latent lost")
			}
			if !reflect.DeepEqual(inputs("321")["width"], []any{"320", 0}) || !reflect.DeepEqual(inputs("317")["image_target"], []any{"321", 0}) {
				t.Fatal("exact output dimensions not restored")
			}
			for _, node := range graph {
				kind := node.(map[string]any)["class_type"]
				if kind == "ReferenceLatent" || kind == "EmptyFlux2LatentImage" {
					t.Fatal("FLUX node leaked into Qwen")
				}
			}
		})
	}
	delete(info, "TextEncodeQwenImage21")
	in := aiHubTestInput(t)
	in.AIHubCarouselRefiner = "qwen21"
	if err := resolveAIHubCarouselV2Models(&in, info); err == nil {
		t.Fatal("missing Qwen node accepted")
	}
}

func TestAIHubCarouselV2RejectsUnknownRefiner(t *testing.T) {
	in := aiHubTestInput(t)
	in.AIHubMode, in.AIHubCarouselPipeline, in.AIHubCarouselRefiner = "carousel", "v2", "unknown"
	if normalizeAIHub(&in) == nil {
		t.Fatal("unknown refiner accepted")
	}
}

func TestAIHubCarouselV2Export(t *testing.T) {
	dir := os.Getenv("AIHUB_V2_EXPORT")
	if dir == "" {
		t.Skip("explicit candidate export only")
	}
	info := aiHubV2TestSchema(t)
	for _, source := range []string{"video", "prompt"} {
		in := aiHubTestInput(t)
		for _, name := range []*string{&in.ModelName, &in.TextEncoder, &in.VAE, &in.AudioVAE} {
			*name = strings.ReplaceAll(*name, "/", "\\")
		}
		in.Width = 352
		in.Height = 640
		in.Seed = 424242
		in.AIHubMode = "carousel"
		in.AIHubCarouselPipeline = "v2"
		in.AIHubCarouselSource = source
		in.AIHubPhotoCount = 2
		in.AIHubPhotoRandomness = 0
		in.InputImage = "aihub-v2-test-portrait.png"
		in.InputVideo = "aihub-v2-test-reference.mp4"
		in.Positive = "The clearly adult woman from <Picture 1> wears a green blazer and white blouse. A medium portrait shot in a bright room. She smiles naturally, turns slightly toward the window, then looks at the camera. Preserve her face, hair and clothing. No speech."
		if source == "prompt" {
			in.InputVideo = ""
		} else {
			in.Positive = "<Picture 1> supplies the clearly adult woman's identity. <Video 1> supplies the scene, framing and gentle waving action. Preserve natural movement and the original clothing. A photorealistic continuous shot."
		}
		if err := normalizeAIHub(&in); err != nil {
			t.Fatal(err)
		}
		if err := resolveAIHubModels(&in, info); err != nil {
			t.Fatal(err)
		}
		graph, err := buildAIHubPrompt(in)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.MarshalIndent(graph, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, source+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
