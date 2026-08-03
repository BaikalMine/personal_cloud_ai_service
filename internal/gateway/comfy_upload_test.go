package gateway

import (
	"bytes"
	"errors"
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
	payload := bytes.Repeat([]byte{0x42}, maxComfyUploadField+4096)
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
	_, _ = file.Write([]byte("image"))
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

func TestValidateComfyUploadReferenceRejectsAnotherNamespace(t *testing.T) {
	own := "gateway/gateway-111111111111111111111111"
	foreign := []byte(`{"filename":"private.png","subfolder":"gateway/gateway-222222222222222222222222","type":"input"}`)
	if err := validateComfyUploadReference(foreign, own); !errors.Is(err, errForeignComfyAsset) {
		t.Fatalf("foreign reference error = %v", err)
	}
	legacy := []byte(`{"filename":"legacy.png","subfolder":"legacy","type":"input"}`)
	if err := validateComfyUploadReference(legacy, own); err != nil {
		t.Fatalf("legacy reference was rejected: %v", err)
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
}

func readMultipartValues(t *testing.T, request *http.Request) map[string][]byte {
	t.Helper()
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
