package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
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
	eventID, err := repository.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: userID, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: "owned-prompt",
		PromptCipher: []byte{1}, ResponseCipher: []byte{2}, MetadataCipher: []byte{3},
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	})
	if err != nil {
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
	contentCipher, err := contentcrypto.NewCipher("01234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	spoolDir := t.TempDir()
	app := &App{
		cfg: Config{
			ComfyUIUpstream: upstreamURL, SessionSecret: "01234567890123456789012345678901",
			MediaSpoolDir: spoolDir, MediaInFlightLimitBytes: 64 << 20,
		},
		store: repository, contentCipher: contentCipher,
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

	assertChunkedMediaRoundTrip(t, ctx, db, app, eventID, userID, spoolDir)
	assertPriorityMiningResume(t, ctx, repository, userID)
}

func assertPriorityMiningResume(t *testing.T, ctx context.Context, repository *store.Store, userID int64) {
	t.Helper()
	_, err := repository.CreateMiner(ctx, store.CreateMinerParams{
		Name: "Integration miner", ScriptPath: `C:\Mining\start.bat`, ProcessName: "miner.exe",
		Enabled: true, Default: true, CreatedByUserID: userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var running atomic.Bool
	var startCalls atomic.Int64
	var stopCalls atomic.Int64
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/state":
			_ = json.NewEncoder(w).Encode(mining.State{Running: running.Load(), ProcessName: "miner.exe"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/start":
			var request mining.Request
			if decodeErr := json.NewDecoder(r.Body).Decode(&request); decodeErr != nil || request.ScriptPath != `C:\Mining\start.bat` || request.ProcessName != "miner.exe" {
				http.Error(w, "invalid start request", http.StatusBadRequest)
				return
			}
			startCalls.Add(1)
			running.Store(true)
			_ = json.NewEncoder(w).Encode(mining.State{Running: true, ProcessName: "miner.exe"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/stop":
			stopCalls.Add(1)
			running.Store(false)
			_ = json.NewEncoder(w).Encode(mining.State{Running: false, ProcessName: "miner.exe"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()
	agentURL, err := url.Parse(agent.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: repository, mining: mining.NewClient(agentURL, "integration-mining-token")}
	user := &User{ID: userID, Username: "gateway-user", PauseMiningForQuickGeneration: true}
	lease, status, err := app.pauseMiningForQuickGeneration(ctx, user, 0)
	if err != nil || lease == nil || !lease.ResumeMining {
		t.Fatalf("priority lease=%+v status=%q err=%v", lease, status, err)
	}
	if status == "" || stopCalls.Load() != 0 {
		t.Fatalf("stopped miner reservation status=%q stop_calls=%d", status, stopCalls.Load())
	}
	if !app.releaseMiningPause(ctx, lease.ID) {
		t.Fatal("priority lease was not released")
	}
	if !running.Load() || startCalls.Load() != 1 {
		t.Fatalf("miner running=%v start_calls=%d", running.Load(), startCalls.Load())
	}
	if _, err := repository.ActiveQuickGenerationMiningLease(ctx); err != sql.ErrNoRows {
		t.Fatalf("active priority lease after release: %v", err)
	}
}

func assertChunkedMediaRoundTrip(t *testing.T, ctx context.Context, db *sql.DB, app *App, eventID, userID int64, spoolDir string) {
	t.Helper()
	payload := bytes.Repeat([]byte("chunked-media-"), (store.ContentMediaChunkSize/14)+3)
	digest := sha256.Sum256(payload)
	if err := app.store.InsertComfyOutputOwnerships(ctx, userID, []domain.ComfyOutputOwnership{{
		PromptID: "owned-prompt", Filename: "chunked.png", StorageType: "output", MediaType: "image",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	capture := &proxyContentCapture{
		userID: userID, service: "comfyui", mediaName: "chunked.png", mediaStorageType: "output",
		mediaType: "image", mimeType: "image/png", isMedia: true, status: http.StatusOK,
	}
	if err := app.persistComfyMediaReader(ctx, capture, bytes.NewReader(payload), int64(len(payload)), hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	var mediaID int64
	var storageFormat string
	var chunkCount int
	if err := db.QueryRowContext(ctx, `
		SELECT m.id,m.storage_format,(SELECT count(*) FROM content_media_chunks c WHERE c.media_id=m.id)
		FROM content_media m WHERE m.event_id=$1 AND m.original_name='chunked.png'
	`, eventID).Scan(&mediaID, &storageFormat, &chunkCount); err != nil {
		t.Fatal(err)
	}
	if storageFormat != "chunked_v1" || chunkCount < 2 {
		t.Fatalf("chunked storage format=%q chunks=%d", storageFormat, chunkCount)
	}
	media, err := app.store.ContentMediaByIDForUser(ctx, mediaID, userID)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := app.materializeContentMedia(ctx, media)
	if err != nil {
		t.Fatal(err)
	}
	actual, readErr := io.ReadAll(materialized)
	closeErr := materialized.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(actual, payload) {
		t.Fatalf("materialized media bytes=%d read_err=%v close_err=%v", len(actual), readErr, closeErr)
	}
	entries, err := os.ReadDir(spoolDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("media round trip left spool files=%v err=%v", entries, err)
	}
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
		TRUNCATE quick_generation_mining_leases, miners, comfy_output_cleanup_tombstones, comfy_input_assets, comfy_userdata, comfy_settings, comfy_output_ownership, content_media, prompt_assistant_runs, content_events,
			websocket_sessions, proxy_requests, audit_log, invite_uses, invites, sessions, users RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatal(err)
	}
}
