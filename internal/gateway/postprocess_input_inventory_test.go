package gateway

import (
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestPostprocessInputInventoryHTTPIntegration(t *testing.T) {
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
	const owner int64 = 9007199254740993
	if _, err := db.Exec(`INSERT INTO users(id,username,password_hash,role) VALUES($1,'inventory-owner','hash','user')`, owner); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		id := fmt.Sprintf("http-inventory-%032d", n)
		var userID any
		if n == 1 {
			userID = owner
		}
		if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,user_id,filename,subfolder,size_bytes,content_hash,state,expires_at) VALUES($1,$2,'source.png','private',42,$3,'reserved',now())`, id, userID, strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO generation_postprocess_inputs(input_id,source_position,dispatch_token,cleanup_blocked) VALUES($1,0,$2,$3)`, id, strings.Repeat("b", 32), n != 3); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{store: store.New(db)}
	actor := &User{Role: "admin"}
	cursor := ""
	for page := 0; page < 3; page++ {
		w := comfyStorageRequest(app, "GET", "/admin/storage/comfy/unresolved-inputs?limit=1&after="+url.QueryEscape(cursor), "", actor, false)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body.String())
		}
		var body struct {
			Items []map[string]any `json:"items"`
			Next  string           `json:"next_after"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if page == 2 {
			if len(body.Items) != 0 || body.Next != "" {
				t.Fatal(body)
			}
			break
		}
		if len(body.Items) != 1 {
			t.Fatal(body)
		}
		item := body.Items[0]
		if item["can_cleanup"] != false || item["job_id"] != nil || item["dispatch_token"] != nil {
			t.Fatal(item)
		}
		if page == 0 && item["user_id"] != "9007199254740993" {
			t.Fatal("owner ID lost precision", item)
		}
		if page == 1 && item["user_id"] != nil {
			t.Fatal(item)
		}
		if body.Next == "" || body.Next == cursor {
			t.Fatal("invalid cursor", body.Next)
		}
		cursor = body.Next
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM generation_postprocess_inputs WHERE cleanup_blocked`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}

func TestPostprocessInputInventoryHTTPValidation(t *testing.T) {
	app := &App{}
	admin := &User{ID: 1, Role: "admin"}
	for _, tc := range []struct {
		method, query string
		actor         *User
		status        int
	}{
		{"GET", "", nil, 403}, {"GET", "", &User{Role: "user"}, 403},
		{"POST", "", admin, 405}, {"GET", "?limit=0", admin, 400},
		{"GET", "?limit=101", admin, 400}, {"GET", "?limit=01", admin, 400},
		{"GET", "?limit=1&limit=2", admin, 400}, {"GET", "?path=secret", admin, 400},
		{"GET", "?after=%ZZ", admin, 400}, {"GET", "", admin, 503},
	} {
		w := comfyStorageRequest(app, tc.method, "/admin/storage/comfy/unresolved-inputs"+tc.query, "", tc.actor, false)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: %d %s", tc.method, tc.query, w.Code, w.Body.String())
		}
	}
}
