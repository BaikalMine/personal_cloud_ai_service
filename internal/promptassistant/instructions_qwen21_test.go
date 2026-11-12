package promptassistant

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestQwen21EditInstructionsNameTenthImage(t *testing.T) {
	if !ValidProfile(ModeImageToImage, ProfileQwen21Edit) || ValidProfile(ModeTextToImage, ProfileQwen21Edit) {
		t.Fatal("Qwen edit profile must be limited to photo editing")
	}
	refs := []ImageReference{
		{Number: 1, Role: ImageReferenceBaseScene},
		{Number: 10, Role: ImageReferenceStyle},
	}
	instruction := SystemPromptWithReferences(ModeImageToImage, ProfileQwen21Edit, refs)
	for _, marker := range []string{"<image1>", "<image10>", "Inspect every supplied image"} {
		if !strings.Contains(instruction, marker) {
			t.Fatalf("Qwen assistant instruction does not contain %q", marker)
		}
	}
	if strings.Contains(instruction, "<image9>:") {
		t.Fatal("Qwen assistant invented an absent image")
	}
}

func TestQwen21EditRequiresStructuredPromptAndReferences(t *testing.T) {
	base, err := url.Parse("http://localhost:11434")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(base, "test:vision")
	client.visionModel = "test:vision"
	prepared, err := client.PrepareEnhancement(ModeImageToImage, ProfileQwen21Edit, "Change the background", nil, VideoContext{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Format struct {
			Required   []string       `json:"required"`
			Properties map[string]any `json:"properties"`
		} `json:"format"`
	}
	if err := json.Unmarshal(prepared.Body, &request); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"prompt", "references", "negative_prompt"} {
		if _, ok := request.Format.Properties[required]; !ok {
			t.Fatalf("missing schema property %s", required)
		}
		found := false
		for _, value := range request.Format.Required {
			found = found || value == required
		}
		if !found {
			t.Fatalf("schema does not require %s", required)
		}
	}
}

func TestQwen21EditKeepsNegativeSeparateAndHonorsCFG(t *testing.T) {
	base, _ := url.Parse("http://localhost:11434")
	client := NewClient(base, "test:vision").WithVisionModel("test:vision", 0, "0")
	prepared, err := client.PrepareEnhancementWithEditContext(ModeImageToImage, ProfileQwen21Edit, "Change the coat", nil, VideoContext{}, EditContext{NegativePrompt: "blurry", CFG: 2}, false)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, `"existing_negative_prompt":"blurry"`) || !strings.Contains(request.Messages[0].Content, "negative_prompt") {
		t.Fatal("Qwen edit did not keep the original negative separate from the positive request")
	}
	if _, err := client.PrepareEnhancementWithEditContext(ModeImageToImage, ProfileQwen21Edit, "Change the coat", nil, VideoContext{}, EditContext{NegativePrompt: "blurry", CFG: 1}, false); err == nil {
		t.Fatal("CFG 1 accepted a negative prompt that cannot affect generation")
	}
	parsed, err := parseModelResultWithReferences(`{"prompt":"A red coat","negative_prompt":"blurry","references":[]}`, ModeImageToImage, nil)
	if err != nil || parsed.NegativePrompt != "blurry" {
		t.Fatalf("separate negative prompt was lost: %+v, %v", parsed, err)
	}
}
