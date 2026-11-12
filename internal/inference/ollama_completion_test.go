package inference

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestOllamaCompletionBarrier(t *testing.T) {
	for _, separator := range []string{"\n", "\r\n"} {
		t.Run(strings.ReplaceAll(separator, "\n", "LF"), func(t *testing.T) {
			text := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}" + separator + separator
			end := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}" + separator + separator
			usage := "data: {\"choices\":[],\"usage\":{\"completion_tokens\":7}}" + separator + separator
			done := "data: [DONE]" + separator + separator
			var visible bytes.Buffer
			tail, err := copyOllamaCompletion(&visible, strings.NewReader(text+end+usage+done))
			if err != nil || visible.String() != text || string(tail) != end+usage+done {
				t.Fatalf("completion escaped barrier: %v visible=%q tail=%q", err, visible.String(), tail)
			}
		})
	}
}

func TestOllamaCompletionRejectsIncompleteAndUnboundedStreams(t *testing.T) {
	for _, stream := range []string{
		"data: {\"choices\":[{\"finish_reason\":null}]}\n\n",
		"data: " + strings.Repeat("x", maxOllamaCompletionTail),
		"data: [DONE]\n\n" + "data: " + strings.Repeat("x", maxOllamaCompletionTail-8) + "\n\n",
	} {
		if tail, err := copyOllamaCompletion(io.Discard, strings.NewReader(stream)); err == nil || tail != nil {
			t.Fatal("incomplete or unbounded stream accepted")
		}
	}
}
