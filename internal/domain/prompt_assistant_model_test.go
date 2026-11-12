package domain

import (
	"strings"
	"testing"
)

func TestPromptAssistantModelID(t *testing.T) {
	for _, id := range []string{"a", "gemma-31b", "qwen-27b", strings.Repeat("a", 64)} {
		if !ValidPromptAssistantModelID(id) {
			t.Errorf("valid ID rejected: %q", id)
		}
	}
	for _, id := range []string{"", "Gemma", "1model", "../model", "model:tag", "a b", "a\n", "a\x00", "модель", strings.Repeat("a", 65)} {
		if ValidPromptAssistantModelID(id) {
			t.Errorf("invalid ID accepted: %q", id)
		}
	}
}
