package inference

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
)

func TestPartialPrompt(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`{"prompt":"A green vase`, "A green vase"},
		{`{"references":[],"prompt":"Hello\\world`, `Hello\world`},
		{`{"prompt":"Hello\nworld","references":[]}`, "Hello\nworld"},
		{`{"prompt":"Hello\u00`, "Hello"},
		{`{"prompt":"Hello\`, "Hello"},
		{`{"references":[{"prompt":"secret"}]`, ""},
		{`A green ceramic vase`, "A green ceramic vase"},
		{"```json\n{\"prompt\":\"A green vase", "A green vase"},
		{`<think>private reasoning`, ""},
		{`<thi`, ""},
		{`<think>private reasoning</think>A green vase`, "A green vase"},
	} {
		if got := partialPrompt(tc.input); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.input, got, tc.want)
		}
	}
}

func TestConsumeChatStream(t *testing.T) {
	stream := `{"message":{"thinking":"private reasoning"},"done":false}
{"message":{"content":"{\"prompt\":\"A green"},"done":false}
{"message":{"content":" vase.\",\"references\":[]}"},"done":false}
{"message":{"content":""},"done":true,"eval_count":12,"prompt_eval_count":34,"eval_duration":2000000000,"total_duration":3000000000}`
	var snapshots []AssistantProgress
	body, err := consumeChatStream(strings.NewReader(stream), func(p AssistantProgress) { snapshots = append(snapshots, p) })
	if err != nil {
		t.Fatal(err)
	}
	if snapshots[0].Phase != "thinking" || snapshots[1].Prompt != "A green" || snapshots[2].Prompt != "A green vase." {
		t.Fatalf("bad progress: %+v", snapshots)
	}
	last := snapshots[len(snapshots)-1]
	if last.Phase != "finishing" || last.Usage.CompletionTokens != 12 || last.Usage.PromptTokens != 34 || last.Usage.CompletionTimeMS != 2000 {
		t.Fatalf("bad metrics: %+v", last)
	}
	if strings.Contains(string(body), "private reasoning") {
		t.Fatal("thinking leaked")
	}
	var response struct {
		Message struct{ Content string }
		Done    bool
	}
	if err = json.Unmarshal(body, &response); err != nil || !response.Done || response.Message.Content != `{"prompt":"A green vase.","references":[]}` {
		t.Fatalf("bad assembled response: %s (%v)", body, err)
	}
}

func TestConsumeChatStreamRejectsIncomplete(t *testing.T) {
	for _, stream := range []string{``, `{"done":false}`, `{"done":true`, `{"error":"failed"}`, `{"done":true}{"done":true}`, strings.Repeat(" ", int(maxAssistantStreamBytes)+1)} {
		if _, err := consumeChatStream(strings.NewReader(stream), func(AssistantProgress) {}); err == nil {
			t.Fatal("invalid stream accepted")
		}
	}
}

func TestStreamRequestKeepsSavedBody(t *testing.T) {
	saved := []byte(`{"model":"fixture","stream":false,"options":{"num_predict":2400}}`)
	body, err := streamRequest(saved)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"stream":true`) || !strings.Contains(string(saved), `"stream":false`) {
		t.Fatal("stream transformation changed saved request")
	}
}

func TestOwnedStreamRequiresPhysicalStop(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name                         string
		stopped, dispatched, invalid bool
		want                         string
	}{
		{"complete", true, true, false, "executor_terminal"},
		{"no stop proof", false, true, false, ""},
		{"cancelled stream", true, true, true, "executor_stopped"},
		{"startup failure", true, false, true, "request_not_dispatched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &ollamaRuntimeFixture{handle: func(ctx context.Context, request modelruntime.HTTPRequest, consume modelruntime.HTTPConsumer) (modelruntime.Result, error) {
				if !strings.Contains(string(request.Body), `"stream":true`) {
					t.Fatal("not streaming")
				}
				result := modelruntime.Result{Status: 200, Stopped: tc.stopped, Dispatched: tc.dispatched}
				if tc.invalid {
					return result, errors.New("cancelled or unavailable")
				}
				err := consume(200, http.Header{}, strings.NewReader(`{"message":{"content":"{\"prompt\":\"A vase\"}"},"done":true}`))
				return result, err
			}}
			worker := &Worker{store: f.store, cipher: f.cipher, owned: &ownedExecution{runtime: runtime, executor: ownedIdentity(1)}}
			worker.owned.ready.Store(true)
			_, body, code := worker.executeStreaming(context.Background(), domain.GPUInference{}, testRequest)
			if code != tc.want || code != "executor_terminal" && len(body) > 0 {
				t.Fatalf("incorrect completion %q %s", code, body)
			}
			if !tc.stopped && worker.ownedReady() {
				t.Fatal("unconfirmed stop retained ready state")
			}
		})
	}
}
