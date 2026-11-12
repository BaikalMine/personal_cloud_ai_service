package inference

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLongAssistantStreamRetainsLargePromptAndContext(t *testing.T) {
	prompt := strings.Repeat("\U0001f3a5", 20000)
	content, _ := json.Marshal(map[string]any{"prompt": prompt, "references": []any{}})
	chunk, _ := json.Marshal(map[string]any{"message": map[string]string{"content": string(content)}, "done": false})
	stream := string(chunk) + "\n" + `{"done":true,"message":{"content":""},"eval_count":10000}`
	var final AssistantProgress
	body, err := consumeChatStream(strings.NewReader(stream), func(progress AssistantProgress) { final = progress })
	if err != nil || final.Prompt != prompt || final.Usage.CompletionTokens != 10000 || len(body) <= 64<<10 {
		t.Fatal("large assistant stream was truncated", err)
	}
	request, err := streamRequest([]byte(`{"model":"fixture","stream":false,"options":{"num_predict":19456,"num_ctx":131072}}`))
	if err != nil || !strings.Contains(string(request), `"num_ctx":131072`) || !strings.Contains(string(request), `"num_predict":19456`) {
		t.Fatal("streaming changed the frozen long-video budget", err)
	}
}
