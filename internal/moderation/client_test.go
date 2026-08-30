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

func TestClassifyImageThresholdBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		score     string
		sensitive bool
	}{
		{name: "below", score: "0.0249", sensitive: false},
		{name: "boundary", score: "0.025", sensitive: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"nsfw_score":` + test.score + `}`))
			}))
			defer server.Close()
			base, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := NewClient(base).ClassifyImage(context.Background(), []byte("image"), "image/png")
			if err != nil {
				t.Fatal(err)
			}
			if actual != test.sensitive {
				t.Fatalf("sensitive=%v want=%v", actual, test.sensitive)
			}
		})
	}
}
