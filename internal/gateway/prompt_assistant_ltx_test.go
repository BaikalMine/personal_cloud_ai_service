package gateway

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestAssistantLTXDraftAndAuditPreserveSoundtrack(t *testing.T) {
	const audio = `A woman says: "Hello." Quiet room ambience.`
	values, err := generationDraftValues(url.Values{"assistant_audio_suggestion": {audio}})
	if err != nil || values["assistant_audio_suggestion"] != audio {
		t.Fatalf("soundtrack draft lost: %v %v", values, err)
	}
	encoded, err := json.Marshal(promptAssistantAudit{AudioSuggestion: audio})
	if err != nil {
		t.Fatal(err)
	}
	var audit promptAssistantAudit
	if err := json.Unmarshal(encoded, &audit); err != nil || audit.AudioSuggestion != audio {
		t.Fatalf("soundtrack audit lost: %v", err)
	}
}
