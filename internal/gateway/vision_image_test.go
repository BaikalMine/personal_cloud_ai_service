package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"ai-access-gateway/internal/promptassistant"
)

func TestPrepareVisionReferenceDownscalesAndReencodes(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 2048, 1024))
	for y := range 1024 {
		for x := range 2048 {
			source.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: 255})
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, source); err != nil {
		t.Fatal(err)
	}
	prepared, mimeType, changed, err := prepareVisionReference(input.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || mimeType != "image/jpeg" {
		t.Fatalf("prepared reference changed=%t mime=%q", changed, mimeType)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(prepared))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 1536 || config.Height != 768 {
		t.Fatalf("prepared dimensions = %dx%d", config.Width, config.Height)
	}
}

func TestPrepareVisionReferenceKeepsSmallPayload(t *testing.T) {
	payload := testPNG(t)
	prepared, mimeType, changed, err := prepareVisionReference(payload, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if changed || mimeType != "image/png" || !bytes.Equal(prepared, payload) {
		t.Fatal("small supported image should pass through unchanged")
	}
}

func TestPrepareVisionReferenceRejectsHugeDecodedImage(t *testing.T) {
	payload := visionWebPHeader(9000, 8000)
	if _, _, _, err := prepareVisionReference(payload, "image/webp"); !errors.Is(err, errVisionReferenceSize) {
		t.Fatalf("oversized decoded vision reference error = %v", err)
	}
}

func visionWebPHeader(width, height int) []byte {
	payload := make([]byte, 30)
	copy(payload[0:4], "RIFF")
	payload[4] = byte(len(payload) - 8)
	copy(payload[8:12], "WEBP")
	copy(payload[12:16], "VP8X")
	payload[16] = 10
	widthMinusOne := width - 1
	heightMinusOne := height - 1
	payload[24], payload[25], payload[26] = byte(widthMinusOne), byte(widthMinusOne>>8), byte(widthMinusOne>>16)
	payload[27], payload[28], payload[29] = byte(heightMinusOne), byte(heightMinusOne>>8), byte(heightMinusOne>>16)
	return payload
}

func TestPrepareVisionReferenceUsesUploadDimensionLimits(t *testing.T) {
	for _, size := range [][2]int{{5602, 6680}, {8000, 8000}, {16384, 1}} {
		payload := visionWebPHeader(size[0], size[1])
		if err := validateComfyUploadPayloadForKind("fixture.webp", payload, "image"); err != nil {
			t.Fatalf("valid upload dimensions %v: %v", size, err)
		}
		// The header-only fixture is not decodable, but must pass the size guard.
		if _, _, _, err := prepareVisionReference(payload, "image/webp"); !errors.Is(err, errVisionReferenceDecode) {
			t.Fatalf("allowed upload dimensions %v failed size guard: %v", size, err)
		}
	}
	for _, size := range [][2]int{{8001, 8000}, {16385, 1}} {
		payload := visionWebPHeader(size[0], size[1])
		if err := validateComfyUploadPayloadForKind("fixture.webp", payload, "image"); err == nil {
			t.Fatalf("oversized upload accepted: %v", size)
		}
		if _, _, _, err := prepareVisionReference(payload, "image/webp"); !errors.Is(err, errVisionReferenceSize) {
			t.Fatalf("oversized assistant dimensions %v: %v", size, err)
		}
	}
}

func TestPromptAssistantImageReferencesLargeEditingInput(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewGray(image.Rect(0, 0, 5602, 6680))); err != nil {
		t.Fatal(err)
	}
	payload := encoded.Bytes()
	if filename := os.Getenv("VISION_REFERENCE_TEST_FILE"); filename != "" {
		var err error
		payload, err = os.ReadFile(filename)
		if err != nil {
			t.Fatal("read opt-in image fixture")
		}
	}
	sourceHash := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/view" || r.URL.Query().Get("type") != "input" {
			t.Errorf("unexpected image fetch endpoint")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	app := &App{cfg: Config{ComfyUIUpstream: endpoint, SessionSecret: "fixture"}}
	request := httptest.NewRequest(http.MethodPost, "/generate/prompt-assistant/jobs", nil)
	request.Form = url.Values{
		"input_image":  {comfyUploadNamespace(app.comfyClientID(1)) + "/fixture.png"},
		"image_role_1": {"base_scene"}, "generation_workflow": {"qwen-image21-edit"},
	}
	if _, _, err := app.fetchGenerationInputImage(context.Background(), request.Form.Get("input_image")); err != nil {
		t.Fatalf("fetch fixture: %v", err)
	}
	refs, err := app.promptAssistantImageReferences(context.Background(), 1, request, promptassistant.ModeImageToImage)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePromptAssistantImages(refs)
	if len(refs) != 1 || refs[0].Number != 1 || refs[0].MIMEType != "image/jpeg" {
		t.Fatalf("unexpected prepared reference metadata")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(refs[0].Image))
	if err != nil || config.Width != 1288 || config.Height != 1536 {
		t.Fatalf("prepared dimensions = %dx%d, err = %v", config.Width, config.Height, err)
	}
	if sha256.Sum256(payload) != sourceHash {
		t.Fatal("source fixture changed")
	}
}

func TestPromptAssistantImagePreparationErrorExplainsCause(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{errVisionReferenceSize, "64 Мп"},
		{errVisionReferenceFormat, "формат не поддерживается"},
		{errVisionReferenceDecode, "файл повреждён"},
	} {
		message := promptAssistantImagePreparationError(2, test.err).Error()
		if !strings.Contains(message, "изображение 2") || !strings.Contains(message, test.want) {
			t.Fatalf("unexpected preparation error: %q", message)
		}
	}
}

type visionLarge16BitFixture struct{}

func (visionLarge16BitFixture) ColorModel() color.Model { return color.NRGBA64Model }
func (visionLarge16BitFixture) Bounds() image.Rectangle { return image.Rect(0, 0, 8000, 8000) }
func (visionLarge16BitFixture) At(x, y int) color.Color {
	return color.NRGBA64{R: 1000, G: 2000, B: 3000, A: 65534}
}

func TestPrepareVisionReferenceMaximum16BitInput(t *testing.T) {
	if os.Getenv("VISION_REFERENCE_MAXIMUM_TEST") != "1" {
		t.Skip("opt-in 64 MP memory test")
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, visionLarge16BitFixture{}); err != nil {
		t.Fatal(err)
	}
	prepared, mime, changed, err := prepareVisionReference(encoded.Bytes(), "image/png")
	if err != nil || !changed || mime != "image/jpeg" {
		t.Fatalf("maximum reference changed=%t mime=%q err=%v", changed, mime, err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(prepared))
	if err != nil || config.Width != 1536 || config.Height != 1536 {
		t.Fatalf("maximum prepared dimensions = %dx%d, err = %v", config.Width, config.Height, err)
	}
}
