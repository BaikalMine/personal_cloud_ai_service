package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/security"
)

func TestAdminTrainingStorageObservationBoundary(t *testing.T) {
	item := loratraining.StorageItem{ID: "training_cache:example", Kind: "training_cache", Label: "cache", State: "present", CanCleanup: true, Version: strings.Repeat("a", 64), SizeBytes: 32, Files: 1}
	inventory := loratraining.StorageInventory{ProtocolVersion: 1, CheckedAt: time.Now().UTC(), Complete: true, TotalItems: 1, Items: []loratraining.StorageItem{item}}
	var calls atomic.Int64
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
			t.Error("missing host authentication")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/storage":
			if r.URL.Query().Get("limit") != "1" {
				t.Error("lost page bound", r.URL.RawQuery)
			}
		case "POST /v1/storage/preview":
			var input loratraining.StoragePreviewRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.IDs) != 1 || input.IDs[0] != item.ID {
				t.Error("changed selection", input, err)
			}
		default:
			t.Error("unexpected host operation", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(inventory)
	}))
	defer agent.Close()
	base, _ := url.Parse(agent.URL)
	app := &App{loraTraining: loratraining.NewClient(base, strings.Repeat("t", 32)), csrfSigner: security.NewCSRFSigner("storage-observation-test")}
	admin := &User{ID: 1, Role: "admin"}
	invoke := func(method, path, body string, actor *User, csrf bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if actor != nil {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "storage-session"})
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", app.csrfSigner.Token("storage-session"))
		}
		if actor != nil {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, actor))
		}
		w := httptest.NewRecorder()
		app.handleAdminRoutes(w, r)
		return w
	}
	for _, test := range []struct {
		method, path, body string
		actor              *User
		csrf               bool
		status             int
	}{
		{"GET", "/admin/storage/training", "", nil, false, 403},
		{"GET", "/admin/storage/training", "", &User{Role: "user"}, false, 403},
		{"POST", "/admin/storage/training", "", admin, true, 405},
		{"GET", "/admin/storage/training/preview", "", admin, true, 405},
		{"POST", "/admin/storage/training/preview", `{"ids":["training_cache:example"]}`, admin, false, 403},
		{"GET", "/admin/storage/training?limit=0", "", admin, false, 400},
		{"GET", "/admin/storage/training?limit=101", "", admin, false, 400},
		{"GET", "/admin/storage/training?limit=01", "", admin, false, 400},
		{"GET", "/admin/storage/training?limit=", "", admin, false, 400},
		{"GET", "/admin/storage/training?limit=1&limit=2", "", admin, false, 400},
		{"GET", "/admin/storage/training?after=../../models", "", admin, false, 400},
		{"GET", "/admin/storage/training?path=C:/models", "", admin, false, 400},
		{"POST", "/admin/storage/training/preview?confirm=true", `{"ids":["training_cache:example"]}`, admin, true, 400},
		{"POST", "/admin/storage/training/preview", `{"ids":[]}`, admin, true, 400},
		{"POST", "/admin/storage/training/preview", `{"ids":["training_cache:example","training_cache:example"]}`, admin, true, 400},
		{"POST", "/admin/storage/training/preview", `{"ids":["training_cache:example"],"delete":true}`, admin, true, 400},
		{"POST", "/admin/storage/training/preview", `{"ids":["training_cache:example"]}{}`, admin, true, 400},
		{"POST", "/admin/storage/training/preview", `{"ids":["` + strings.Repeat("x", 17000) + `"]}`, admin, true, 400},
		{"POST", "/admin/storage/training/cleanup", `{"ids":["training_cache:example"]}`, admin, true, 404},
		{"POST", "/admin/storage/training/fence", `{"ids":["training_cache:example"]}`, admin, true, 404},
	} {
		w := invoke(test.method, test.path, test.body, test.actor, test.csrf)
		if w.Code != test.status {
			t.Fatalf("%s %s: %d want %d: %s", test.method, test.path, w.Code, test.status, w.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached host", calls.Load())
	}
	for _, w := range []*httptest.ResponseRecorder{
		invoke("GET", "/admin/storage/training?limit=1", "", admin, false),
		invoke("POST", "/admin/storage/training/preview", `{"ids":["training_cache:example"]}`, admin, true),
	} {
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body.String())
		}
		var response struct {
			Inventory loratraining.StorageInventory `json:"inventory"`
			Available bool                          `json:"manual_cleanup_available"`
			Reason    string                        `json:"manual_cleanup_reason"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !response.Available || response.Reason != "" || len(response.Inventory.Items) != 1 || response.Inventory.Items[0] != item {
			t.Fatal("invalid observation", response, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected host call count", calls.Load())
	}
}

func TestAdminTrainingStorageHostFailures(t *testing.T) {
	for _, mode := range []string{"not configured", "old agent", "redirect", "malformed", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			var destructiveCalls atomic.Int64
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/storage" {
					destructiveCalls.Add(1)
				}
				switch mode {
				case "old agent":
					http.Error(w, "C:/private/models", 404)
				case "redirect":
					http.Redirect(w, r, "/v1/storage/cleanup", 307)
				case "malformed":
					_, _ = w.Write([]byte(`{"protocol_version":99}`))
				default:
					http.Error(w, "C:/private/models", 500)
				}
			}))
			defer agent.Close()
			base, _ := url.Parse(agent.URL)
			app := &App{}
			if mode != "not configured" {
				app.loraTraining = loratraining.NewClient(base, strings.Repeat("t", 32))
			}
			r := httptest.NewRequest("GET", "/admin/storage/training", nil)
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{Role: "admin"}))
			w := httptest.NewRecorder()
			app.handleAdminRoutes(w, r)
			want := 502
			if mode == "not configured" || mode == "old agent" {
				want = 503
			}
			if w.Code != want || strings.Contains(w.Body.String(), "C:/private") || destructiveCalls.Load() != 0 {
				t.Fatal(w.Code, w.Body.String(), destructiveCalls.Load())
			}
		})
	}
}
