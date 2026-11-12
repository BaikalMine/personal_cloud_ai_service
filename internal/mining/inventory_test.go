package mining

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestInventoryRejectsUnconfirmedSnapshots(t *testing.T) {
	now := time.Now().UTC()
	state := State{Available: true, Running: true, ProcessName: "miner.exe", PIDs: []int{10}, CollectedAt: now}
	for _, tc := range []struct {
		name  string
		value Inventory
		valid bool
	}{
		{"idle", Inventory{CollectedAt: now, States: []State{}}, true},
		{"running", Inventory{CollectedAt: now, States: []State{state}}, true},
		{"missing_states", Inventory{CollectedAt: now}, false},
		{"stale", Inventory{CollectedAt: now.Add(-time.Minute), States: []State{}}, false},
		{"future", Inventory{CollectedAt: now.Add(time.Minute), States: []State{}}, false},
		{"duplicates", Inventory{CollectedAt: now, States: []State{state, state}}, false},
		{"unavailable", Inventory{CollectedAt: now, States: []State{{ProcessName: "miner.exe", CollectedAt: now}}}, false},
		{"no_pids", Inventory{CollectedAt: now, States: []State{{Available: true, Running: true, ProcessName: "miner.exe", CollectedAt: now}}}, false},
		{"stale_state", Inventory{CollectedAt: now, States: []State{{Available: true, ProcessName: "miner.exe", CollectedAt: now.Add(-time.Minute)}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("missing authentication")
				}
				_ = json.NewEncoder(w).Encode(tc.value)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			_, err := NewClient(base, "fixture").Inventory(context.Background())
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
