package modelmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestCheckUpdateFreshnessAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		available, stale, failed bool
		status                   int
		want                     error
	}{
		{"available", true, false, false, 200, nil},
		{"current", false, false, false, 200, nil},
		{"stale", true, true, false, 200, ErrUnavailable},
		{"cancelled", false, false, true, 200, ErrUnavailable},
		{"rate limited", false, false, false, 429, ErrRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/lm/loras/updates/refresh":
					if r.Method != "POST" {
						t.Error("refresh must be explicit POST")
					}
					var body struct {
						IDs   []int64 `json:"modelIds"`
						Force bool    `json:"force"`
					}
					if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.IDs) != 1 || body.IDs[0] != 42 || !body.Force {
						t.Error("unbounded or incorrect update request")
					}
					w.WriteHeader(tc.status)
					json.NewEncoder(w).Encode(map[string]any{"success": !tc.failed, "records": []any{}})
				case "/api/lm/loras/updates/status/42":
					checked := time.Now().Unix()
					if tc.stale {
						checked -= 3600
					}
					json.NewEncoder(w).Encode(map[string]any{"success": true, "record": map[string]any{"modelId": 42, "hasUpdate": tc.available, "lastCheckedAt": checked}})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			got, err := (&Client{Base: base}).CheckUpdate(context.Background(), "loras", 42)
			if err != tc.want || err == nil && got != tc.available {
				t.Fatalf("got %v %v", got, err)
			}
		})
	}
	for _, input := range []struct {
		kind string
		id   int64
	}{{"../settings", 42}, {"loras", 0}, {"checkpoints", -1}} {
		if _, err := (&Client{}).CheckUpdate(context.Background(), input.kind, input.id); err != ErrInvalid {
			t.Fatal("invalid input reached upstream")
		}
	}
}

func TestListUpdateFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("base_model") || r.URL.Query().Has("search") {
			t.Error("empty filters must be omitted, not interpreted as exact empty matches")
		}
		if r.URL.Query().Get("update_available_only") != "true" || r.URL.Query().Get("page_size") != "24" {
			t.Error("filter or pagination lost")
		}
		w.Write([]byte(`{"items":[],"page":1}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	if _, err := (&Client{Base: base}).ListFiltered(context.Background(), "loras", "", "", 1, true); err != nil {
		t.Fatal(err)
	}
}

func TestListPreservesNonEmptyFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("base_model") != "Flux.1 D" || q.Get("search") != "studio light" || q.Has("update_available_only") {
			t.Error("explicit filters changed")
		}
		w.Write([]byte(`{"items":[],"page":1}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	if _, err := (&Client{Base: base}).List(context.Background(), "loras", "studio light", "Flux.1 D", 1); err != nil {
		t.Fatal(err)
	}
}
