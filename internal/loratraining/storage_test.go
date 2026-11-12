package loratraining

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func storageInventoryFixture() StorageInventory {
	return StorageInventory{ProtocolVersion: 1, CheckedAt: time.Now().UTC(), Complete: true, TotalItems: 1, Items: []StorageItem{{ID: "training_cache:one", Kind: "training_cache", Label: "cache", State: "present", CanCleanup: true, SizeBytes: 42, Files: 1, Version: strings.Repeat("a", 64)}}}
}

func TestStorageInventoryClientRejectsUnprovenObservations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*StorageInventory)
	}{
		{"wrong protocol", func(r *StorageInventory) { r.ProtocolVersion = 0 }},
		{"negative bytes", func(r *StorageInventory) { r.Items[0].SizeBytes = -1 }},
		{"missing credited", func(r *StorageInventory) { r.Items[0].State = "missing" }},
		{"unavailable complete", func(r *StorageInventory) { r.Items[0].State = "unavailable" }},
		{"original selectable", func(r *StorageInventory) { r.Items[0].ID = "original_model:one"; r.Items[0].Kind = "original_model" }},
		{"protected selectable", func(r *StorageInventory) { r.Items[0].ProtectedReason = "active_job" }},
		{"duplicate", func(r *StorageInventory) { r.Items = append(r.Items, r.Items[0]); r.TotalItems = 2 }},
		{"unknown kind", func(r *StorageInventory) { r.Items[0].Kind = "other" }},
		{"arbitrary path", func(r *StorageInventory) { r.Items[0].ID = "training_cache:../one" }},
		{"missing version", func(r *StorageInventory) { r.Items[0].Version = "" }},
		{"wrong cursor", func(r *StorageInventory) { r.NextCursor = "training_cache:other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := storageInventoryFixture()
			tc.change(&response)
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(response) }))
			defer host.Close()
			base, _ := url.Parse(host.URL)
			got, err := NewClient(base, strings.Repeat("x", 32)).StorageInventory(context.Background(), "", 100)
			if err == nil || len(got.Items) != 0 {
				t.Fatal(got, err)
			}
		})
	}
}

func TestStorageClientDoesNotRedirectOrFallBackToDeletion(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusNotFound} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			deletions := 0
			calls := 0
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == "/v1/storage/cleanup" {
					deletions++
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Location", "/v1/storage/cleanup")
				w.WriteHeader(code)
			}))
			defer host.Close()
			base, _ := url.Parse(host.URL)
			client := NewClient(base, strings.Repeat("x", 32))
			if _, err := client.PreviewStorageCleanup(context.Background(), []string{"training_cache:one"}); err == nil {
				t.Fatal("accepted unsupported preview")
			}
			if _, err := client.StorageCleanupResult(context.Background(), strings.Repeat("a", 32)); err == nil {
				t.Fatal("accepted missing operation")
			}
			if deletions != 0 || calls != 2 {
				t.Fatal(calls, deletions)
			}
		})
	}
	var missing *Client
	if _, err := missing.StorageInventory(context.Background(), "", 100); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestStorageCleanupClientVerifiesRequestAndByteAccounting(t *testing.T) {
	request, hash, err := CanonicalStorageCleanupRequest(StorageCleanupRequest{ID: strings.Repeat("b", 32), Items: []StorageSelection{{ID: "training_cache:one", Version: strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "wrong operation", "wrong selection", "negative bytes", "missing bytes", "partial without files", "trailing JSON"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC()
			result := StorageCleanupResult{ProtocolVersion: 1, ID: request.ID, RequestHash: hash, State: "completed", StartedAt: now, FinishedAt: &now, Items: []StorageCleanupOutcome{{ID: request.Items[0].ID, Kind: "training_cache", Label: "cache", Version: request.Items[0].Version, Outcome: "deleted", FreedBytes: 42, DeletedFiles: 1}}}
			switch mode {
			case "wrong operation":
				result.ID = strings.Repeat("c", 32)
			case "wrong selection":
				result.Items[0].ID = "training_cache:other"
			case "negative bytes":
				result.Items[0].FreedBytes = -1
			case "missing bytes":
				result.Items[0].Outcome = "missing"
			case "partial without files":
				result.State = "partial"
				result.Items[0].Outcome = "partial"
				result.Items[0].DeletedFiles = 0
			}
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/storage/cleanup" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
					t.Error(r.Method, r.URL, r.Header)
				}
				_ = json.NewEncoder(w).Encode(result)
				if mode == "trailing JSON" {
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer host.Close()
			base, _ := url.Parse(host.URL)
			got, err := NewClient(base, strings.Repeat("x", 32)).CleanupStorage(context.Background(), request)
			if mode == "valid" {
				if err != nil || got.Items[0].FreedBytes != 42 {
					t.Fatal(got, err)
				}
			} else if err == nil || len(got.Items) != 0 {
				t.Fatal(got, err)
			}
		})
	}
}
