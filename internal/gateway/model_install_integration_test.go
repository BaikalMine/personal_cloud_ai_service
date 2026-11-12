package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/modelmanager"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

func TestModelInstallWorkerLostResponse(t *testing.T) {
	for _, lostProgress := range []bool{false, true} {
		name := "progress retained"
		if lostProgress {
			name = "progress cache lost"
		}
		t.Run(name, func(t *testing.T) { testModelInstallRecovery(t, lostProgress) })
	}
}

func testModelInstallRecovery(t *testing.T, lostProgress bool) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `TRUNCATE model_install_jobs,model_install_requests`); err != nil {
		t.Fatal(err)
	}
	var owner int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('install-worker-admin','test','admin') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, owner) }()
	var starts atomic.Int32
	lm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/lm/download-model":
			starts.Add(1)
			var d modelmanager.Download
			if json.NewDecoder(r.Body).Decode(&d) != nil || d.Root != "C:/ComfyUI/models/loras" || d.ID != strings.Repeat("1", 32) {
				t.Error("unexpected installation payload")
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				connection.Close()
			}
		case strings.HasPrefix(r.URL.Path, "/api/lm/download-progress/"):
			if lostProgress {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"status":"completed","progress":100}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer lm.Close()
	lmURL, _ := url.Parse(lm.URL)
	var verified atomic.Bool
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
			t.Error("unexpected capacity request")
		}
		if r.URL.Path == "/v1/model-verify" {
			var in updates.ModelVerifyRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Root != "C:/ComfyUI/models/loras" || in.RelativePath != "Krea2/fixture.safetensors" || in.SHA256 != strings.Repeat("2", 64) {
				t.Error("verification identity changed")
			}
			state := "missing"
			if verified.Load() {
				state = "verified"
			}
			_ = json.NewEncoder(w).Encode(updates.ModelVerification{Protocol: 1, State: state, Size: 1024})
			return
		}
		if r.URL.Path != "/v1/model-capacity" {
			t.Error("unexpected agent path")
		}
		_, _ = w.Write([]byte(`{"protocol":1,"free_bytes":1099511627776}`))
	}))
	defer agent.Close()
	agentURL, _ := url.Parse(agent.URL)
	repo := store.New(db)
	a := &App{store: repo, cfg: Config{ComfyUIUpstream: lmURL}, updates: updates.NewClient(agentURL, strings.Repeat("t", 32))}
	d := modelmanager.Download{ID: strings.Repeat("1", 32), VersionID: 12, Root: "C:/ComfyUI/models/loras", RelativePath: "Krea2"}
	payload, _ := json.Marshal(d)
	j := store.ModelInstall{ID: d.ID, OwnerID: owner, ModelID: 10, VersionID: 12, Kind: "loras", Name: "Fixture", Filename: "fixture.safetensors", SHA256: strings.Repeat("2", 64), Size: 1024, Request: payload}
	browser, cancelBrowser := context.WithCancel(ctx)
	if _, err = repo.EnqueueModelInstall(browser, j); err != nil {
		t.Fatal(err)
	}
	cancelBrowser()
	if _, err = a.runModelInstall(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ModelInstallReceipt(ctx, j.ID)
	if err != nil || job.State != "reconciling" {
		t.Fatalf("unknown outcome: %+v %v", job, err)
	}
	restarted := &App{store: store.New(db), cfg: a.cfg, updates: a.updates}
	if _, err = restarted.runModelInstall(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.refreshModelInstallProgress(ctx); err != nil {
		t.Fatal(err)
	}
	job, err = repo.ModelInstallReceipt(ctx, j.ID)
	if err != nil || job.State != "reconciling" {
		t.Fatal("download status bypassed file verification", job.State, err)
	}
	if _, err = restarted.verifyModelInstall(ctx); err != nil {
		t.Fatal(err)
	}
	job, err = repo.ModelInstallReceipt(ctx, j.ID)
	if err != nil || job.State != "reconciling" {
		t.Fatal("missing file marked installed", job.State, err)
	}
	verified.Store(true)
	if _, err = db.ExecContext(ctx, `UPDATE model_install_jobs SET verification_after=now() WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.verifyModelInstall(ctx); err != nil {
		t.Fatal(err)
	}
	job, err = repo.ModelInstallReceipt(ctx, j.ID)
	if err != nil || job.State != "completed" || starts.Load() != 1 {
		t.Fatalf("recovery %+v starts=%d err=%v", job, starts.Load(), err)
	}
}
