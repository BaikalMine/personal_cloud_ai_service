package gateway

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
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
	payload := make([]byte, 30)
	copy(payload[0:4], "RIFF")
	copy(payload[8:12], "WEBP")
	copy(payload[12:16], "VP8X")
	widthMinusOne := 7_999
	heightMinusOne := 7_999
	payload[24], payload[25], payload[26] = byte(widthMinusOne), byte(widthMinusOne>>8), byte(widthMinusOne>>16)
	payload[27], payload[28], payload[29] = byte(heightMinusOne), byte(heightMinusOne>>8), byte(heightMinusOne>>16)
	if _, _, _, err := prepareVisionReference(payload, "image/webp"); err == nil {
		t.Fatal("oversized decoded vision reference was accepted")
	}
}
