package gateway

import "testing"

func TestSensitiveGenerationDetection(t *testing.T) {
	for _, input := range []generationForm{
		{Positive: "cinematic adult portrait", AssistantTemplate: "nsfw"},
		{Positive: "an explicit erotic portrait"},
		{Positive: "обнажённая художественная фигура в студии"},
	} {
		if !isSensitiveGeneration(input) {
			t.Fatalf("expected sensitive generation for %#v", input)
		}
	}
	if isSensitiveGeneration(generationForm{Positive: "a person walking through a rainy city at night"}) {
		t.Fatal("ordinary prompt was marked sensitive")
	}
}
