package updates

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestComfyStorageClientNeverRetriesOrFallsBack(t *testing.T) {
	for _, scenario := range []string{"success", "partial_closure", "redirect", "unsupported", "busy", "not_found", "wrong_hash", "wrong_executor", "wrong_item", "wrong_protocol", "oversize", "trailing", "unknown_field", "invalid_credit"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/comfy-storage/execute" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 64) {
					t.Error("incorrect request", r.Method, r.URL)
				}
				var request ComfyStorageRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("invalid request body")
				}
				result := comfyStorageTestResult()
				switch scenario {
				case "redirect":
					http.Redirect(w, r, "/v1/comfy-asset/delete", 307)
					return
				case "unsupported":
					w.WriteHeader(404)
					return
				case "busy":
					w.WriteHeader(409)
					_, _ = w.Write([]byte(`{"protocol":1,"error":"storage_busy"}`))
					return
				case "not_found":
					w.WriteHeader(404)
					_, _ = w.Write([]byte(`{"protocol":1,"error":"storage_operation_not_found"}`))
					return
				case "partial_closure":
					result.GuardClosed = false
					w.WriteHeader(503)
				case "wrong_hash":
					result.RequestHash = strings.Repeat("0", 64)
				case "wrong_executor":
					result.ExecutorID = strings.Repeat("0", 64)
				case "wrong_item":
					result.Items[0].Selection.File.Filename = "different.png"
				case "wrong_protocol":
					result.Protocol = 2
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat(" ", maxResponseBytes+1)))
					return
				case "invalid_credit":
					result.Items[0].FreedBytes++
				}
				if scenario == "unknown_field" {
					data, _ := json.Marshal(result)
					_, _ = w.Write([]byte(strings.Replace(string(data), `"protocol":1`, `"protocol":1,"unexpected":true`, 1)))
					return
				}
				_ = json.NewEncoder(w).Encode(result)
				if scenario == "trailing" {
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL + "/old?ignored=1")
			client := NewClient(endpoint, strings.Repeat("s", 64))
			result, err := client.ExecuteComfyStorage(context.Background(), comfyStorageTestRequest())
			if calls != 1 || (err == nil) != (scenario == "success") {
				t.Fatal(result, err, calls)
			}
			if scenario == "partial_closure" && (!errors.Is(err, ErrUnavailable) || !result.ExecutorFenced || result.GuardClosed || len(result.Items) != 2) {
				t.Fatal("safe partial evidence lost", result, err)
			}
			if scenario == "busy" && !errors.Is(err, ErrComfyStorageBusy) {
				t.Fatal(err)
			}
			if scenario == "not_found" && !errors.Is(err, ErrComfyStorageNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func TestComfyStorageClientReadAndFenceOnlyUseSelectedRoute(t *testing.T) {
	for _, action := range []string{"read", "fence"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if action == "read" {
					if r.Method != http.MethodGet || r.URL.Path != "/v1/comfy-storage/operations/"+comfyStorageTestRequest().ID {
						t.Error(r.Method, r.URL)
					}
				} else if r.Method != http.MethodPost || r.URL.Path != "/v1/comfy-storage/fence" {
					t.Error(r.Method, r.URL)
				}
				_ = json.NewEncoder(w).Encode(comfyStorageTestResult())
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			client := NewClient(endpoint, strings.Repeat("s", 64))
			var err error
			if action == "read" {
				_, err = client.ReadComfyStorage(context.Background(), comfyStorageTestRequest())
			} else {
				_, err = client.FenceComfyStorage(context.Background(), comfyStorageTestRequest())
			}
			if err != nil || calls != 1 {
				t.Fatal(err, calls)
			}
		})
	}
}
