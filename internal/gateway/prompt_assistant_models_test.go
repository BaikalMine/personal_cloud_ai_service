package gateway

import (
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/promptassistant"
)

func TestAssistantCatalogMatchesReferenceModality(t *testing.T) {
	a := &App{cfg: Config{PromptAssistantModels: []domain.PromptAssistantModel{{ID: "approved", Model: "text", VisionModel: "vision"}}}}
	for _, item := range []struct {
		kind, model string
		allowed     bool
	}{{"", "text", true}, {"audio", "text", true}, {"video", "text", true}, {"image", "vision", true}, {"image", "text", false}, {"", "other", false}} {
		snapshot := promptAssistantJobSnapshot{ModelID: "approved", Request: promptassistant.PreparedEnhancement{Model: item.model}}
		if item.kind != "" {
			snapshot.Request.References = []promptassistant.ReferenceUnderstanding{{Kind: item.kind}}
		}
		if a.assistantSnapshotMatchesCatalog(snapshot) != item.allowed {
			t.Fatal(item)
		}
	}
}
