package updateagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-access-gateway/internal/updates"
)

func TestModelVerification(t *testing.T) {
	working := t.TempDir()
	root := filepath.Join(working, "models", "loras")
	relative := "Flux2/Civitai-42/model.safetensors"
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("model file fixture")
	digest := sha256.Sum256(data)
	in := updates.ModelVerifyRequest{Root: root, RelativePath: relative, SHA256: hex.EncodeToString(digest[:])}
	check := func(want string) {
		t.Helper()
		out, err := verifyModelFile(context.Background(), working, in)
		if err != nil || out.State != want {
			t.Fatalf("want %s got %+v %v", want, out, err)
		}
	}
	check("missing")
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	check("verified")
	in.SHA256 = strings.Repeat("0", 64)
	check("mismatch")
	in.SHA256 = hex.EncodeToString(digest[:])
	for _, suffix := range []string{".part", ".aria2"} {
		if err := os.WriteFile(target+suffix, nil, 0600); err != nil {
			t.Fatal(err)
		}
		check("incomplete")
		if err := os.Remove(target + suffix); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := verifyModelFile(ctx, working, in); err == nil {
		t.Fatal("cancelled verification continued")
	}
	for _, bad := range []string{"../model.safetensors", "/model.safetensors", "Flux2/../model.safetensors", "Flux2\\model.safetensors", "Flux2/model.safetensors:secret", "Flux2/model.bin"} {
		copy := in
		copy.RelativePath = bad
		if _, err := verifyModelFile(context.Background(), working, copy); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	outside := t.TempDir()
	external := filepath.Join(outside, "model.safetensors")
	if err := os.WriteFile(external, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err == nil {
		copy := in
		copy.RelativePath = "escape/model.safetensors"
		if _, err = verifyModelFile(context.Background(), working, copy); err == nil {
			t.Fatal("followed link outside model root")
		}
	}
}

func TestModelVerificationAuthenticatedAPI(t *testing.T) {
	working := t.TempDir()
	root := filepath.Join(working, "models", "diffusion_models")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("verified diffusion fixture")
	hash := sha256.Sum256(data)
	if err := os.WriteFile(filepath.Join(root, "model.safetensors"), data, 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 32)
	s, err := NewServer(token, &fakeController{}, ComfyTarget{WorkingDirectory: working})
	if err != nil {
		t.Fatal(err)
	}
	input := updates.ModelVerifyRequest{Root: root, RelativePath: "model.safetensors", SHA256: hex.EncodeToString(hash[:])}
	raw, _ := json.Marshal(input)
	for _, tc := range []struct {
		method, key string
		want        int
	}{{"POST", "", 401}, {"POST", "wrong", 401}, {"GET", token, 405}} {
		r := httptest.NewRequest(tc.method, "/v1/model-verify", bytes.NewReader(raw))
		if tc.key != "" {
			r.Header.Set("Authorization", "Bearer "+tc.key)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("status %d, want %d", w.Code, tc.want)
		}
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := updates.NewClient(base, token)
	result, err := client.VerifyModel(context.Background(), input)
	if err != nil || result.State != "verified" || result.Size != int64(len(data)) {
		t.Fatal(result, err)
	}
	s.modelVerifyMu.Lock()
	_, err = client.VerifyModel(context.Background(), input)
	s.modelVerifyMu.Unlock()
	if err == nil {
		t.Fatal("concurrent verification was not bounded")
	}
}
