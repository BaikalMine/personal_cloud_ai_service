package updates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestInstallUsesLongRunningClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/install" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true}`))
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(baseURL, "12345678901234567890123456789012")
	client.http.Timeout = 10 * time.Millisecond
	client.install.Timeout = time.Second

	if _, err := client.Install(context.Background(), Request{Components: []string{ComponentOpenWebUI}}); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
}
