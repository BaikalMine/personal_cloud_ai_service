package gateway

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoraLibraryTemplateRendersReadOnly(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = templates.ExecuteTemplate(&output, "lora_library", map[string]any{
		"Title": "Мои LoRA", "AssetVersion": "test", "CurrentUser": &User{ID: 42}, "Message": "<script>untrusted</script>",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"data-lora-library", "data-owner=\"42\"", "/static/lora-library.js", "/static/lora-library.css", "/train-lora?new=1", "&lt;script&gt;untrusted&lt;/script&gt;"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("missing %q", expected)
		}
	}
	if strings.Contains(output.String(), "<script>untrusted</script>") || strings.Contains(output.String(), "data-lora-training-form") {
		t.Fatal("library rendered execution form or unescaped message")
	}
}

func TestLoraTrainingLibraryEntry(t *testing.T) {
	for _, tc := range []struct {
		query  string
		editor bool
	}{
		{"", false}, {"?job=training-id", false}, {"?view=datasets", false},
		{"?new=1", true}, {"?new=0", false}, {"?dataset=abc", true}, {"?version=abc", true},
		{"?dataset=", true}, {"?version=", true}, {"?run=training-id", true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			if got := loraTrainingEditorRequested(httptest.NewRequest("GET", "/train-lora"+tc.query, nil)); got != tc.editor {
				t.Fatalf("editor = %v, want %v", got, tc.editor)
			}
		})
	}
}
