package moderation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestMediaDetectorAndInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body      string
		want, wantError bool
	}{
		{"local_detector", `{"nsfw_score":0.001,"sensitive":true}`, true, false},
		{"classifier_still_wins", `{"nsfw_score":0.3,"sensitive":false}`, true, false},
		{"both_safe", `{"nsfw_score":0.001,"sensitive":false}`, false, false},
		{"missing_score", `{}`, false, true},
		{"null_score", `{"nsfw_score":null}`, false, true},
		{"invalid_score", `{"nsfw_score":2}`, false, true},
		{"invalid_detector", `{"nsfw_score":0,"sensitive":"false"}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Content-Type") != "video/mp4" {
					t.Error("video content type lost")
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			u, _ := url.Parse(s.URL)
			got, err := NewClient(u).ClassifyMedia(context.Background(), []byte("video fixture"), "video/mp4")
			if (err != nil) != tc.wantError || got != tc.want {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}
