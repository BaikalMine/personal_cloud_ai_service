package updates

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCleanupInspectionClientNeverFallsBackToDelete(t *testing.T) {
	item := ComfyAssetFile{Filename: "item.png", StorageType: "output", SizeBytes: 42, SHA256: strings.Repeat("a", 64)}
	for _, scenario := range []string{"success", "unsupported", "redirect", "wrong_identity", "deleted", "unversioned", "partial", "trailing"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/comfy-asset/inspect" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
					t.Error("wrong request", r.Method, r.URL)
				}
				if scenario == "unsupported" {
					w.WriteHeader(404)
					return
				}
				if scenario == "redirect" {
					http.Redirect(w, r, "/v1/comfy-asset/delete", http.StatusTemporaryRedirect)
					return
				}
				var request ComfyAssetDeleteRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Files) != 1 || request.Files[0] != item {
					t.Error("changed inspection targets", request, err)
				}
				result := ComfyAssetInspectResult{Protocol: 1, CheckedAt: time.Now().UTC(), Complete: true, Items: []ComfyAssetDeleteOutcome{{Filename: item.Filename, StorageType: item.StorageType, SizeBytes: item.SizeBytes, SHA256: item.SHA256, Status: "matched"}}}
				switch scenario {
				case "wrong_identity":
					result.Items[0].Filename = "other.png"
				case "deleted":
					result.Items[0].Status = "deleted"
				case "unversioned":
					result.Protocol = 0
				case "partial":
					result.Complete = false
					result.Items[0].Status = "io_error"
					w.WriteHeader(503)
				}
				_ = json.NewEncoder(w).Encode(result)
				if scenario == "trailing" {
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL + "/old?ignore=1")
			client := NewClient(base, strings.Repeat("x", 32))
			result, err := client.InspectComfyAssets(context.Background(), []ComfyAssetFile{item})
			if (err == nil) != (scenario == "success") || calls != 1 {
				t.Fatal(result, err, calls)
			}
			if scenario == "partial" && (len(result.Items) != 1 || result.Items[0].Status != "io_error") {
				t.Fatal("lost safe partial observation", result)
			}
		})
	}
}
