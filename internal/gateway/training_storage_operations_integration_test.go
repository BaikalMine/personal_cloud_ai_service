package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/trainingagent"
)

type trainingStorageHTTPFixture struct {
	app                       *App
	db                        *sql.DB
	actor                     *User
	client                    *loratraining.Client
	base                      *url.URL
	model, original, targetID string
	cleanupCalls              atomic.Int64
	fenceCalls                atomic.Int64
	previewCalls              atomic.Int64
}

func newTrainingStorageHTTPFixture(t *testing.T, mode string) *trainingStorageHTTPFixture {
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
	f := &trainingStorageHTTPFixture{db: db, actor: &User{Role: "admin", Username: "storage-http-admin"}}
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES($1,'hash','admin') RETURNING id`, f.actor.Username).Scan(&f.actor.ID); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	f.original = filepath.Join(root, "original.safetensors")
	f.model = filepath.Join(root, "training", "models", "storage-test-format-v2.safetensors")
	if err := os.WriteFile(f.original, []byte("original model remains untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	config := trainingagent.Config{RootDir: filepath.Join(root, "training"), ComfyLoraDir: filepath.Join(root, "loras"), Profiles: []trainingagent.ProfileConfig{{ID: "storage-test", Name: "Synthetic storage profile", Family: "krea2", BaseModel: "synthetic", DiT: f.original, StripPrefix: "model."}}}
	controller, err := trainingagent.NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	if err := os.WriteFile(f.model, []byte("rebuildable model bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("s", 32)
	server, err := trainingagent.NewServer(token, controller)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/storage/preview" {
			f.previewCalls.Add(1)
		}
		if r.URL.Path == "/v1/storage/fence" {
			f.fenceCalls.Add(1)
		}
		if r.URL.Path != "/v1/storage/cleanup" || f.cleanupCalls.Add(1) != 1 {
			handler.ServeHTTP(w, r)
			return
		}
		if mode == "not_received" {
			http.Error(w, "synthetic transport failure before dispatch", 503)
			return
		}
		if mode == "interrupted" {
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				http.Error(w, "fixture error", 500)
				return
			}
			var request loratraining.StorageCleanupRequest
			if err := json.Unmarshal(payload, &request); err != nil {
				t.Error(err)
				http.Error(w, "fixture error", 500)
				return
			}
			canonical, hash, err := loratraining.CanonicalStorageCleanupRequest(request)
			if err != nil {
				t.Error(err)
				http.Error(w, "fixture error", 500)
				return
			}
			intent := loratraining.StorageCleanupResult{ProtocolVersion: 1, ID: canonical.ID, RequestHash: hash, State: "running", StartedAt: time.Now().UTC()}
			for _, selection := range canonical.Items {
				kind, _, _ := strings.Cut(selection.ID, ":")
				intent.Items = append(intent.Items, loratraining.StorageCleanupOutcome{ID: selection.ID, Kind: kind, Label: "model", Version: selection.Version, Outcome: "not_run"})
			}
			journal := filepath.Join(config.RootDir, ".storage-cleanup")
			if err := os.MkdirAll(journal, 0700); err != nil {
				t.Error(err)
				http.Error(w, "fixture error", 500)
				return
			}
			raw, _ := json.Marshal(intent)
			temporary := filepath.Join(journal, canonical.ID+".fixture")
			if err := os.WriteFile(temporary, raw, 0600); err != nil {
				t.Error(err)
				http.Error(w, "fixture error", 500)
				return
			}
			if err := os.Rename(temporary, filepath.Join(journal, canonical.ID+".json")); err != nil {
				t.Error(err)
				http.Error(w, "fixture error", 500)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(payload))
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if mode == "lost_response" || mode == "host_changed" {
			http.Error(w, "synthetic lost response after deletion", 500)
			return
		}
		if mode == "invalid_response" {
			var result loratraining.StorageCleanupResult
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Items) != 1 {
				t.Error("fixture result", err)
				http.Error(w, "fixture error", 500)
				return
			}
			result.Items[0].FreedBytes++
			writeJSON(w, 200, result)
			return
		}
		for key, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	t.Cleanup(agent.Close)
	f.base, _ = url.Parse(agent.URL)
	f.client = loratraining.NewClient(f.base, token)
	f.app = &App{store: store.New(db), loraTraining: f.client, csrfSigner: security.NewCSRFSigner("storage-http-session-secret")}
	inventory, err := f.client.StorageInventory(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range inventory.Items {
		if item.Kind == "normalized_model" && item.State == "present" {
			f.targetID = item.ID
		}
	}
	if f.targetID == "" {
		t.Fatal("missing synthetic normalized model")
	}
	return f
}

func (f *trainingStorageHTTPFixture) call(t *testing.T, method, path string, input any, actor *User, csrf bool) (*httptest.ResponseRecorder, store.TrainingStorageOperation) {
	t.Helper()
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if actor != nil {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, actor))
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "storage-http-session"})
	}
	if csrf {
		r.Header.Set("X-CSRF-Token", f.app.csrfSigner.Token("storage-http-session"))
	}
	w := httptest.NewRecorder()
	f.app.handleAdminRoutes(w, r)
	var reply struct {
		Operation store.TrainingStorageOperation `json:"operation"`
	}
	if w.Code == 200 || w.Code == 202 {
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
	}
	return w, reply.Operation
}

func TestTrainingStorageOperationHTTPRecovery(t *testing.T) {
	for _, mode := range []string{"normal", "lost_response", "not_received", "invalid_response", "interrupted", "write_failure", "host_changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newTrainingStorageHTTPFixture(t, mode)
			ctx := context.Background()
			const root = "/admin/storage/training/operations"
			id := strings.Repeat("a", 32)
			input := map[string]any{"id": id, "ids": []string{f.targetID}}
			w, op := f.call(t, "POST", root, input, f.actor, true)
			if w.Code != 200 || op.State != "preview" || op.ItemCount != 1 {
				t.Fatal(w.Code, w.Body.String())
			}
			if f.previewCalls.Load() != 1 {
				t.Fatal("preview call count", f.previewCalls.Load())
			}
			if _, err := os.Stat(f.model); err != nil {
				t.Fatal("preview deleted synthetic model", err)
			}
			if mode == "write_failure" {
				if _, err := f.db.Exec(`CREATE FUNCTION storage_http_write_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='completed' THEN RAISE EXCEPTION 'synthetic write failure'; END IF; RETURN NEW; END $$;
				CREATE TRIGGER storage_http_write_failure BEFORE UPDATE ON training_storage_operations FOR EACH ROW EXECUTE FUNCTION storage_http_write_failure()`); err != nil {
					t.Fatal(err)
				}
			}
			confirm := map[string]any{"request_hash": op.RequestHash, "confirm": true}
			w, op = f.call(t, "POST", root+"/"+id+"/confirm", confirm, f.actor, true)
			want := 202
			if mode == "normal" {
				want = 200
			}
			if w.Code != want || f.cleanupCalls.Load() != 1 {
				t.Fatal(w.Code, w.Body.String(), f.cleanupCalls.Load())
			}
			w, _ = f.call(t, "POST", root+"/"+id+"/confirm", confirm, f.actor, true)
			if w.Code != want || f.cleanupCalls.Load() != 1 {
				t.Fatal("duplicate confirm dispatched again", w.Code, f.cleanupCalls.Load())
			}
			if mode == "write_failure" {
				if _, err := f.db.Exec(`DROP TRIGGER storage_http_write_failure ON training_storage_operations;DROP FUNCTION storage_http_write_failure()`); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "normal" {
				if blocked, err := f.app.store.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || !blocked {
					t.Fatal("unknown deletion released gate", blocked, err)
				}
				if op.FreedBytes != 0 {
					t.Fatal("unknown result credited bytes", op)
				}
			}
			if mode == "host_changed" {
				f.app.loraTraining = loratraining.NewClient(f.base, strings.Repeat("z", 32))
				w, changed := f.call(t, "POST", root+"/"+id+"/reconcile", map[string]any{}, f.actor, true)
				if w.Code != 202 || changed.LastCode != "host_changed" || f.cleanupCalls.Load() != 1 {
					t.Fatal(w.Code, changed)
				}
				f.app.loraTraining = f.client
			}
			w, op = f.call(t, "POST", root+"/"+id+"/reconcile", map[string]any{}, f.actor, true)
			state := "completed"
			if mode == "not_received" {
				state = "rejected"
			}
			if mode == "interrupted" {
				state = "closed_unconfirmed"
			}
			if w.Code != 200 || op.State != state || f.cleanupCalls.Load() != 1 {
				t.Fatal(w.Code, w.Body.String(), f.cleanupCalls.Load())
			}
			if mode == "not_received" || mode == "interrupted" {
				if op.FreedBytes != 0 || f.fenceCalls.Load() != 1 {
					t.Fatal("fence accounting", op, f.fenceCalls.Load())
				}
				if _, err := os.Stat(f.model); err != nil {
					t.Fatal("fencing deleted model", err)
				}
				stored, err := f.app.store.TrainingStorageOperation(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.client.CleanupStorage(ctx, *stored.Request); err != nil {
					t.Fatal("late host request", err)
				}
				if _, err := os.Stat(f.model); err != nil {
					t.Fatal("late request deleted rebuilt path", err)
				}
			} else {
				if op.FreedBytes != int64(len("rebuildable model bytes")) || op.DeletedFiles != 1 {
					t.Fatal("confirmed bytes", op)
				}
				if _, err := os.Stat(f.model); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("model not removed", err)
				}
			}
			if original, err := os.ReadFile(f.original); err != nil || string(original) != "original model remains untouched" {
				t.Fatal("original changed", err)
			}
			if blocked, err := f.app.store.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || blocked {
				t.Fatal("settled operation retained gate", blocked, err)
			}
			w, replay := f.call(t, "POST", root, input, f.actor, true)
			if w.Code != 200 || replay.State != state || f.previewCalls.Load() != 1 {
				t.Fatal("replay re-scanned/reopened operation", w.Code, replay, f.previewCalls.Load())
			}
		})
	}
}

func TestTrainingStorageOperationHTTPAuthorizationAndConfirmation(t *testing.T) {
	f := newTrainingStorageHTTPFixture(t, "normal")
	const root = "/admin/storage/training/operations"
	id := strings.Repeat("b", 32)
	input := map[string]any{"id": id, "ids": []string{f.targetID}}
	for _, actor := range []*User{nil, {Role: "user"}} {
		w, _ := f.call(t, "POST", root, input, actor, true)
		if w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w, _ := f.call(t, "POST", root, input, f.actor, false)
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	w, op := f.call(t, "POST", root, input, f.actor, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []map[string]any{{"request_hash": op.RequestHash}, {"request_hash": op.RequestHash, "confirm": false}, {"request_hash": op.RequestHash, "confirm": true, "ids": []string{f.targetID}}} {
		w, _ := f.call(t, "POST", root+"/"+id+"/confirm", body, f.actor, true)
		if w.Code != 400 {
			t.Fatal("unconfirmed mutation", w.Code, w.Body.String())
		}
	}
	w, _ = f.call(t, "POST", root+"/"+id+"/confirm", map[string]any{"request_hash": strings.Repeat("f", 64), "confirm": true}, f.actor, true)
	if w.Code != 409 {
		t.Fatal("stale confirmation", w.Code, w.Body.String())
	}
	w, op = f.call(t, "POST", root+"/"+id+"/cancel", map[string]any{}, f.actor, true)
	if w.Code != 200 || op.State != "cancelled" {
		t.Fatal(w.Code, w.Body.String())
	}
	w, op = f.call(t, "POST", root+"/"+id+"/confirm", map[string]any{"request_hash": op.RequestHash, "confirm": true}, f.actor, true)
	if w.Code != 200 || op.State != "cancelled" || f.cleanupCalls.Load() != 0 {
		t.Fatal("cancelled preview dispatched", w.Code, op, f.cleanupCalls.Load())
	}
	w, _ = f.call(t, "GET", root+"?offset=01", nil, f.actor, false)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	w, _ = f.call(t, "GET", root+"?offset=0", nil, f.actor, false)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(f.model); err != nil {
		t.Fatal("rejected requests changed host files", err)
	}
}

func TestTrainingStorageOperationRestoreDrillBlocksHostMutations(t *testing.T) {
	f := newTrainingStorageHTTPFixture(t, "normal")
	ctx := context.Background()
	id := strings.Repeat("c", 32)
	op, err := f.app.prepareTrainingStorageOperation(ctx, f.actor.ID, id, []string{f.targetID})
	if err != nil {
		t.Fatal(err)
	}
	op, claimed, err := f.app.store.DispatchTrainingStorageOperation(ctx, f.actor.ID, id, f.client.StorageHostKey(), op.RequestHash)
	if err != nil || !claimed {
		t.Fatal(op, claimed, err)
	}
	f.app.cfg.RestoreDrillMode = true
	for _, action := range []string{"confirm", "cancel", "reconcile"} {
		input := map[string]any{}
		if action == "confirm" {
			input = map[string]any{"request_hash": op.RequestHash, "confirm": true}
		}
		w, _ := f.call(t, "POST", "/admin/storage/training/operations/"+id+"/"+action, input, f.actor, true)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "storage_read_only") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if _, err := f.app.confirmTrainingStorageOperation(ctx, f.actor.ID, id, op.RequestHash); !errors.Is(err, errTrainingStorageReadOnly) {
		t.Fatal(err)
	}
	if _, err := f.app.reconcileTrainingStorageOperation(ctx, op); !errors.Is(err, errTrainingStorageReadOnly) {
		t.Fatal(err)
	}
	if _, err := f.app.prepareTrainingStorageOperation(ctx, f.actor.ID, strings.Repeat("d", 32), []string{f.targetID}); !errors.Is(err, errTrainingStorageReadOnly) {
		t.Fatal(err)
	}
	if count, err := f.app.reconcileTrainingStorageOperations(ctx); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if f.cleanupCalls.Load() != 0 || f.fenceCalls.Load() != 0 {
		t.Fatal("restore drill mutated host", f.cleanupCalls.Load(), f.fenceCalls.Load())
	}
	if blocked, err := f.app.store.TrainingStorageAdmissionBlocked(ctx, domain.GPUPrimaryResource); err != nil || !blocked {
		t.Fatal("restore drill released durable gate", blocked, err)
	}
	if _, err := os.Stat(f.model); err != nil {
		t.Fatal(err)
	}
}

func TestTrainingStorageOperationWorkerRecoversAfterRestart(t *testing.T) {
	for _, sent := range []bool{false, true} {
		name := "before_host_request"
		if sent {
			name = "after_host_result"
		}
		t.Run(name, func(t *testing.T) {
			f := newTrainingStorageHTTPFixture(t, "normal")
			ctx := context.Background()
			id := strings.Repeat("e", 32)
			op, err := f.app.prepareTrainingStorageOperation(ctx, f.actor.ID, id, []string{f.targetID})
			if err != nil {
				t.Fatal(err)
			}
			op, claimed, err := f.app.store.DispatchTrainingStorageOperation(ctx, f.actor.ID, id, f.client.StorageHostKey(), op.RequestHash)
			if err != nil || !claimed {
				t.Fatal(op, claimed, err)
			}
			restarted := &App{store: store.New(f.db), loraTraining: f.client}
			if count, err := restarted.reconcileTrainingStorageOperations(ctx); err != nil || count != 0 || f.fenceCalls.Load() != 0 {
				t.Fatal("worker preempted fresh request", count, err)
			}
			if sent {
				if _, err := f.client.CleanupStorage(ctx, *op.Request); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.db.Exec(`UPDATE training_storage_operations SET dispatched_at=now()-interval '4 minutes' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			if count, err := restarted.reconcileTrainingStorageOperations(ctx); err != nil || count != 1 {
				t.Fatal(count, err)
			}
			result, err := restarted.store.TrainingStorageOperation(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if sent {
				if result.State != "completed" || result.FreedBytes != int64(len("rebuildable model bytes")) || f.cleanupCalls.Load() != 1 || f.fenceCalls.Load() != 0 {
					t.Fatal(result, f.cleanupCalls.Load(), f.fenceCalls.Load())
				}
			} else {
				if result.State != "rejected" || result.FreedBytes != 0 || f.cleanupCalls.Load() != 0 || f.fenceCalls.Load() != 1 {
					t.Fatal(result, f.cleanupCalls.Load(), f.fenceCalls.Load())
				}
				if _, err := os.Stat(f.model); err != nil {
					t.Fatal(err)
				}
			}
			if count, err := restarted.reconcileTrainingStorageOperations(ctx); err != nil || count != 0 {
				t.Fatal("settled operation recovered again", count, err)
			}
		})
	}
}
