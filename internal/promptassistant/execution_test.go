package promptassistant

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestInferenceExecutionEvidence(t *testing.T) {
	for _, kind := range []string{"assistant", "caption"} {
		for _, tc := range []struct {
			name   string
			status int
			body   string
			want   ExecutionOutcome
		}{
			{"done", 200, `{"done":true,"message":{"content":"{\"prompt\":\"A landscape in daylight\",\"caption\":\"sample, landscape in daylight\",\"references\":[]}"}}`, ExecutionCompleted},
			{"done_empty_content", 200, `{"done":true,"message":{"content":""}}`, ExecutionCompleted},
			{"not_done", 200, `{"done":false,"message":{"content":"partial answer"}}`, ExecutionUnconfirmed},
			{"missing_done", 200, `{"message":{"content":"partial answer"}}`, ExecutionUnconfirmed},
			{"proxy_timeout", 504, `{"done":true}`, ExecutionUnconfirmed},
			{"rejected", 400, `{"error":"not accepted"}`, ExecutionUnconfirmed},
			{"truncated_json", 200, `{"done":true,`, ExecutionUnconfirmed},
			{"oversized", 200, `{"done":true,"padding":"` + strings.Repeat("x", maxResponseBytes) + `"}`, ExecutionUnconfirmed},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				base, _ := url.Parse(server.URL)
				client := NewClient(base, "test")
				outcome := runExecutionTestCall(context.Background(), client, kind)
				if outcome != tc.want || outcome.Settled() != (tc.want != ExecutionUnconfirmed) {
					t.Fatalf("outcome=%v settled=%v want=%v", outcome, outcome.Settled(), tc.want)
				}
			})
		}
		t.Run(kind+"/cancelled_transport", func(t *testing.T) {
			base, _ := url.Parse("http://unreachable.invalid")
			client := NewClient(base, "test")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if got := runExecutionTestCall(ctx, client, kind); got != ExecutionUnconfirmed {
				t.Fatalf("cancelled transport returned %v", got)
			}
		})
	}
	client := NewClient(nil, "")
	if result, err := client.EnhanceResult(context.Background(), ModeTextToImage, ProfileWorkflowDefault, "", nil, false); err == nil || result.Execution != ExecutionNotDispatched {
		t.Fatalf("local validation execution=%v err=%v", result.Execution, err)
	}
	if result, err := client.CaptionImage(context.Background(), "", "character", nil, "image/png"); err == nil || result.Execution != ExecutionNotDispatched {
		t.Fatalf("local caption validation execution=%v err=%v", result.Execution, err)
	}
}

func runExecutionTestCall(ctx context.Context, client *Client, kind string) ExecutionOutcome {
	if kind == "caption" {
		result, _ := client.CaptionImage(ctx, "sample", "character", []byte{1}, "image/png")
		return result.Execution
	}
	result, _ := client.EnhanceResult(ctx, ModeTextToImage, ProfileWorkflowDefault, "A landscape", nil, false)
	return result.Execution
}
