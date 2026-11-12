package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/promptassistant"
)

func TestHarnessPromptGuideIsTheActualPromptAssistantInstruction(t *testing.T) {
	base, _ := url.Parse("http://localhost:11434")
	client := promptassistant.NewClient(base, "fixture:vision")
	for _, mode := range []string{"text", "references", "identity", "control", "inpaint", "outpaint"} {
		for _, cfg := range []float64{1, 2} {
			query, _ := json.Marshal(map[string]any{"mode": mode, "cfg": cfg})
			w := httptest.NewRecorder()
			(&App{}).harnessUltraPromptGuide(w, httptest.NewRequest(http.MethodPost, "/prompt-guide", strings.NewReader(string(query))))
			if w.Code != 200 {
				t.Fatal(mode, cfg, w.Code, w.Body.String())
			}
			var guide struct {
				Mode        promptassistant.Mode    `json:"mode"`
				Profile     promptassistant.Profile `json:"profile"`
				Instruction string                  `json:"instruction"`
				Revision    string                  `json:"revision"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &guide); err != nil {
				t.Fatal(err)
			}
			prepared, err := client.PrepareEnhancementWithEditContext(guide.Mode, guide.Profile, "A ceramic teapot", nil, promptassistant.VideoContext{}, promptassistant.EditContext{CFG: cfg}, false)
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Messages []promptassistant.Message `json:"messages"`
			}
			if json.Unmarshal(prepared.Body, &request) != nil || request.Messages[0].Content != guide.Instruction || len(guide.Revision) != 64 {
				t.Fatal("Harness and the website used different prompt guides", mode, cfg)
			}
			if mode != "text" && guide.Profile != promptassistant.ProfileKrea2Edit {
				t.Fatal("the existing edit profile mapping was lost")
			}
		}
	}
	for _, body := range []string{`{"mode":"video"}`, `{"profile":"flux-edit"}`, `{"cfg":0}`, `{"references":[{"number":1,"role":"identity"}]}`, `{"mode":"identity","references":[{"number":1,"role":"unknown"}]}`, `{"host_path":"C:/secret"}`} {
		w := httptest.NewRecorder()
		(&App{}).harnessUltraPromptGuide(w, httptest.NewRequest(http.MethodPost, "/prompt-guide", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal("invalid guidance request accepted", body, w.Code)
		}
	}
}
