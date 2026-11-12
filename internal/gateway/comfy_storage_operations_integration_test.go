package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updateagent"
	"ai-access-gateway/internal/updates"
)

type comfyStorageUnusedController struct{}

func (comfyStorageUnusedController) Status(context.Context) (updates.Status, error) {
	return updates.Status{}, updates.ErrUnavailable
}
func (comfyStorageUnusedController) Check(context.Context, updates.Request) (updates.Status, error) {
	return updates.Status{}, updates.ErrUnavailable
}
func (comfyStorageUnusedController) Install(context.Context, updates.Request) (updates.Status, error) {
	return updates.Status{}, updates.ErrUnavailable
}

type comfyStorageTestWindow struct {
	Protocol    int    `json:"protocol"`
	ID          string `json:"id"`
	RequestHash string `json:"request_hash"`
	ExecutorID  string `json:"executor_id"`
	JournalID   string `json:"journal_id"`
	State       string `json:"state"`
}

type comfyStorageHTTPFixture struct {
	app               *App
	db                *sql.DB
	actor             *User
	path              string
	contents          []byte
	selection         []store.ComfyStorageRegistrySelection
	executions        atomic.Int64
	reads             atomic.Int64
	fences            atomic.Int64
	statusCalls       atomic.Int64
	closeFails        atomic.Bool
	guardChanged      atomic.Bool
	statusUnavailable atomic.Bool
	host              atomic.Pointer[updateagent.Server]
	target            updateagent.ComfyTarget
}

func newComfyStorageHTTPFixture(t *testing.T, mode string) *comfyStorageHTTPFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, err := database.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	if _, err := db.Exec(`TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	f := &comfyStorageHTTPFixture{db: db, actor: &User{Role: "admin", Username: "comfy-operations-admin"}, contents: []byte("synthetic selected storage bytes")}
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES($1,'hash','admin') RETURNING id`, f.actor.Username).Scan(&f.actor.ID); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	windows := map[string]comfyStorageTestWindow{}
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-ai-gateway-gpu-key") != strings.Repeat("g", 32) {
			w.WriteHeader(403)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		journal := strings.Repeat("d", 32)
		if f.guardChanged.Load() {
			journal = strings.Repeat("e", 32)
		}
		if r.URL.Path == "/ai-gateway/gpu/status" {
			held := false
			for _, window := range windows {
				held = held || window.State == "held"
			}
			writeJSON(w, 200, map[string]any{"protocol": 1, "enforced": true, "instance_id": strings.Repeat("f", 32), "storage_protocol": 1, "storage_journal_id": journal, "storage_held": held})
			return
		}
		var request struct {
			Protocol       int    `json:"protocol"`
			Action         string `json:"action"`
			ID             string `json:"id"`
			RequestHash    string `json:"request_hash"`
			ExecutorID     string `json:"executor_id"`
			JournalID      string `json:"journal_id"`
			ExecutorFenced bool   `json:"executor_fenced"`
		}
		if r.URL.Path != "/ai-gateway/storage/window" || r.Method != "POST" || json.NewDecoder(r.Body).Decode(&request) != nil || request.JournalID != journal {
			w.WriteHeader(409)
			return
		}
		window, found := windows[request.ID]
		if !found {
			window = comfyStorageTestWindow{Protocol: 1, ID: request.ID, RequestHash: request.RequestHash, ExecutorID: request.ExecutorID, JournalID: journal, State: "unknown"}
		}
		if window.RequestHash != request.RequestHash || window.ExecutorID != request.ExecutorID {
			w.WriteHeader(409)
			return
		}
		switch request.Action {
		case "hold":
			if window.State == "unknown" {
				for _, other := range windows {
					if other.State == "held" {
						w.WriteHeader(409)
						return
					}
				}
				window.State = "held"
			}
		case "close":
			if !request.ExecutorFenced {
				t.Error("guard closed before executor fence")
				w.WriteHeader(400)
				return
			}
			if f.closeFails.Load() {
				w.WriteHeader(503)
				return
			}
			window.State = "closed"
		case "read":
		default:
			w.WriteHeader(400)
			return
		}
		windows[request.ID] = window
		writeJSON(w, 200, window)
	}))
	t.Cleanup(guard.Close)
	root := t.TempDir()
	working := filepath.Join(root, "comfy")
	journal := filepath.Join(root, "journal")
	for _, dir := range []string{filepath.Join(working, "input"), filepath.Join(working, "output"), journal} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.path = filepath.Join(working, "input", "selected.png")
	if err := os.WriteFile(f.path, f.contents, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(f.contents)
	hash := hex.EncodeToString(digest[:])
	if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,user_id,filename,size_bytes,content_hash,state,expires_at) VALUES($1,$2,'selected.png',$3,$4,'stored',now()-interval '1 hour')`, strings.Repeat("1", 32), f.actor.ID, len(f.contents), hash); err != nil {
		t.Fatal(err)
	}
	f.target = updateagent.ComfyTarget{WorkingDirectory: working, Storage: &updateagent.ComfyStorageConfig{JournalDirectory: journal, GuardURL: guard.URL + "/ai-gateway/storage/window", GuardToken: strings.Repeat("g", 32)}}
	f.restart(t)
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/comfy-storage":
			f.statusCalls.Add(1)
			if f.statusUnavailable.Load() {
				w.WriteHeader(503)
				return
			}
		case r.URL.Path == "/v1/comfy-storage/fence":
			f.fences.Add(1)
		case strings.HasPrefix(r.URL.Path, "/v1/comfy-storage/operations/"):
			f.reads.Add(1)
		case r.URL.Path == "/v1/comfy-storage/execute":
			f.executions.Add(1)
			if mode == "never_received" {
				http.Error(w, "synthetic response loss before receipt", 503)
				return
			}
			if mode == "lost_response" {
				response := httptest.NewRecorder()
				f.host.Load().Handler().ServeHTTP(response, r)
				if response.Code != 200 {
					t.Error("host execution failed", response.Code, response.Body.String())
				}
				http.Error(w, "synthetic response loss after receipt", 503)
				return
			}
		default:
			t.Error("unexpected agent route", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		f.host.Load().Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(agent.Close)
	base, _ := url.Parse(agent.URL)
	f.app = &App{store: store.New(db), updates: updates.NewClient(base, strings.Repeat("a", 32)), csrfSigner: security.NewCSRFSigner("comfy-operation-test-secret")}
	f.app.cfg.ComfyStorageCleanupEnabled = true
	f.app.cfg.ComfyGPUGuardRequired = true
	view, err := f.app.store.ComfyStorageInventory(context.Background(), store.ComfyStorageQuery{Limit: 50})
	if err != nil || len(view.Items) != 1 {
		t.Fatal(view, err)
	}
	f.selection = []store.ComfyStorageRegistrySelection{{Key: view.Items[0].Key, Version: view.Items[0].Version}}
	return f
}

func (f *comfyStorageHTTPFixture) restart(t *testing.T) {
	t.Helper()
	host, err := updateagent.NewServer(strings.Repeat("a", 32), comfyStorageUnusedController{}, f.target)
	if err != nil {
		t.Fatal(err)
	}
	f.host.Store(host)
}

func (f *comfyStorageHTTPFixture) create(t *testing.T, id string) store.ComfyStorageOperation {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"id": id, "items": f.selection})
	w := comfyStorageRequest(f.app, "POST", "/admin/storage/comfy/operations", string(payload), f.actor, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Operation store.ComfyStorageOperation     `json:"operation"`
		Files     []updates.ComfyStorageSelection `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Operation.State != "preview" || len(response.Files) != 1 {
		t.Fatal(response, err)
	}
	if strings.Contains(w.Body.String(), `"executor_id"`) || strings.Contains(w.Body.String(), `"host_key":"`+f.app.updates.ComfyStorageHostKey()) {
		t.Fatal("exposed internal host identity", w.Body.String())
	}
	return response.Operation
}

func (f *comfyStorageHTTPFixture) confirm(op store.ComfyStorageOperation) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(map[string]any{"confirm": true, "request_hash": op.RequestHash})
	return comfyStorageRequest(f.app, "POST", "/admin/storage/comfy/operations/"+op.ID+"/confirm", string(payload), f.actor, true)
}

func TestComfyStorageOperationHTTPRecoveryIntegration(t *testing.T) {
	for _, mode := range []string{"success", "lost_response", "never_received", "close_failed", "write_failed"} {
		t.Run(mode, func(t *testing.T) {
			f := newComfyStorageHTTPFixture(t, mode)
			op := f.create(t, strings.Repeat("2", 32))
			if mode == "close_failed" {
				f.closeFails.Store(true)
			}
			if mode == "write_failed" {
				if _, err := f.db.Exec(`CREATE FUNCTION fail_comfy_http_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.finished_at IS NOT NULL THEN RAISE EXCEPTION 'fixture write failure'; END IF; RETURN NEW; END $$;
			CREATE TRIGGER fail_comfy_http_settlement BEFORE UPDATE ON comfy_storage_operations FOR EACH ROW EXECUTE FUNCTION fail_comfy_http_settlement()`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					f.db.Exec(`DROP TRIGGER IF EXISTS fail_comfy_http_settlement ON comfy_storage_operations;DROP FUNCTION IF EXISTS fail_comfy_http_settlement()`)
				})
			}
			w := f.confirm(op)
			want := 200
			if mode != "success" {
				want = 202
			}
			if w.Code != want || f.executions.Load() != 1 {
				t.Fatal(w.Code, w.Body.String(), f.executions.Load())
			}
			if mode == "never_received" {
				if data, err := os.ReadFile(f.path); err != nil || string(data) != string(f.contents) {
					t.Fatal("unreceived request mutated file", err)
				}
			} else if _, err := os.Stat(f.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("expected host deletion", err)
			}
			f.statusUnavailable.Store(true)
			w = f.confirm(op)
			if w.Code != want || f.executions.Load() != 1 {
				t.Fatal("confirmation retried execute", w.Code, w.Body.String())
			}
			f.statusUnavailable.Store(false)
			if mode != "success" {
				if blocked, err := f.app.store.TrainingStorageAdmissionBlocked(context.Background(), domain.GPUPrimaryResource); err != nil || !blocked {
					t.Fatal("lost durable claim", blocked, err)
				}
				if mode == "write_failed" {
					var receipts int
					if err := f.db.QueryRow(`SELECT count(*) FROM comfy_input_cleanup_receipts`).Scan(&receipts); err != nil || receipts != 0 {
						t.Fatal("partial DB commit", receipts, err)
					}
					if _, err := f.db.Exec(`DROP TRIGGER fail_comfy_http_settlement ON comfy_storage_operations;DROP FUNCTION fail_comfy_http_settlement()`); err != nil {
						t.Fatal(err)
					}
				}
				f.closeFails.Store(false)
				f.restart(t)
				// Recovery remains enabled after the new-operation switch is disabled.
				f.app.cfg.ComfyStorageCleanupEnabled = false
				w = comfyStorageRequest(f.app, "POST", "/admin/storage/comfy/operations/"+op.ID+"/reconcile", "{}", f.actor, true)
				if w.Code != 200 || f.executions.Load() != 1 {
					t.Fatal(w.Code, w.Body.String(), f.executions.Load())
				}
			}
			settled, err := f.app.store.ComfyStorageOperation(context.Background(), op.ID)
			wantBytes := int64(len(f.contents))
			if mode == "never_received" {
				wantBytes = 0
			}
			if err != nil || settled.Active() || settled.FreedBytes != wantBytes {
				t.Fatal(settled, err)
			}
			if mode == "never_received" {
				if settled.State != "rejected" || f.fences.Load() != 1 {
					t.Fatal(settled, f.fences.Load())
				}
				executor, err := updateagent.NewComfyStorageExecutor(f.target)
				if err != nil {
					t.Fatal(err)
				}
				late, err := executor.Execute(context.Background(), *settled.Request)
				if err != nil || late.State != "rejected" {
					t.Fatal(late, err)
				}
				if data, err := os.ReadFile(f.path); err != nil || string(data) != string(f.contents) {
					t.Fatal("late original execute removed file", err)
				}
			}
			if blocked, err := f.app.store.TrainingStorageAdmissionBlocked(context.Background(), domain.GPUPrimaryResource); err != nil || blocked {
				t.Fatal(blocked, err)
			}
		})
	}
}

func TestComfyStorageOperationChangedHostAndRestoreDrillIntegration(t *testing.T) {
	f := newComfyStorageHTTPFixture(t, "never_received")
	op := f.create(t, strings.Repeat("2", 32))
	if w := f.confirm(op); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.guardChanged.Store(true)
	w := comfyStorageRequest(f.app, "POST", "/admin/storage/comfy/operations/"+op.ID+"/reconcile", "{}", f.actor, true)
	if w.Code != 202 || f.reads.Load() != 0 || f.fences.Load() != 0 || !strings.Contains(w.Body.String(), `"last_code":"host_changed"`) {
		t.Fatal(w.Code, w.Body.String(), f.reads.Load(), f.fences.Load())
	}
	f.guardChanged.Store(false)
	f.app.cfg.RestoreDrillMode = true
	before := f.statusCalls.Load()
	w = comfyStorageRequest(f.app, "POST", "/admin/storage/comfy/operations/"+op.ID+"/reconcile", "{}", f.actor, true)
	if w.Code != 403 || f.statusCalls.Load() != before {
		t.Fatal(w.Code, w.Body.String())
	}
	if checked, err := f.app.reconcileComfyStorageOperations(context.Background()); err != nil || checked != 0 || f.statusCalls.Load() != before {
		t.Fatal(checked, err)
	}
	f.app.cfg.RestoreDrillMode = false
	if _, err := f.db.Exec(`UPDATE comfy_storage_operations SET dispatched_at=now()-interval '4 minutes' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if checked, err := f.app.reconcileComfyStorageOperations(context.Background()); err != nil || checked != 1 || f.fences.Load() != 1 {
		t.Fatal(checked, err)
	}
	w = comfyStorageRequest(f.app, "GET", "/admin/storage/comfy/operations?offset=0", "", f.actor, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":1`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestComfyStorageOperationStalePreviewAndLegacyIsolationIntegration(t *testing.T) {
	f := newComfyStorageHTTPFixture(t, "success")
	op := f.create(t, strings.Repeat("2", 32))
	if _, err := f.db.Exec(`UPDATE comfy_input_assets SET content_hash=repeat('f',64)`); err != nil {
		t.Fatal(err)
	}
	if w := f.confirm(op); w.Code != 409 || f.executions.Load() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := f.db.Exec(`INSERT INTO comfy_output_cleanup_tombstones(filename,storage_type,size_bytes,content_hash) VALUES('expired.png','output',1,repeat('a',64))`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.deleteExpiredComfyInputs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.deleteExpiredComfyMedia(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.executions.Load() != 0 || f.fences.Load() != 0 {
		t.Fatal("cleanup fell back to a host mutation")
	}
	if _, err := os.Stat(f.path); err != nil {
		t.Fatal(err)
	}
	w := comfyStorageRequest(f.app, "GET", "/admin/storage/comfy", "", f.actor, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"manual_cleanup_available":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestComfyStorageOperationHTTPInputValidation(t *testing.T) {
	a := &App{csrfSigner: security.NewCSRFSigner("comfy-storage-validation")}
	admin := &User{ID: 1, Role: "admin"}
	id := strings.Repeat("2", 32)
	root := "/admin/storage/comfy/operations"
	for _, tc := range []struct {
		method, path, body string
		actor              *User
		csrf               bool
		status             int
	}{
		{"POST", root, `{}`, nil, false, 403},
		{"POST", root, `{}`, &User{ID: 2, Role: "user"}, true, 403},
		{"POST", root, `{}`, admin, false, 403},
		{"DELETE", root, `{}`, admin, true, 405},
		{"POST", root + "/" + id, `{}`, admin, true, 405},
		{"POST", root + "/" + id + "/force", `{}`, admin, true, 404},
		{"POST", root + "/" + id + "/cancel/", `{}`, admin, true, 404},
		{"POST", root, `{"id":"` + id + `","items":[]}`, admin, true, 400},
		{"POST", root, `{"id":"` + id + `","items":[{"key":"cleanup:1","version":"` + strings.Repeat("a", 64) + `"}]} {}`, admin, true, 400},
		{"POST", root + "/" + id + "/confirm", `{"confirm":false,"request_hash":"` + strings.Repeat("a", 64) + `"}`, admin, true, 400},
		{"POST", root + "/" + id + "/reconcile", `{"force":true}`, admin, true, 400},
		{"GET", root + "/" + id + "?force=true", "", admin, false, 400},
		{"GET", root + "?offset=01", "", admin, false, 400},
		{"GET", root + "?offset=0&offset=1", "", admin, false, 400},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			w := comfyStorageRequest(a, tc.method, tc.path, tc.body, tc.actor, tc.csrf)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	valid := `{"id":"` + id + `","items":[{"key":"cleanup:1","version":"` + strings.Repeat("a", 64) + `"}]}`
	w := comfyStorageRequest(a, "POST", root, valid, admin, true)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "storage_cleanup_disabled") {
		t.Fatal(w.Code, w.Body.String())
	}
	a.cfg.RestoreDrillMode = true
	w = comfyStorageRequest(a, "POST", root, valid, admin, true)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "storage_read_only") {
		t.Fatal(w.Code, w.Body.String())
	}
}
