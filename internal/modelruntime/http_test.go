package modelruntime

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOwnedHTTPRequestContract(t *testing.T) {
	consume := func(int, http.Header, io.Reader) error { return nil }
	valid := HTTPRequest{Method: http.MethodPost, Path: "/api/chat", Body: []byte(`{"model":"fixture","stream":true}`), ResponseLimit: 1024}
	for _, tc := range []struct {
		name, method, path string
		body               []byte
		limit              int64
		ok                 bool
	}{
		{"native chat", "POST", "/api/chat", valid.Body, 1024, true},
		{"native generate", "POST", "/api/generate", valid.Body, 1024, true},
		{"OpenAI stream", "POST", "/v1/chat/completions", valid.Body, 1024, true},
		{"metadata", "GET", "/api/tags?fixture=1", nil, 1024, true},
		{"head", "HEAD", "/api/version", nil, 1024, true},
		{"delete", "DELETE", "/api/delete", valid.Body, 1024, true},
		{"foreign authority", "POST", "http://127.0.0.1:1/api/chat", valid.Body, 1024, false},
		{"network relative", "POST", "//127.0.0.1/api/chat", valid.Body, 1024, false},
		{"relative", "POST", "api/chat", valid.Body, 1024, false},
		{"fragment", "POST", "/api/chat#fragment", valid.Body, 1024, false},
		{"backslash", "POST", "/api\\chat", valid.Body, 1024, false},
		{"header injection", "POST", "/api/chat\r\nHost: other", valid.Body, 1024, false},
		{"tunnel", "CONNECT", "/api/chat", valid.Body, 1024, false},
		{"get body", "GET", "/api/tags", valid.Body, 1024, false},
		{"zero limit", "POST", "/api/chat", valid.Body, 0, false},
		{"huge limit", "POST", "/api/chat", valid.Body, MaxHTTPResponseBytes + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHTTPRequest(HTTPRequest{Method: tc.method, Path: tc.path, Body: tc.body, ResponseLimit: tc.limit}, consume)
			if (err == nil) != tc.ok {
				t.Fatalf("validation: %v", err)
			}
		})
	}
	if err := validateHTTPRequest(valid, nil); !errors.Is(err, ErrHTTPInput) {
		t.Fatal("nil consumer accepted", err)
	}
}

func TestOwnedHTTPResponseConsumption(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		limit      int64
		want       error
	}{
		{"empty", "", 1, nil},
		{"exact limit", "1234", 4, nil},
		{"overflow", "12345", 4, ErrHTTPResponseLimit},
		{"native stream", "{\"done\":false}\n{\"done\":true}\n", 1024, nil},
		{"SSE", "data: {\"choices\":[]}\n\ndata: [DONE]\n\n", 1024, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{StatusCode: 429, Header: http.Header{"Content-Type": {"application/x-ndjson"}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			var received bytes.Buffer
			err := consumeHTTPResponse(response, tc.limit, func(status int, header http.Header, reader io.Reader) error {
				if status != 429 || header.Get("Content-Type") != "application/x-ndjson" {
					t.Fatal("envelope changed")
				}
				header.Set("X-Fixture", "changed")
				_, err := io.Copy(&received, reader)
				return err
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("consume=%v want=%v", err, tc.want)
			}
			if tc.want == nil && received.String() != tc.body {
				t.Fatal("stream changed")
			}
			if int64(received.Len()) > tc.limit {
				t.Fatal("response budget exceeded")
			}
			if response.Header.Get("X-Fixture") != "" {
				t.Fatal("consumer changed transport headers")
			}
		})
	}
	response := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("body"))}
	if err := consumeHTTPResponse(response, 10, func(int, http.Header, io.Reader) error { return nil }); !errors.Is(err, ErrHTTPResponseIncomplete) {
		t.Fatal("early consumer success accepted", err)
	}
	response.Body = io.NopCloser(strings.NewReader("body"))
	if err := consumeHTTPResponse(response, 1, func(_ int, _ http.Header, r io.Reader) error { _, _ = io.Copy(io.Discard, r); return nil }); !errors.Is(err, ErrHTTPResponseLimit) {
		t.Fatal("consumer swallowed limit error", err)
	}
}
