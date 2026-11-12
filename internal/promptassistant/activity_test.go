package promptassistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLoadedModelsAdmissionProbe(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		status, count int
		fail          bool
	}{
		{"empty", `{"models":[]}`, 200, 0, false},
		{"loaded", `{"models":[{"name":"vision"}]}`, 200, 1, false},
		{"missing", `{}`, 200, 0, true},
		{"null", `{"models":null}`, 200, 0, true},
		{"invalid", `{"models":`, 200, 0, true},
		{"proxy", `{"models":[]}`, 504, 0, true},
		{"oversize", strings.Repeat(" ", maxResponseBytes) + `{"models":[]}`, 200, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/ps" {
					t.Errorf("unexpected probe: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			count, err := NewClient(endpoint, "fixture").LoadedModels(context.Background())
			if (err != nil) != tc.fail || count != tc.count {
				t.Fatalf("count=%d err=%v", count, err)
			}
		})
	}
}
