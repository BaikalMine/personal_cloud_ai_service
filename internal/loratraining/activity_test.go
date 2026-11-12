package loratraining

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestActivityContract(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		fail       bool
	}{
		{"idle", `{"protocol_version":1,"state":"idle"}`, 200, false},
		{"busy", `{"protocol_version":1,"state":"busy","pending_submissions":1}`, 200, false},
		{"uncertain", `{"protocol_version":1,"state":"uncertain","unconfirmed_jobs":1}`, 200, false},
		{"false idle", `{"protocol_version":1,"state":"idle","active_jobs":1}`, 200, true},
		{"negative", `{"protocol_version":1,"state":"idle","pending_submissions":-1}`, 200, true},
		{"old", `{}`, 404, true},
		{"missing version", `{"state":"idle"}`, 200, true},
		{"unavailable", `{}`, 503, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/activity" {
					t.Errorf("unexpected probe: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			_, err := NewClient(endpoint, "activity-test-token-at-least-32-characters").Activity(context.Background())
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
