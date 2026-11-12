package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/modelmanager"
	"ai-access-gateway/internal/updates"
)

func TestInstalledModelRelativePath(t *testing.T) {
	download := modelmanager.Download{Root: "C:/ComfyUI/models/diffusion_models", RelativePath: "QwenImage2.1"}
	hash := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, file, hash, want string
	}{
		{"renamed", "C:/ComfyUI/models/diffusion_models/QwenImage2.1/model-aaaa.safetensors", hash, "QwenImage2.1/model-aaaa.safetensors"},
		{"windows slashes", "C:\\ComfyUI\\models\\diffusion_models\\QwenImage2.1\\model-aaaa.safetensors", hash, "QwenImage2.1/model-aaaa.safetensors"},
		{"wrong root", "C:/Other/models/diffusion_models/QwenImage2.1/model-aaaa.safetensors", hash, ""},
		{"nested folder", "C:/ComfyUI/models/diffusion_models/QwenImage2.1/Civitai-12/model-aaaa.safetensors", hash, ""},
		{"wrong hash", "C:/ComfyUI/models/diffusion_models/QwenImage2.1/model-aaaa.safetensors", strings.Repeat("b", 64), ""},
		{"path traversal", "C:/ComfyUI/models/diffusion_models/QwenImage2.1/..\\model-aaaa.safetensors", hash, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := installedModelRelativePath(modelmanager.Item{Path: tc.file, Hash: tc.hash}, download, hash)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("relative=%q ok=%v, want %q", got, ok, tc.want)
			}
		})
	}
}

func TestVerifyRenamedModel(t *testing.T) {
	hash := strings.Repeat("2", 64)
	lm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/lm/loras/list" || r.URL.Query().Get("search") != "fixture" {
			t.Errorf("unexpected catalog request %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(modelmanager.Catalog{Items: []modelmanager.Item{
			{Path: "C:/ComfyUI/models/loras/Krea2/fixture-2222.safetensors", Hash: hash},
		}, Pages: 1})
	}))
	defer lm.Close()
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in updates.ModelVerifyRequest
		if r.URL.Path != "/v1/model-verify" || json.NewDecoder(r.Body).Decode(&in) != nil || in.RelativePath != "Krea2/fixture-2222.safetensors" || in.SHA256 != hash {
			t.Errorf("unexpected verification request: %+v", in)
		}
		_ = json.NewEncoder(w).Encode(updates.ModelVerification{Protocol: 1, State: "verified"})
	}))
	defer agent.Close()
	lmURL, _ := url.Parse(lm.URL)
	agentURL, _ := url.Parse(agent.URL)
	a := &App{cfg: Config{ComfyUIUpstream: lmURL}, updates: updates.NewClient(agentURL, strings.Repeat("t", 32))}
	result, found := a.verifyRenamedModel(context.Background(), "loras", "fixture.safetensors", hash, modelmanager.Download{Root: "C:/ComfyUI/models/loras", RelativePath: "Krea2"})
	if !found || result.State != "verified" {
		t.Fatalf("renamed file not verified: %+v found=%v", result, found)
	}
}
