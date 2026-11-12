package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

func TestComfyOutputCleanupWorkerReceiptsIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name               string
		completed, pending int
		freed              int64
		lastOutcome        string
	}{
		{"exact", 2, 0, 42, ""},
		{"partial", 1, 1, 42, "unconfirmed"},
		{"legacy", 2, 0, 0, ""},
		{"mismatched", 0, 2, 0, "mismatched"},
		{"unavailable", 0, 2, 0, "host_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetGatewayIntegrationDatabase(t, db)
			r := store.New(db)
			for i, name := range []string{"a.png", "b.png"} {
				if err := r.ScheduleComfyOutputCleanup(ctx, domain.ComfyOutputCleanupTombstone{Filename: name, StorageType: "output", SizeBytes: int64(42 + i), ContentHash: strings.Repeat("a", 64)}, time.Now().Add(-time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls++
				if request.Method != http.MethodPost || request.URL.Path != "/v1/comfy-output/delete" {
					t.Error("wrong cleanup request", request.Method, request.URL)
				}
				var batch updates.ComfyAssetDeleteRequest
				if err := json.NewDecoder(request.Body).Decode(&batch); err != nil || len(batch.Files) != 2 {
					t.Error("cleanup batch", batch, err)
					w.WriteHeader(400)
					return
				}
				result := updates.ComfyAssetDeleteResult{}
				for i, item := range batch.Files {
					status := "deleted"
					if i == 1 {
						status = "missing"
					}
					if tc.name == "mismatched" {
						status = "mismatched"
					}
					result.Items = append(result.Items, updates.ComfyAssetDeleteOutcome{Filename: item.Filename, Subfolder: item.Subfolder, StorageType: item.StorageType, SizeBytes: item.SizeBytes, SHA256: item.SHA256, Status: status})
				}
				switch tc.name {
				case "partial":
					result.Items = result.Items[:1]
					w.WriteHeader(500)
				case "legacy":
					result.Items = nil
					result.Deleted, result.Missing = 1, 1
				}
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer host.Close()
			endpoint, _ := url.Parse(host.URL)
			client := updates.NewClient(endpoint, strings.Repeat("x", 32))
			if tc.name == "unavailable" {
				client = updates.NewClient(nil, "")
			}
			a := &App{store: r, updates: client}
			completed, err := a.deleteExpiredComfyMedia(ctx)
			if completed != int64(tc.completed) || (err != nil) != (tc.pending > 0) {
				t.Fatal("worker result", completed, err)
			}
			if tc.name != "unavailable" && calls != 1 {
				t.Fatal("repeated host request", calls)
			}
			receipts, err := r.ComfyOutputCleanupReceipts(ctx, 100)
			var freed int64
			for _, receipt := range receipts {
				freed += receipt.FreedBytes
			}
			if err != nil || len(receipts) != tc.completed || freed != tc.freed {
				t.Fatal("host receipts", receipts, freed, err)
			}
			var pending int
			if err := db.QueryRow(`SELECT count(*) FROM comfy_output_cleanup_tombstones WHERE last_outcome=$1 AND attempt_count=1 AND next_attempt_at>now()`, tc.lastOutcome).Scan(&pending); err != nil || pending != tc.pending {
				t.Fatal("retained retry reason", pending, err)
			}
		})
	}
}
