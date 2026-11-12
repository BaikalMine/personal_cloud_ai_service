package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestLoraPassportPublicBoundary(t *testing.T) {
	app := &App{}
	for _, test := range []struct {
		method string
		user   *User
		want   int
	}{
		{"GET", nil, 401}, {"POST", nil, 405}, {"GET", &User{ID: 1}, 403},
		{"GET", &User{ID: 1, CanTrainImageLora: true, Disabled: true}, 401},
		{"GET", &User{ID: 1, CanTrainImageLora: true, AccountExpiresAt: sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}}, 401},
	} {
		r := httptest.NewRequest(test.method, "/api/lora-training/passports/fixture", nil)
		if test.user != nil {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, test.user))
		}
		w := httptest.NewRecorder()
		app.publicMux().ServeHTTP(w, r)
		if w.Code != test.want || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Location") != "" {
			t.Fatalf("public boundary: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestLoraPassportViewDoesNotInventLegacyEvidence(t *testing.T) {
	job := domain.LoraTrainingJob{PublicID: "fixture", Seed: (1 << 62) + 14, ProfileID: "old-profile",
		BaseModel: "old-base", DatasetPath: "do-not-expose-server-path", DispatchToken: "do-not-expose-token",
		State: domain.LoraTrainingCompleted, ArtifactName: "old-adapter.safetensors"}
	view := loraPassport(job)
	data, _ := json.Marshal(view)
	if view.Execution != nil || view.EvidenceState != "not_recorded" || view.Requested.Seed != strconv.FormatInt(job.Seed, 10) ||
		!strings.Contains(string(data), `"seed":"4611686018427387918"`) || strings.Contains(string(data), "do-not-expose") {
		t.Fatalf("legacy evidence invented or leaked: %s", data)
	}
	job.Provenance = &domain.LoraTrainingProvenance{Artifact: &domain.LoraFileIdentity{Name: "known.safetensors"}}
	view = loraPassport(job)
	if view.EvidenceState != "artifact_recorded" {
		t.Fatal("recorded artifact hidden")
	}
	view.Execution.Artifact.Name = "changed"
	if job.Provenance.Artifact.Name == "changed" {
		t.Fatal("view modifies stored evidence")
	}
}

func TestLoraPassportOwnershipIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
	var owner, other int64
	for name, id := range map[string]*int64{"passport-owner": &owner, "passport-other": &other} {
		if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,can_train_image_lora) VALUES($1,'disabled','user',true) RETURNING id`, name).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{store: store.New(db)}
	job, err := app.store.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{
		PublicID: "passport-owner-job", RequestID: "passport-owner-job", UserID: owner,
		ProfileID: "profile", Family: "krea2", BaseModel: "base", Name: "Fixture", OutputName: "fixture_output",
		TriggerWord: "fixture", ConceptType: "object", Preset: "quick", Resolution: 512, MaxTrainSteps: 100,
		NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.001, Seed: 1, SampleCount: 5, DatasetBytes: 1, DatasetPath: "private-host-path"})
	if err != nil {
		t.Fatal(err)
	}
	read := func(id int64, path string, want int) string {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: id, Role: "admin", CanTrainImageLora: true}))
		w := httptest.NewRecorder()
		app.handleLoraTrainingPassport(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("passport response %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	url := "/api/lora-training/passports/" + job.PublicID
	result := read(owner, url, 200)
	if strings.Contains(result, "private-host-path") || !strings.Contains(result, `"evidence_state":"not_recorded"`) || !strings.Contains(result, `"trigger":"fixture"`) {
		t.Fatal("incorrect passport projection", result)
	}
	if read(other, url, 404) != read(other, "/api/lora-training/passports/not-found", 404) {
		t.Fatal("foreign source distinguishable from missing")
	}
	read(owner, url+"?owner=other", 400)
	read(owner, url+"/extra", 400)
	usageRead := func(userID int64, path string, want int) string {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: userID, Role: "admin", CanTrainImageLora: true}))
		w := httptest.NewRecorder()
		app.handleLoraUsage(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("usage response %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	for _, prefix := range []string{"/api/lora-training/usage/", "/api/lora-training/source/"} {
		suffix, success := "", 200
		if strings.Contains(prefix, "/source/") {
			suffix, success = "?model=fixture", 409
		}
		usageRead(owner, prefix+job.PublicID+suffix, success)
		if usageRead(other, prefix+job.PublicID+suffix, 404) != usageRead(other, prefix+"not-found"+suffix, 404) {
			t.Fatal("foreign LoRA usage distinguishable from missing")
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO lora_datasets(id,user_id,name,manifest_cipher) VALUES('passport-dataset',$1,'Dataset','private-cipher')`, owner)
	exec(`INSERT INTO lora_dataset_snapshots(id,dataset_id,user_id,name,revision,manifest_cipher,manifest_hash,image_count,size_bytes,expires_at)
		VALUES('passport-snapshot','passport-dataset',$1,'Snapshot',1,'private-cipher',repeat('a',64),1,1,now()-interval '1 day')`, owner)
	exec(`UPDATE lora_training_jobs SET dataset_snapshot_id='passport-snapshot',dataset_snapshot_hash=repeat('a',64) WHERE id=$1`, job.ID)
	if !strings.Contains(read(owner, url, 200), `"dataset_snapshot_available":true`) {
		t.Fatal("retained referenced snapshot not available")
	}
	exec(`UPDATE lora_dataset_snapshots SET manifest_hash=repeat('b',64) WHERE id='passport-snapshot'`)
	if !strings.Contains(read(owner, url, 200), `"dataset_snapshot_available":false`) {
		t.Fatal("different snapshot content accepted")
	}
	exec(`DELETE FROM lora_dataset_snapshots WHERE id='passport-snapshot'`)
	if !strings.Contains(read(owner, url, 200), `"dataset_snapshot_available":false`) {
		t.Fatal("deleted snapshot still available")
	}
	exec(`UPDATE users SET can_train_image_lora=false WHERE id=$1`, owner)
	usageRead(owner, "/api/lora-training/usage/"+job.PublicID, 403)
	usageRead(owner, "/api/lora-training/source/"+job.PublicID+"?model=fixture", 403)
	exec(`UPDATE users SET can_train_image_lora=true,disabled=true WHERE id=$1`, owner)
	usageRead(owner, "/api/lora-training/usage/"+job.PublicID, 403)
}
