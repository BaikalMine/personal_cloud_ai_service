package gateway

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRewriteComfyUploadAddsUserNamespaceAndPreservesFile(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("image", "sample.png")
	if err != nil {
		t.Fatal(err)
	}
	payload := append(testPNG(t), bytes.Repeat([]byte{0x42}, maxComfyUploadField+4096)...)
	if _, err := file.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("subfolder", "projects/demo"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	user := &User{ID: 42}
	request := httptest.NewRequest(http.MethodPost, "/upload/image", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	if err := app.rewriteComfyUpload(response, request, user); err != nil {
		t.Fatal(err)
	}

	parts := readMultipartValues(t, request)
	wantNamespace := comfyUploadNamespace(app.comfyClientID(user.ID)) + "/projects/demo"
	if got := string(parts["subfolder"]); got != wantNamespace {
		t.Fatalf("subfolder = %q, want %q", got, wantNamespace)
	}
	if !bytes.Equal(parts["image"], payload) {
		t.Fatalf("image payload changed: got %d bytes, want %d", len(parts["image"]), len(payload))
	}
}

func TestRewriteComfyUploadAddsMissingSubfolder(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("image", "sample.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(testPNG(t))
	_ = writer.Close()

	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	user := &User{ID: 7}
	request := httptest.NewRequest(http.MethodPost, "/upload/image", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if err := app.rewriteComfyUpload(httptest.NewRecorder(), request, user); err != nil {
		t.Fatal(err)
	}
	parts := readMultipartValues(t, request)
	if got, want := string(parts["subfolder"]), comfyUploadNamespace(app.comfyClientID(user.ID)); got != want {
		t.Fatalf("subfolder = %q, want %q", got, want)
	}
}

func TestValidateComfyUploadPayloadRejectsDecodedImageBomb(t *testing.T) {
	payload := make([]byte, 30)
	copy(payload[0:4], "RIFF")
	copy(payload[8:12], "WEBP")
	copy(payload[12:16], "VP8X")
	widthMinusOne := 9_999
	heightMinusOne := 9_999
	payload[24], payload[25], payload[26] = byte(widthMinusOne), byte(widthMinusOne>>8), byte(widthMinusOne>>16)
	payload[27], payload[28], payload[29] = byte(heightMinusOne), byte(heightMinusOne>>8), byte(heightMinusOne>>16)
	if err := validateComfyUploadPayload("large.webp", payload); err == nil {
		t.Fatal("oversized decoded image was accepted")
	}
}

func TestValidateComfyUploadPayloadAllowsNonImageAudio(t *testing.T) {
	if err := validateComfyUploadPayload("reference.mp3", []byte("ID3\x04\x00\x00audio")); err != nil {
		t.Fatalf("audio payload was rejected: %v", err)
	}
}

func TestValidateComfyUploadPayloadKindAndVideoSignature(t *testing.T) {
	mp4 := make([]byte, 16)
	copy(mp4[4:8], "ftyp")
	if err := validateComfyUploadPayloadForKind("reference.mp4", mp4, "video"); err != nil {
		t.Fatalf("MP4 video payload was rejected: %v", err)
	}
	if err := validateComfyUploadPayloadForKind("reference.mp4", mp4, "image"); err == nil {
		t.Fatal("video payload was accepted by the image endpoint")
	}
	if err := validateComfyUploadPayloadForKind("reference.mp3", []byte("ID3\x04\x00\x00audio"), "video"); err == nil {
		t.Fatal("audio payload was accepted by the video endpoint")
	}
}

func TestValidateComfyUploadPayloadRejectsUnknownFile(t *testing.T) {
	if err := validateComfyUploadPayload("payload.bin", []byte("not a supported media file")); err == nil {
		t.Fatal("unknown upload payload was accepted")
	}
	if err := validateComfyUploadPayload("fake.mp3", []byte("not really an mp3")); err == nil {
		t.Fatal("spoofed audio payload was accepted")
	}
}

func TestComfyStoredUploadFilenamePreservesSafeExtension(t *testing.T) {
	if got := comfyStoredUploadFilename("reservation", "Photo.PNG"); got != "gateway-reservation.png" {
		t.Fatalf("stored filename = %q", got)
	}
	if got := comfyStoredUploadFilename("reservation", "unsafe.bad-extension!"); got != "gateway-reservation.bin" {
		t.Fatalf("unsafe stored filename = %q", got)
	}
}

func TestValidateComfyUploadReferenceRejectsAnotherNamespace(t *testing.T) {
	own := "gateway/gateway-111111111111111111111111"
	foreign := []byte(`{"filename":"private.png","subfolder":"gateway/gateway-222222222222222222222222","type":"input"}`)
	if err := validateComfyUploadReference(foreign, own); !errors.Is(err, errForeignComfyAsset) {
		t.Fatalf("foreign reference error = %v", err)
	}
	legacy := []byte(`{"filename":"legacy.png","subfolder":"legacy","type":"input"}`)
	if err := validateComfyUploadReference(legacy, own); !errors.Is(err, errForeignComfyAsset) {
		t.Fatalf("legacy reference error = %v", err)
	}
}

func TestComfyNamespaceOwnership(t *testing.T) {
	own := "gateway/gateway-111111111111111111111111"
	for _, subfolder := range []string{own, own + "/nested"} {
		if namespaced, owned := comfyNamespaceOwnership(subfolder, own); !namespaced || !owned {
			t.Fatalf("own namespace %q = namespaced:%v owned:%v", subfolder, namespaced, owned)
		}
	}
	if namespaced, owned := comfyNamespaceOwnership("gateway/gateway-222222222222222222222222", own); !namespaced || owned {
		t.Fatalf("foreign namespace = namespaced:%v owned:%v", namespaced, owned)
	}
	if namespaced, owned := comfyNamespaceOwnership("legacy", own); namespaced || owned {
		t.Fatalf("legacy namespace = namespaced:%v owned:%v", namespaced, owned)
	}
	if namespaced, owned := comfyNamespaceOwnership(own+"/nested/../gateway/gateway-222222222222222222222222", own); !namespaced || owned {
		t.Fatalf("noncanonical namespace = namespaced:%v owned:%v", namespaced, owned)
	}
}

func readMultipartValues(t *testing.T, request *http.Request) map[string][]byte {
	t.Helper()
	defer request.Body.Close()
	reader, err := request.MultipartReader()
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string][]byte)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		values[part.FormName()] = payload
	}
	return values
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var body bytes.Buffer
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&body, canvas); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}
