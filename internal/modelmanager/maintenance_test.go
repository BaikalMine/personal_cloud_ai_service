package modelmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestLoraRelativePath(t *testing.T) {
	for _, tc := range []struct {
		file  string
		valid bool
	}{
		{"C:/Comfy/models/loras/Flux2/v1/model.safetensors", true},
		{"c:\\Comfy\\models\\loras\\model.safetensors", true},
		{"C:/Comfy/models/loras-other/model.safetensors", false},
		{"C:/Comfy/models/loras/../model.safetensors", false},
		{"C:/Comfy/models/loras/a/../../model.safetensors", false},
		{"C:/Comfy/models/loras/file:stream.safetensors", false},
		{"C:/Comfy/models/loras/dir./model.safetensors", false},
		{"C:/Comfy/models/loras/model.ckpt", false},
		{"D:/Comfy/models/loras/model.safetensors", false},
	} {
		_, err := LoraRelativePath("C:/Comfy/models/loras", tc.file)
		if (err == nil) != tc.valid {
			t.Errorf("%s: %v", tc.file, err)
		}
	}
}
func TestMaintenanceTargetsOneFileAndDoesNotRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" {
			t.Error("mutation must be POST")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["file_path"] != "C:/Comfy/models/loras/one.safetensors" {
			t.Error("unexpected file selection")
		}
		if r.URL.Path == "/api/lm/loras/delete" {
			http.Error(w, "private upstream details", 500)
			return
		}
		if r.URL.Path != "/api/lm/loras/fetch-civitai" {
			t.Error("unexpected operation")
		}
		w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := Client{Base: base}
	if err := c.RefreshMetadata(context.Background(), "loras", "C:/Comfy/models/loras/one.safetensors"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteLora(context.Background(), "C:/Comfy/models/loras/one.safetensors"); err != ErrUnavailable {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("operation retried")
	}
}
