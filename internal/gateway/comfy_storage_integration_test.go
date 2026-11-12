package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updateagent"
	"ai-access-gateway/internal/updates"
)

func TestComfyStorageHTTPInspectionIntegration(t *testing.T) {
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
	if _, err := db.Exec(`TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	actor := &User{Role: "admin", Username: "comfy-storage-admin"}
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES($1,'hash','admin') RETURNING id`, actor.Username).Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	inputDir := filepath.Join(root, "input")
	if err := os.Mkdir(inputDir, 0700); err != nil {
		t.Fatal(err)
	}
	contents := []byte("tracked input bytes")
	digest := sha256.Sum256(contents)
	hash := hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(inputDir, "kept.png"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inputDir, "changed.png"), []byte("changed input bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"kept.png", "changed.png", "upload.png"} {
		state := "stored"
		if i == 2 {
			state = "reserved"
		}
		if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,user_id,filename,size_bytes,content_hash,state) VALUES($1,$2,$3,$4,$5,$6)`, strings.Repeat(strconv.Itoa(i+1), 32), actor.ID, name, len(contents), hash, state); err != nil {
			t.Fatal(err)
		}
	}
	var cleanupID int64
	if err := db.QueryRow(`INSERT INTO comfy_output_cleanup_tombstones(filename,storage_type,size_bytes,content_hash) VALUES('gone.png','output',$1,$2) RETURNING id`, len(contents), hash).Scan(&cleanupID); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/comfy-asset/inspect" || r.Method != "POST" {
			t.Error("unexpected host mutation", r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		var request updates.ComfyAssetDeleteRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		result, err := updateagent.InspectComfyAssetFiles(r.Context(), updateagent.ComfyTarget{WorkingDirectory: root}, request.Files)
		code := 200
		if err != nil {
			code = 503
		}
		writeJSON(w, code, result)
	}))
	defer agent.Close()
	base, _ := url.Parse(agent.URL)
	app := &App{store: store.New(db), updates: updates.NewClient(base, strings.Repeat("s", 32)), csrfSigner: security.NewCSRFSigner("comfy-storage-test-secret")}
	w := comfyStorageRequest(app, "GET", "/admin/storage/comfy", "", actor, false)
	if w.Code != 200 || calls.Load() != 0 {
		t.Fatal(w.Code, w.Body.String(), calls.Load())
	}
	var reply struct {
		Inventory domain.ComfyStorageInventory `json:"inventory"`
		Manual    bool                         `json:"manual_cleanup_available"`
		Scope     string                       `json:"scope"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Manual || reply.Scope != "tracked_records" || len(reply.Inventory.Items) != 4 {
		t.Fatal(reply, err)
	}
	selection := []comfyStorageSelection{}
	for _, item := range reply.Inventory.Items {
		selection = append(selection, comfyStorageSelection{Key: item.Key, Version: item.Version})
	}
	request, _ := json.Marshal(map[string]any{"items": selection})
	w = comfyStorageRequest(app, "POST", "/admin/storage/comfy/inspect", string(request), actor, true)
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatal(w.Code, w.Body.String(), calls.Load())
	}
	var inspected struct {
		Inspection comfyStorageInspection `json:"inspection"`
		Manual     bool                   `json:"manual_cleanup_available"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &inspected); err != nil || inspected.Manual || inspected.Inspection.Complete || inspected.Inspection.HostState != "available" {
		t.Fatal(inspected, err)
	}
	want := map[string]string{"input:" + strings.Repeat("1", 32): "matched", "input:" + strings.Repeat("2", 32): "mismatched", "input:" + strings.Repeat("3", 32): "upload_unconfirmed", "cleanup:" + strconv.FormatInt(cleanupID, 10): "missing"}
	for _, item := range inspected.Inspection.Items {
		if want[item.Key] != item.Status {
			t.Fatal(item)
		}
	}
	for _, name := range []string{"kept.png", "changed.png"} {
		if _, err := os.Stat(filepath.Join(inputDir, name)); err != nil {
			t.Fatal("inspection removed input", err)
		}
	}
	var inputs, tombstones, receipts, attempts int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM comfy_input_assets),(SELECT count(*) FROM comfy_output_cleanup_tombstones),(SELECT count(*) FROM comfy_input_cleanup_receipts)+(SELECT count(*) FROM comfy_output_cleanup_receipts),(SELECT sum(cleanup_attempts) FROM comfy_input_assets)+(SELECT sum(attempt_count) FROM comfy_output_cleanup_tombstones)`).Scan(&inputs, &tombstones, &receipts, &attempts); err != nil || inputs != 3 || tombstones != 1 || receipts != 0 || attempts != 0 {
		t.Fatal("inspection mutated registry", inputs, tombstones, receipts, attempts, err)
	}
	app.cfg.RestoreDrillMode = true
	w = comfyStorageRequest(app, "POST", "/admin/storage/comfy/inspect", string(request), actor, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"manual_cleanup_reason":"restore_drill"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	before := calls.Load()
	if _, err := db.Exec(`UPDATE comfy_input_assets SET content_hash=repeat('e',64) WHERE filename='kept.png'`); err != nil {
		t.Fatal(err)
	}
	w = comfyStorageRequest(app, "POST", "/admin/storage/comfy/inspect", string(request), actor, true)
	if w.Code != 409 || calls.Load() != before {
		t.Fatal("stale selection sent to host", w.Code, w.Body.String(), calls.Load())
	}
}
