package gateway

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestMiniMaxH3ResolvedCanvasSurvivesPhotoSettings(t *testing.T) {
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewGray(image.Rect(0, 0, 1216, 1616))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body.Bytes())
	}))
	defer server.Close()
	upstream, _ := url.Parse(server.URL)
	app := &App{cfg: Config{ComfyUIUpstream: upstream}}
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "minimax-h3-video")
	if !ok {
		t.Fatal("video workflow missing")
	}
	for _, mode := range []string{miniMaxH3FrameMode, miniMaxH3ReferenceMode} {
		for _, quality := range []int{480, 720, 1080, 1440} {
			for _, aspect := range []string{"3:4", "16:9", "custom"} {
				t.Run(fmt.Sprintf("%s/%d/%s", mode, quality, aspect), func(t *testing.T) {
					input := generationForm{ModelName: "MiniMax/base.safetensors", ReferenceModel: "MiniMax/ref.safetensors",
						TextEncoder: "MiniMax/text.safetensors", VAE: "MiniMax/video.safetensors", AudioVAE: "MiniMax/audio.safetensors",
						Positive: "A ceramic vase on a table.", InputImage: "gateway/first.png", Width: 1216, Height: 1616,
						AspectRatio: aspect, OutputMegapixels: 1.9, DimensionMultiple: 16, VideoQuality: quality,
						VideoMode: mode, VideoDurationSeconds: 5, VideoSteps: 25, Steps: 25, CFG: 1, Denoise: 1,
						Sampler: "euler", Scheduler: "simple", Seed: 42}
					if err := definition.normalizeAndValidate(&input); err != nil {
						t.Fatal(err)
					}
					if err := app.resolveMiniMaxH3ReferenceDimensions(context.Background(), &input); err != nil {
						t.Fatal(err)
					}
					width, height := input.Width, input.Height
					prompt, err := definition.buildPrompt(input)
					if err != nil {
						t.Fatal(err)
					}
					conditioning := prompt["7"].(map[string]any)["inputs"].(map[string]any)
					if conditioning["width"] != width || conditioning["height"] != height {
						t.Fatalf("photo settings replaced video canvas: %vx%v, want %dx%d", conditioning["width"], conditioning["height"], width, height)
					}
					if width%32 != 0 || height%32 != 0 || max(width, height) > quality {
						t.Fatal("invalid video canvas")
					}
					if mode == miniMaxH3FrameMode {
						resize := prompt["50"].(map[string]any)["inputs"].(map[string]any)
						if resize["custom_width"] != width || resize["custom_height"] != height {
							t.Fatal("frame and canvas differ")
						}
					}
				})
			}
		}
	}
}

func TestMiniMaxH3GraphRejectsUnalignedCanvas(t *testing.T) {
	for _, size := range [][2]int{{1216, 1616}, {1616, 1216}, {0, 704}, {512, -32}} {
		if _, err := buildMiniMaxH3Prompt(generationForm{Width: size[0], Height: size[1], VAE: "video", AudioVAE: "audio", TextEncoder: "text"}); err == nil {
			t.Fatalf("invalid canvas accepted: %v", size)
		}
	}
}
