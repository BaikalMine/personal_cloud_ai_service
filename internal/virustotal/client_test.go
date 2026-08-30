package virustotal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubmitURLUsesVirusTotalAPIAndParsesAnalysis(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/urls" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("x-apikey"); got != "test-key" {
			t.Fatalf("x-apikey = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "example.com") {
			t.Fatalf("URL is absent from body %q", body)
		}
		_, _ = w.Write([]byte(`{"data":{"id":"analysis-1","attributes":{"status":"queued","stats":{"malicious":0,"suspicious":0,"harmless":1,"undetected":2,"timeout":0}}}}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL("test-key", server.URL, server.Client())
	analysis, err := client.SubmitURL(context.Background(), "https://example.com/model")
	if err != nil {
		t.Fatal(err)
	}
	if analysis.ID != "analysis-1" || analysis.Status != "queued" || analysis.Harmless != 1 {
		t.Fatalf("unexpected analysis: %#v", analysis)
	}
}

func TestClientRejectsUseWithoutAPIKey(t *testing.T) {
	client := NewClientWithBaseURL("", "https://example.invalid", nil)
	if client.Configured() {
		t.Fatal("client without key must not be configured")
	}
	if _, err := client.SubmitURL(context.Background(), "https://example.com"); err == nil {
		t.Fatal("missing API key must fail before a network call")
	}
}
