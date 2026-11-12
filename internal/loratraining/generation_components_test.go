package loratraining

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/componentinspection"
)

func TestComponentInspectionClientContract(t *testing.T) {
	request := componentinspection.Request{Version: 1, RequestID: "generation-request-001", References: []componentinspection.Reference{{Kind: "lora", Name: "adapter.safetensors"}}}
	valid := func() componentinspection.Response {
		now := time.Now().UTC()
		return componentinspection.Response{Version: 1, RequestID: request.RequestID, RequestSHA256: request.Digest(), Source: componentinspection.Source, State: "complete",
			Items: []componentinspection.Observation{{Reference: request.References[0], State: "verified", SHA256: strings.Repeat("a", 64), Bytes: 12, HashedAt: now, CheckedAt: now}}}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*componentinspection.Response)
		valid  bool
	}{
		{"verified", func(*componentinspection.Response) {}, true},
		{"pending", func(r *componentinspection.Response) {
			r.State = "pending"
			r.Items[0] = componentinspection.Observation{Reference: request.References[0], State: "pending", CheckedAt: time.Now()}
		}, true},
		{"missing", func(r *componentinspection.Response) {
			r.Items[0] = componentinspection.Observation{Reference: request.References[0], State: "missing", CheckedAt: time.Now()}
		}, true},
		{"foreign request", func(r *componentinspection.Response) { r.RequestID = "foreign-request-001" }, false},
		{"foreign digest", func(r *componentinspection.Response) { r.RequestSHA256 = strings.Repeat("0", 64) }, false},
		{"foreign file", func(r *componentinspection.Response) { r.Items[0].Name = "other.safetensors" }, false},
		{"duplicate", func(r *componentinspection.Response) { r.Items = append(r.Items, r.Items[0]) }, false},
		{"absent", func(r *componentinspection.Response) { r.Items = nil }, false},
		{"false completion", func(r *componentinspection.Response) { r.State = "pending" }, false},
		{"invalid hash", func(r *componentinspection.Response) { r.Items[0].SHA256 = "invalid" }, false},
		{"invented hash", func(r *componentinspection.Response) { r.Items[0].State = "missing" }, false},
		{"negative bytes", func(r *componentinspection.Response) { r.Items[0].Bytes = -1 }, false},
		{"stale check", func(r *componentinspection.Response) {
			r.Items[0].CheckedAt = time.Now().Add(-time.Hour)
			r.Items[0].HashedAt = r.Items[0].CheckedAt
		}, false},
		{"stale hash", func(r *componentinspection.Response) { r.Items[0].HashedAt = time.Now().Add(-time.Hour) }, false},
		{"future check", func(r *componentinspection.Response) { r.Items[0].CheckedAt = time.Now().Add(time.Hour) }, false},
		{"foreign source", func(r *componentinspection.Response) { r.Source = "comfy_loaded_bytes" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/generation/components/inspect" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
					t.Error("wrong request boundary")
				}
				var received componentinspection.Request
				if json.NewDecoder(r.Body).Decode(&received) != nil || received.Digest() != request.Digest() {
					t.Error("request changed")
				}
				result := valid()
				tc.mutate(&result)
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			_, err := NewClient(base, strings.Repeat("t", 32)).InspectGenerationComponents(context.Background(), request)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
	encoded, _ := json.Marshal(valid())
	for _, body := range []string{"{", string(encoded) + " {}", strings.Repeat(" ", 128<<10) + string(encoded), strings.Replace(string(encoded), `"version":1`, `"unknown":true,"version":1`, 1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		base, _ := url.Parse(server.URL)
		_, err := NewClient(base, strings.Repeat("t", 32)).InspectGenerationComponents(context.Background(), request)
		server.Close()
		if err == nil {
			t.Fatal("invalid wire body accepted")
		}
	}
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer target.Close()
	for _, status := range []int{302, 307, 404, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(status)
		}))
		base, _ := url.Parse(server.URL)
		_, err := NewClient(base, strings.Repeat("t", 32)).InspectGenerationComponents(context.Background(), request)
		server.Close()
		if err == nil {
			t.Fatal("unavailable endpoint accepted")
		}
	}
	if followed.Load() != 0 {
		t.Fatal("redirect followed")
	}
	var client *Client
	if _, err := client.InspectGenerationComponents(context.Background(), request); err == nil {
		t.Fatal("nil client accepted")
	}
}
