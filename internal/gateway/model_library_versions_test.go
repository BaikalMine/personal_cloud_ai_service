package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestModelLibraryVersionsExplainsCatalogAndProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, message string
		upstream, status    int
	}{
		{"checkpoint in loras", `{"error":"Model type mismatch. Expected LORA, LoCon, or DORA, got Checkpoint"}`, "выберите «Модель»", 400, 422},
		{"lora in models", `{"error":"Model type mismatch. Expected Checkpoint, got LORA"}`, "выберите «LoRA»", 400, 422},
		{"unsupported", `{"error":"Model type mismatch. Expected LORA, got TextualInversion"}`, "тип ресурса CivitAI не поддерживается", 400, 422},
		{"missing", `private-token`, "Модель не найдена или недоступна", 404, 404},
		{"rate limited", `private-token`, "ограничил частоту запросов", 429, 429},
		{"unavailable", `private-token`, "LoRA Manager недоступен", 500, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := "loras"
			if strings.Contains(tc.message, "выберите «LoRA»") {
				kind = "checkpoints"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/lm/"+kind+"/civitai/versions/42" {
					t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.upstream)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			app := &App{cfg: Config{ComfyUIUpstream: base}}
			r := httptest.NewRequest(http.MethodGet, "/models/api/versions?kind="+kind+"&id=42", nil)
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 42, Role: "admin"}))
			w := httptest.NewRecorder()
			app.handleModelLibraryAPI(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.message) || strings.Contains(w.Body.String(), "private-token") || strings.Contains(w.Body.String(), "Model type mismatch") {
				t.Fatalf("response = %d %q; want %d containing %q", w.Code, w.Body.String(), tc.status, tc.message)
			}
		})
	}
}

func TestModelLibraryVersionsRejectsInvalidCatalogBeforeCallingManager(t *testing.T) {
	app := &App{}
	r := httptest.NewRequest(http.MethodGet, "/models/api/versions?kind=unknown&id=42", nil)
	r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 42, Role: "admin"}))
	w := httptest.NewRecorder()
	app.handleModelLibraryAPI(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}
