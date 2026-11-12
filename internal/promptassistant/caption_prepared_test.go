package promptassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestPreparedCaptionMatchesDirectCall(t *testing.T) {
	var received []byte
	response := []byte(`{"done":true,"message":{"content":"{\"caption\":\"sample, a green vase\"}"},"eval_count":12}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received, _ = io.ReadAll(r.Body); w.Write(response) }))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := NewClient(base, "vision-before-restart")
	instruction, _ := LoraCaptionInstructionSnapshot()
	prepared, err := client.PrepareCaptionImageWithInstruction("sample", "object", []byte{1, 2, 3}, "image/png", instruction)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := client.CaptionImageWithInstruction(context.Background(), "sample", "object", []byte{1, 2, 3}, "image/png", instruction)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, prepared.Body) {
		t.Fatal("durable and direct requests differ")
	}
	metadata, err := json.Marshal(prepared)
	if err != nil || bytes.Contains(metadata, []byte("images")) || bytes.Contains(metadata, []byte("Body")) {
		t.Fatalf("image leaked into metadata: %s %v", metadata, err)
	}
	var restored PreparedCaption
	if err = json.Unmarshal(metadata, &restored); err != nil {
		t.Fatal(err)
	}
	saved, err := ParseCaptionResponse(restored, 200, response)
	if err != nil || saved.Model != direct.Model || saved.Caption != direct.Caption || saved.Execution != ExecutionCompleted || !reflect.DeepEqual(saved.Usage, direct.Usage) {
		t.Fatalf("restored parser changed result: %+v %v", saved, err)
	}
}

func TestParseCaptionResponseRejectsIncompleteOrInvalidOutput(t *testing.T) {
	tests := []struct {
		name, content, reason string
		wantError             bool
	}{
		{"json", `{"caption":"token, a portrait"}`, "stop", false},
		{"plain text fallback", "token, a portrait", "stop", false},
		{"broken json", `{"caption":"token, a portrait`, "stop", true},
		{"array instead of object", `["token, a portrait"`, "stop", true},
		{"token limit", `{"caption":"token, a portrait"}`, "length", true},
		{"too long", strings.Repeat("a", MaxLoraCaptionCharacters+1), "stop", true},
		{"unicode limit", strings.Repeat("я", MaxLoraCaptionCharacters), "stop", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"done": true, "done_reason": tc.reason, "message": map[string]string{"content": tc.content}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := ParseCaptionResponse(PreparedCaption{}, http.StatusOK, body)
			if (err != nil) != tc.wantError {
				t.Fatalf("caption=%q error=%v, want error=%v", result.Caption, err, tc.wantError)
			}
			if !tc.wantError && result.Caption != tc.content && tc.name != "json" {
				t.Fatalf("caption changed: %q", result.Caption)
			}
			if result.Execution != ExecutionCompleted {
				t.Fatalf("execution = %v", result.Execution)
			}
		})
	}
}
