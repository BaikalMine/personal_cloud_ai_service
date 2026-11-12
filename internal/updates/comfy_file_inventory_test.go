package updates

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComfyFileInventoryQuery(t *testing.T) {
	for _, raw := range []string{"", "kind=input&limit=100&q=hello", "cursor=" + strings.Repeat("a", 64) + "&snapshot=" + strings.Repeat("b", 64)} {
		q, err := ParseComfyFileInventoryQuery(raw)
		if err != nil {
			t.Fatal(raw, err)
		}
		parsed, err := ParseComfyFileInventoryQuery(q.Values().Encode())
		if err != nil || parsed != q {
			t.Fatal(parsed, err)
		}
	}
	for _, raw := range []string{"kind=tmp", "limit=0", "limit=101", "limit=01", "kind=input&kind=output", "limit=+1", "offset=1", "cursor=x", "snapshot=" + strings.Repeat("a", 64), "q=%00", "q=%ff", "root=C:/ComfyUI"} {
		if _, err := ParseComfyFileInventoryQuery(raw); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func TestComfyFileInventoryClientValidation(t *testing.T) {
	query := ComfyFileInventoryQuery{Kind: "output", Limit: 50}
	baseResult := func() ComfyFileInventory {
		return ComfyFileInventory{Protocol: 1, Kind: "output", CheckedAt: time.Now().UTC(), Complete: true, Snapshot: strings.Repeat("a", 64), TotalItems: 1, TotalFiles: 1, LogicalBytes: 4, MatchedItems: 1, Items: []ComfyFileInventoryItem{{ID: ComfyFileInventoryID("output", "", "test.png"), Filename: "test.png", EntryType: "file", SizeKnown: true, SizeBytes: 4}}}
	}
	for _, mode := range []string{"ok", "partial", "foreign_id", "negative", "path", "unknown_type", "bad_total", "bad_cursor", "bad_kind", "bad_snapshot", "empty_items", "unknown_field", "trailing", "oversize", "redirect", "http_error"} {
		t.Run(mode, func(t *testing.T) {
			var redirected atomic.Int64
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer destination.Close()
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/comfy-asset/inventory" || r.URL.Query().Get("kind") != "output" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("k", 64) {
					t.Error("wrong inventory request")
				}
				result := baseResult()
				switch mode {
				case "partial":
					result.Complete = false
					result.Issue = "scan_limit"
				case "foreign_id":
					result.Items[0].ID = strings.Repeat("e", 64)
				case "negative":
					result.Items[0].SizeBytes = -1
				case "path":
					result.Items[0].Subfolder = "C:/private"
				case "unknown_type":
					result.Items[0].EntryType = "safe_to_delete"
				case "bad_total":
					result.LogicalBytes = 0
				case "bad_cursor":
					result.NextCursor = strings.Repeat("e", 64)
				case "bad_kind":
					result.Kind = "input"
				case "bad_snapshot":
					result.Snapshot = "unknown"
				case "empty_items":
					result.Items = nil
				case "redirect":
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
					return
				case "http_error":
					w.WriteHeader(503)
					return
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat(" ", maxResponseBytes+1)))
					return
				}
				payload, _ := json.Marshal(result)
				if mode == "unknown_field" {
					payload = append([]byte(`{"private_path":"hidden",`), payload[1:]...)
				}
				if mode == "trailing" {
					payload = append(payload, []byte(` {}`)...)
				}
				_, _ = w.Write(payload)
			}))
			defer host.Close()
			base, _ := url.Parse(host.URL + "/ignored?ignored=1")
			client := NewClient(base, strings.Repeat("k", 64))
			result, err := client.ComfyFileInventory(context.Background(), query)
			if (err == nil) != (mode == "ok" || mode == "partial") {
				t.Fatal(mode, result, err)
			}
			if redirected.Load() != 0 {
				t.Fatal("followed inventory redirect")
			}
		})
	}
	if _, err := (*Client)(nil).ComfyFileInventory(context.Background(), query); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestComfyFileInventoryRejectsRepeatedCursor(t *testing.T) {
	item := ComfyFileInventoryItem{ID: ComfyFileInventoryID("input", "", "a.png"), Filename: "a.png", EntryType: "file", SizeKnown: true, SizeBytes: 1}
	query := ComfyFileInventoryQuery{Kind: "input", Limit: 1, Cursor: item.ID, Snapshot: strings.Repeat("a", 64)}
	result := ComfyFileInventory{Protocol: 1, Kind: "input", CheckedAt: time.Now().UTC(), Complete: true, Snapshot: query.Snapshot, TotalItems: 2, TotalFiles: 2, LogicalBytes: 2, MatchedItems: 2, Items: []ComfyFileInventoryItem{item}}
	if ValidateComfyFileInventory(result, query) == nil {
		t.Fatal("accepted repeated cursor entry")
	}
}
