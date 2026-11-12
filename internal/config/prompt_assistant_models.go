package config

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
)

func parsePromptAssistantModels(raw string) ([]domain.PromptAssistantModel, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > 16<<10 {
		return nil, fmt.Errorf("PROMPT_ASSISTANT_MODELS exceeds 16 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var models []domain.PromptAssistantModel
	if err := decoder.Decode(&models); err != nil {
		return nil, fmt.Errorf("PROMPT_ASSISTANT_MODELS must be a JSON array: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || models == nil || len(models) > 16 {
		return nil, fmt.Errorf("PROMPT_ASSISTANT_MODELS must contain at most 16 model choices")
	}
	seen := map[string]bool{}
	for _, model := range models {
		if !domain.ValidPromptAssistantModelID(model.ID) || seen[model.ID] ||
			!validAssistantCatalogText(model.Label, 80, true) || !validAssistantCatalogText(model.Model, 256, false) ||
			(model.VisionModel != "" && !validAssistantCatalogText(model.VisionModel, 256, false)) ||
			(model.TimeoutSeconds != 0 && (model.TimeoutSeconds < 15 || model.TimeoutSeconds > 900)) {
			return nil, fmt.Errorf("PROMPT_ASSISTANT_MODELS contains an invalid or duplicate choice")
		}
		seen[model.ID] = true
	}
	return models, nil
}

func validAssistantCatalogText(value string, limit int, spaces bool) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit || strings.TrimSpace(value) != value {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) || (!spaces && unicode.IsSpace(c)) {
			return false
		}
	}
	return true
}
