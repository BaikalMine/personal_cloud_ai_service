package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updateagent"
	"ai-access-gateway/internal/updates"
)

func TestComfyFilesHTTPValidation(t *testing.T) {
	app := &App{}
	admin := &User{ID: 1, Role: "admin"}
	for _, tc := range []struct {
		method, path string
		actor        *User
		code         int
	}{
		{"GET", "/admin/storage/comfy/files", nil, 403},
		{"GET", "/admin/storage/comfy/files", &User{Role: "user"}, 403},
		{"POST", "/admin/storage/comfy/files", admin, 405},
		{"GET", "/admin/storage/comfy/files?path=secret", admin, 400},
		{"GET", "/admin/storage/comfy/files?kind=input&kind=output", admin, 400},
		{"GET", "/admin/storage/comfy/files", admin, 503},
	} {
		w := comfyStorageRequest(app, tc.method, tc.path, "", tc.actor, false)
		if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}

func TestComfyFilesHTTPInventoryIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, err := database.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	actor := &User{Role: "admin", Username: "inventory-admin"}
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES($1,'hash','admin') RETURNING id`, actor.Username).Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"known.png", "unknown.png"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("observed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,user_id,filename,size_bytes,content_hash,state) VALUES($1,$2,'known.png',8,repeat('a',64),'stored')`, strings.Repeat("a", 32), actor.ID); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/comfy-asset/inventory" {
			t.Error("unexpected mutation", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		query, err := updates.ParseComfyFileInventoryQuery(r.URL.RawQuery)
		if err != nil {
			t.Fatal(err)
		}
		result, err := updateagent.ComfyFileInventory(r.Context(), updateagent.ComfyTarget{InputDirectory: root}, query)
		if err != nil {
			t.Fatal(err)
		}
		writeJSON(w, 200, result)
	}))
	defer host.Close()
	base, _ := url.Parse(host.URL)
	app := &App{store: store.New(db), updates: updates.NewClient(base, strings.Repeat("t", 64))}
	for _, drill := range []bool{false, true} {
		app.cfg.RestoreDrillMode = drill
		w := comfyStorageRequest(app, "GET", "/admin/storage/comfy/files?kind=input", "", actor, false)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct {
			Scope     string
			Inventory struct {
				Complete bool
				Items    []struct {
					Filename     string
					Registration string
					CanCleanup   bool `json:"can_cleanup"`
				}
			}
		}
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Scope != "host_metadata" || !result.Inventory.Complete || len(result.Inventory.Items) != 2 {
			t.Fatal(w.Body.String())
		}
		for _, item := range result.Inventory.Items {
			want := "unregistered_path"
			if item.Filename == "known.png" {
				want = "registered_path"
			}
			if item.Registration != want || item.CanCleanup {
				t.Fatal(item)
			}
		}
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	for _, name := range []string{"known.png", "unknown.png"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(data) != "observed" {
			t.Fatal("inventory changed bytes", err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM comfy_input_assets WHERE filename='known.png' AND cleanup_attempts=0 AND state='stored'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("inventory mutated registry", count, err)
	}
}
