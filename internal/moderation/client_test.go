package moderation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestClassifyImageUsesLocalScoreThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/classify" || r.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("unexpected request: %s %s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte(`{"nsfw_score":0.15}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	sensitive, err := NewClient(base).ClassifyImage(context.Background(), []byte("image"), "image/png")
	if err != nil || !sensitive {
		t.Fatalf("sensitive=%v err=%v", sensitive, err)
	}
}
