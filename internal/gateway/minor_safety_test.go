package gateway

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateGenerationPromptRejectsSexualizedMinorContent(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "english", text: "explicit nude schoolgirl portrait", want: true},
		{name: "russian", text: "эротический портрет несовершеннолетней", want: true},
		{name: "age", text: "nude person, 16 years old", want: true},
		{name: "teenager", text: "explicit nude teenager", want: true},
		{name: "minor plural", text: "sexualized content involving minors", want: true},
		{name: "safe child scene", text: "a child playing football in a park"},
		{name: "adult content", text: "editorial portrait of an adult woman in lingerie"},
		{name: "fifteen second adult content", text: "a clearly adult woman in a continuous fifteen-second nude boudoir shot"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateGenerationPrompt(test.text)
			if got := errors.Is(err, errMinorSexualContent); got != test.want {
				t.Fatalf("validateGenerationPrompt(%q) blocked=%v, want %v (err=%v)", test.text, got, test.want, err)
			}
		})
	}
}

func TestEnforceComfyPromptSafetyRejectsQueuedPromptAndRestoresBody(t *testing.T) {
	body := []byte(`{"prompt":{"7":{"class_type":"CLIPTextEncode","inputs":{"text":"pornographic teen character"}}}}`)
	request := httptest.NewRequest(http.MethodPost, "/prompt", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	app := &App{}
	if err := app.enforceComfyPromptSafety(request); !errors.Is(err, errMinorSexualContent) {
		t.Fatalf("enforceComfyPromptSafety() error = %v, want minor safety block", err)
	}
	if got := request.ContentLength; got != int64(len(body)) {
		t.Fatalf("ContentLength = %d, want %d", got, len(body))
	}
}

func TestEnforceComfyPromptSafetyAllowsOrdinaryPrompt(t *testing.T) {
	body := []byte(`{"prompt":{"7":{"class_type":"CLIPTextEncode","inputs":{"text":"a child playing football in a park"}}}}`)
	request := httptest.NewRequest(http.MethodPost, "/prompt", bytes.NewReader(body))
	if err := (&App{}).enforceComfyPromptSafety(request); err != nil {
		t.Fatalf("enforceComfyPromptSafety() error = %v", err)
	}
}
