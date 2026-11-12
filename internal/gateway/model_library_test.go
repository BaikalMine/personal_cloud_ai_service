package gateway

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
)

func TestModelLibraryReadOnly(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, admin := range []bool{false, true} {
		var output bytes.Buffer
		role := "user"
		if admin {
			role = "admin"
		}
		err = templates.ExecuteTemplate(&output, "model_library", map[string]any{"CurrentUser": &User{ID: 42, Role: role}, "CanManageModels": admin, "CSRF": "test"})
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"data-open-install", "data-open-settings", "data-confirm", "data-jobs", "data-check-update", "data-updates-only", "data-delete-lora", "data-delete-dialog", "data-refresh-metadata", "data-check-all"} {
			if strings.Contains(output.String(), action) != admin {
				t.Errorf("admin=%v action=%s", admin, action)
			}
		}
		if !strings.Contains(output.String(), "data-catalog") {
			t.Fatal("catalog missing")
		}
	}
	a := &App{}
	for _, path := range []string{"settings", "versions", "quote", "installs", "cancel", "close-request", "check-update", "delete-preview", "delete-lora", "refresh-metadata"} {
		for _, method := range []string{"GET", "POST"} {
			req := httptest.NewRequest(method, "/models/api/"+path, strings.NewReader(`{}`))
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &User{ID: 42, Role: "user"}))
			w := httptest.NewRecorder()
			a.handleModelLibraryAPI(w, req)
			if w.Code != 403 {
				t.Fatalf("%s %s = %d", method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/api/lm/settings", "/api/lm/download-model-get", "/api/lm/loras/delete"} {
		if !isComfyAdminOnlyPath(path) {
			t.Fatalf("proxy bypass %s", path)
		}
	}
}
func TestModelLibraryTokenScope(t *testing.T) {
	cipher, _ := contentcrypto.NewCipher(strings.Repeat("s", 32))
	a := &App{contentCipher: cipher}
	token, err := a.modelToken(modelLibraryToken{Scope: "item", Owner: 42, Expires: time.Now().Add(time.Hour).Unix(), Path: "C:/private/model.safetensors"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, "private") {
		t.Fatal("path exposed")
	}
	if _, err = a.readModelToken(token, "item", 42); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		scope string
		owner int64
	}{{"install", 42}, {"item", 43}} {
		if _, err = a.readModelToken(token, tc.scope, tc.owner); err == nil {
			t.Fatal("token crossed scope")
		}
	}
	if _, err = a.readModelToken(token+"x", "item", 42); err == nil {
		t.Fatal("tampered token accepted")
	}
}
