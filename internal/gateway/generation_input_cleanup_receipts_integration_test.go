package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

func TestComfyInputCleanupWorkerReceiptsIntegration(t *testing.T) {
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
		last               string
	}{
		{"exact", 2, 0, 42, ""}, {"partial", 1, 1, 42, "unconfirmed"}, {"legacy", 2, 0, 0, ""},
		{"mismatched", 0, 2, 0, "mismatched"}, {"unavailable", 0, 2, 0, "host_unavailable"},
		{"conflicting", 0, 2, 0, "unconfirmed"}, {"invalid_legacy", 0, 2, 0, "unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetGatewayIntegrationDatabase(t, db)
			r := store.New(db)
			for i := range 2 {
				if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,filename,size_bytes,content_hash,state,expires_at)
				 VALUES($1,$2,$3,$4,'stored',now()-interval '1 hour')`, fmt.Sprintf("%032d", i+1), fmt.Sprintf("%d.png", i), 42+i, strings.Repeat("a", 64)); err != nil {
					t.Fatal(err)
				}
			}
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Method != "POST" || request.URL.Path != "/v1/comfy-asset/delete" {
					t.Error(request.Method, request.URL)
					w.WriteHeader(400)
					return
				}
				var batch updates.ComfyAssetDeleteRequest
				if err := json.NewDecoder(request.Body).Decode(&batch); err != nil || len(batch.Files) != 2 {
					t.Error(batch, err)
					w.WriteHeader(400)
					return
				}
				result := updates.ComfyAssetDeleteResult{}
				for i, file := range batch.Files {
					status := "deleted"
					if i == 1 {
						status = "missing"
					}
					if tc.name == "mismatched" {
						status = "mismatched"
					}
					ack := updates.ComfyAssetDeleteOutcome{Filename: file.Filename, Subfolder: file.Subfolder, StorageType: file.StorageType, SizeBytes: file.SizeBytes, SHA256: file.SHA256, Status: status}
					result.Items = append(result.Items, ack)
					if tc.name == "conflicting" {
						ack.Status = "rejected"
						result.Items = append(result.Items, ack)
					}
				}
				switch tc.name {
				case "partial":
					result.Items = result.Items[:1]
					w.WriteHeader(500)
				case "legacy":
					result.Items = nil
					result.Deleted, result.Missing = 1, 1
				case "invalid_legacy":
					result.Items = nil
					result.Deleted, result.Missing = -1, 3
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
			completed, err := a.deleteExpiredComfyInputs(ctx)
			if completed != int64(tc.completed) || (err != nil) != (tc.pending > 0) {
				t.Fatal(completed, err)
			}
			receipts, err := r.ComfyInputCleanupReceipts(ctx, 100)
			var total int64
			for _, receipt := range receipts {
				total += receipt.FreedBytes
			}
			if err != nil || len(receipts) != tc.completed || total != tc.freed {
				t.Fatal(receipts, err)
			}
			var pending int
			if err := db.QueryRow(`SELECT count(*) FROM comfy_input_assets WHERE cleanup_last_outcome=$1 AND cleanup_attempts=1 AND cleanup_retry_at>now()`, tc.last).Scan(&pending); err != nil || pending != tc.pending {
				t.Fatal(pending, err)
			}
		})
	}
}
