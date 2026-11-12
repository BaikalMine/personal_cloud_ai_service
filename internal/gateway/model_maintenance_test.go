package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/updates"
)

func TestModelRemovalPreviewAndConfirmation(t *testing.T) {
	state := "verified"
	calls, verifies := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/api/lm/settings":
			json.NewEncoder(w).Encode(map[string]any{"settings": map[string]string{"default_lora_root": "C:/models/loras"}})
		case "/v1/model-verify":
			verifies++
			var in updates.ModelVerifyRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Root != "C:/models/loras" || in.RelativePath != "family/old.safetensors" || in.SHA256 != strings.Repeat("a", 64) {
				t.Error("wrong verification target")
			}
			json.NewEncoder(w).Encode(map[string]any{"protocol": 1, "state": state, "size_bytes": 16})
		default:
			t.Errorf("unexpected mutation: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	cipher, _ := contentcrypto.NewCipher(strings.Repeat("s", 32))
	app := &App{cfg: Config{ComfyUIUpstream: base}, contentCipher: cipher, csrfSigner: security.NewCSRFSigner("test-removal"), updates: updates.NewClient(base, strings.Repeat("k", 32))}
	item := modelLibraryToken{Scope: "item", Owner: 1, Expires: time.Now().Add(time.Hour).Unix(), Kind: "loras", Path: "C:/models/loras/family/old.safetensors", SHA256: strings.Repeat("a", 64), Size: 16}
	token, _ := app.modelToken(item)
	request := func(action, token, confirm string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"token": token, "confirm": confirm})
		r := httptest.NewRequest("POST", "/models/api/"+action, strings.NewReader(string(body)))
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: "admin"}))
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "test-session"})
		r.Header.Set("X-CSRF-Token", app.csrfSigner.Token("test-session"))
		w := httptest.NewRecorder()
		app.handleModelLibraryAPI(w, r)
		return w
	}
	w := request("delete-preview", token, "")
	if w.Code != 200 {
		t.Fatalf("preview %d: %s", w.Code, w.Body.String())
	}
	var quote struct {
		Token    string `json:"token"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &quote); err != nil {
		t.Fatal(err)
	}
	if quote.Filename != "old.safetensors" || verifies != 0 {
		t.Fatal("invalid preview")
	}
	signed, err := app.readModelToken(quote.Token, "delete-lora", 1)
	if err != nil || signed.SHA256 != item.SHA256 || signed.Size != 16 || signed.Expires > time.Now().Add(121*time.Second).Unix() {
		t.Fatal("invalid deletion snapshot", err)
	}
	before := calls
	if w = request("delete-lora", token, quote.Filename); w.Code != 400 || calls != before {
		t.Fatal("item token authorized deletion")
	}
	if w = request("delete-lora", quote.Token, "other.safetensors"); w.Code != 400 {
		t.Fatal("wrong filename accepted")
	}
	if w = request("delete-lora", quote.Token, quote.Filename); w.Code != 409 {
		t.Fatal("deletion allowed without admission guard")
	}
	item.Path = "C:/models/loras/../other.safetensors"
	token, _ = app.modelToken(item)
	before = verifies
	if w = request("delete-preview", token, ""); w.Code != 409 || verifies != before {
		t.Fatal("traversal reached verifier")
	}
}

func TestModelRemovalPreviewUsesMatchingModelRoot(t *testing.T) {
	for _, tc := range []struct{ root, file string }{
		{"C:/models/checkpoints", "C:/models/checkpoints/SDXL/a.safetensors"},
		{"C:/models/diffusion_models", "C:/models/diffusion_models/MiniMax/a.safetensors"},
	} {
		t.Run(tc.root, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/lm/settings":
					json.NewEncoder(w).Encode(map[string]any{"settings": map[string]string{"default_checkpoint_root": "C:/models/checkpoints", "default_unet_root": "C:/models/diffusion_models"}})
				case "/v1/model-verify":
					var in updates.ModelVerifyRequest
					if json.NewDecoder(r.Body).Decode(&in) != nil || in.Root != tc.root || !strings.HasSuffix(in.RelativePath, "/a.safetensors") {
						t.Errorf("wrong verification target: %+v", in)
					}
					json.NewEncoder(w).Encode(map[string]any{"protocol": 1, "state": "verified", "size_bytes": 16})
				default:
					t.Errorf("unexpected mutation %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			base, _ := url.Parse(upstream.URL)
			cipher, _ := contentcrypto.NewCipher(strings.Repeat("s", 32))
			app := &App{cfg: Config{ComfyUIUpstream: base}, contentCipher: cipher, csrfSigner: security.NewCSRFSigner("test-removal"), updates: updates.NewClient(base, strings.Repeat("k", 32))}
			token, _ := app.modelToken(modelLibraryToken{Scope: "item", Owner: 1, Expires: time.Now().Add(time.Hour).Unix(), Kind: "checkpoints", Path: tc.file, SHA256: strings.Repeat("a", 64), Size: 16})
			body, _ := json.Marshal(map[string]string{"token": token})
			r := httptest.NewRequest("POST", "/models/api/delete-preview", strings.NewReader(string(body)))
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: "admin"}))
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "test-session"})
			r.Header.Set("X-CSRF-Token", app.csrfSigner.Token("test-session"))
			w := httptest.NewRecorder()
			app.handleModelLibraryAPI(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("preview failed: %d %s", w.Code, w.Body.String())
			}
			var quote struct {
				RelativePath string `json:"relative_path"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &quote); err != nil || !strings.HasSuffix(quote.RelativePath, "/a.safetensors") {
				t.Fatalf("wrong preview path: %+v, %v", quote, err)
			}
		})
	}
}
