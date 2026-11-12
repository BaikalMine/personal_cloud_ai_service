package promptassistant

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestKreaUltraNegativeContextPreservesSelectedStyleAndSeparateContract(t *testing.T) {
	base, _ := url.Parse("http://localhost:11434")
	client := NewClient(base, "test:vision")
	for _, profile := range []Profile{ProfileWorkflowDefault, ProfilePhotographic, ProfileAnime} {
		prepared, err := client.PrepareEnhancementWithEditContext(ModeTextToImage, profile, "A ceramics studio", nil, VideoContext{}, EditContext{CFG: 2, NegativePrompt: "blurry lettering"}, false)
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			Messages []Message `json:"messages"`
			Format   struct {
				Required []string `json:"required"`
			} `json:"format"`
		}
		if err := json.Unmarshal(prepared.Body, &request); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(request.Messages[0].Content, "negative_prompt") || !strings.Contains(request.Messages[1].Content, `"cfg":2`) || !strings.Contains(request.Messages[1].Content, `"existing_negative_prompt":"blurry lettering"`) {
			t.Fatal("missing negative context")
		}
		found := false
		for _, name := range request.Format.Required {
			found = found || name == "negative_prompt"
		}
		if !found {
			t.Fatal("negative field is not required by structured response")
		}
	}
	if _, err := client.PrepareEnhancementWithEditContext(ModeTextToImage, ProfileAnime, "A studio", nil, VideoContext{}, EditContext{CFG: 1, NegativePrompt: "blur"}, false); err == nil {
		t.Fatal("CFG 1 accepted ineffective negative")
	}
}
