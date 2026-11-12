package config

import (
	"strings"
	"testing"
)

func TestPromptAssistantModelCatalog(t *testing.T) {
	valid := `[{"id":"gemma-31b","label":"Gemma 31B","model":"local/gemma:31b","vision_model":"local/vision:27b","timeout_seconds":600}]`
	models, err := parsePromptAssistantModels(valid)
	if err != nil || len(models) != 1 || models[0].VisionModel != "local/vision:27b" || models[0].TimeoutSeconds != 600 {
		t.Fatal(models, err)
	}
	for _, raw := range []string{"", "[]", "  "} {
		if _, err := parsePromptAssistantModels(raw); err != nil {
			t.Fatal("empty catalog rejected", err)
		}
	}
	for _, raw := range []string{
		"null", "{}", valid + "[]", strings.Repeat(" ", 16<<10) + valid,
		strings.Replace(valid, `"id":"gemma-31b"`, `"id":"local/gemma:31b"`, 1),
		strings.Replace(valid, `"label":"Gemma 31B"`, `"label":""`, 1),
		strings.Replace(valid, `"model":"local/gemma:31b"`, `"model":"local /gemma:31b"`, 1),
		strings.Replace(valid, `"timeout_seconds":600`, `"timeout_seconds":14`, 1),
		strings.Replace(valid, `"timeout_seconds":600`, `"timeout_seconds":901`, 1),
		strings.Replace(valid, `"timeout_seconds":600`, `"unknown":600`, 1),
		"[" + strings.Repeat(valid[1:len(valid)-1]+",", 16) + valid[1:len(valid)-1] + "]",
		"[" + valid[1:len(valid)-1] + "," + valid[1:len(valid)-1] + "]",
	} {
		if _, err := parsePromptAssistantModels(raw); err == nil {
			t.Errorf("invalid catalog accepted: %.120s", raw)
		}
	}
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	t.Setenv("PROMPT_ASSISTANT_MODELS", valid)
	cfg, err := Load()
	if err != nil || len(cfg.PromptAssistantModels) != 1 || cfg.PromptAssistantModels[0].ID != "gemma-31b" {
		t.Fatal("catalog not loaded", cfg.PromptAssistantModels, err)
	}
}
