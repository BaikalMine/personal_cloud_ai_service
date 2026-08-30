package gateway

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestGatewayIntegrationComfyOwnership(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)

	var userID, adminID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO users(username,password_hash,role) VALUES('gateway-user','disabled','user') RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO users(username,password_hash,role) VALUES('gateway-admin','disabled','admin') RETURNING id
	`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	repository := store.New(db)
	if _, err := repository.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: userID, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: "owned-prompt",
		PromptCipher: []byte{1}, ResponseCipher: []byte{2}, MetadataCipher: []byte{3},
	}); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"owned-prompt":{"outputs":{"9":{"images":[{"filename":"owned.png","subfolder":"","type":"output"}]}}},
			"foreign-prompt":{"outputs":{"9":{"images":[{"filename":"foreign.png","subfolder":"","type":"output"}]}}}
		}`))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		cfg:   Config{ComfyUIUpstream: upstreamURL, SessionSecret: "01234567890123456789012345678901"},
		store: repository,
	}
	if err := app.refreshComfyOutputOwnerships(ctx, userID); err != nil {
		t.Fatal(err)
	}
	ownerID, known, err := repository.ComfyOutputOwner(ctx, "owned.png", "", "output")
	if err != nil || !known || ownerID != userID {
		t.Fatalf("refreshed owner = %d, known=%v, err=%v", ownerID, known, err)
	}
	if _, known, err := repository.ComfyOutputOwner(ctx, "foreign.png", "", "output"); err != nil || known {
		t.Fatalf("foreign history leaked into ownership: known=%v err=%v", known, err)
	}

	user := &User{ID: userID, Role: "user"}
	admin := &User{ID: adminID, Role: "admin"}
	assertComfyMediaAccess(t, app, user, "/view?filename=owned.png&type=output", true)
	assertComfyMediaAccess(t, app, user, "/view?filename=legacy.png&type=output", false)
	assertComfyMediaAccess(t, app, admin, "/view?filename=legacy.png&type=output", true)
	assertComfyMediaAccess(t, app, admin, "/view?filename=owned.png&type=output", false)

	ownNamespace := comfyUploadNamespace(app.comfyClientID(userID))
	assertComfyMediaAccess(t, app, user, "/view?filename=input.png&type=input&subfolder="+url.QueryEscape(ownNamespace), true)
	assertComfyMediaAccess(t, app, user, "/view?filename=input.png&type=input&subfolder=gateway%2Fgateway-aaaaaaaaaaaaaaaaaaaaaaaa", false)
	assertComfyMediaAccess(t, app, user, "/view?filename=legacy-input.png&type=input", false)
}

func assertComfyMediaAccess(t *testing.T, app *App, user *User, target string, want bool) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	got, err := app.authorizeComfyMediaRequest(request, user)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("authorizeComfyMediaRequest(%q, role=%s) = %v, want %v", target, user.Role, got, want)
	}
}

func resetGatewayIntegrationDatabase(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		TRUNCATE comfy_output_cleanup_tombstones, comfy_input_assets, comfy_userdata, comfy_settings, comfy_output_ownership, content_media, content_events,
			websocket_sessions, proxy_requests, audit_log, invite_uses, invites, sessions, users RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatal(err)
	}
}
