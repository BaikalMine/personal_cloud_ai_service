package promptassistant

import (
	"strings"
	"testing"
)

func TestAIHubEditingAndAudioContract(t *testing.T) {
	for _, source := range []string{"original", "ltx"} {
		v := VideoContext{AIHubMode: "video", AIHubTask: "replace_clothing", AudioSource: source, AudioReference: true, AIHubT8: true}
		text := aiHubPromptInstruction(v)
		for _, want := range []string{"replace only the clothing", "<Audio 1>", "T8 locks", "does not guarantee"} {
			if !strings.Contains(text, want) {
				t.Fatalf("missing %q: %s", want, text)
			}
		}
		if strings.Contains(text, "not supplied as <Audio 1>") {
			t.Fatal("contradictory reference instructions")
		}
		v.AudioReference = false
		if source == "ltx" && !strings.Contains(aiHubPromptInstruction(v), "not supplied as <Audio 1>") {
			t.Fatal("disabled reference lost")
		}
	}
	if AIHubTaskInstruction("reference") != "" {
		t.Fatal("legacy mode changed")
	}
}
