package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestLoraRepeatConfirmationPurposeAndIntegrity(t *testing.T) {
	cipher, err := contentcrypto.NewCipher("repeat-confirmation-test-secret-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{contentCipher: cipher}
	value := loraRepeatConfirmation{Purpose: "lora-repeat-v1", UserID: 1, RequestID: "repeat-request-0001", SourceID: "repeat-source-0001", SourceSHA256: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute).Unix(),
		Constraint: &domain.LoraTrainingRepeatConstraint{Version: 1, SourceJobID: "repeat-source-0001", ProfileSHA256: strings.Repeat("b", 64), Parameters: domain.LoraExecutionParameters{Resolution: 512, MaxSteps: 100, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.001, Seed: 14, SampleCount: 5}}}
	token, err := app.encodeLoraRepeatConfirmation(value)
	if err != nil {
		t.Fatal(err)
	}
	got, err := app.decodeLoraRepeatConfirmation(token)
	if err != nil || got.Constraint.Parameters != value.Constraint.Parameters {
		t.Fatal(got, err)
	}
	for _, bad := range []string{token[:len(token)/2] + "broken", "not/base64", strings.Repeat("a", 32769)} {
		if _, err := app.decodeLoraRepeatConfirmation(bad); err == nil {
			t.Fatal("bad token accepted")
		}
	}
	value.Purpose = "another-purpose"
	token, _ = app.encodeLoraRepeatConfirmation(value)
	if _, err := app.decodeLoraRepeatConfirmation(token); err == nil {
		t.Fatal("purpose not checked")
	}
}

func TestLoraRepeatAPIUsesExactEncryptedSnapshotIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	f.app.csrfSigner = security.NewCSRFSigner("repeat-csrf-fixture")
	manifest := domain.LoraDatasetManifest{Version: 1, Settings: domain.LoraDatasetSettings{Name: "Original dataset", ProfileID: "repeat-profile", OutputName: "repeat_source", TriggerWord: "fixture", ConceptType: "object", Resolution: 768, Preset: "balanced"}}
	var assets []string
	for i := 0; i < 5; i++ {
		asset, err := f.app.persistLoraDatasetImage(ctx, f.user.ID, fmt.Sprintf("image-%d.png", i), bytes.NewReader(datasetTestPNG(t, 256+i, 256)))
		if err != nil {
			t.Fatal(err)
		}
		assets = append(assets, asset.ID)
		manifest.Images = append(manifest.Images, domain.LoraDatasetImage{ID: fmt.Sprintf("image-%d", i), AssetID: asset.ID, Caption: fmt.Sprintf("fixture original caption %d", i)})
	}
	cipher, hash, err := f.app.encryptLoraDatasetManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := f.app.store.CreateLoraDataset(ctx, f.user.ID, "repeat-dataset-0001", manifest.Settings.Name, cipher)
	if err != nil {
		t.Fatal(err)
	}
	dataset, err = f.app.store.SaveLoraDataset(ctx, f.user.ID, dataset.ID, dataset.Revision, dataset.Name, cipher, assets)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.app.store.CreateLoraDatasetSnapshot(ctx, f.user.ID, dataset.ID, dataset.Revision, "repeat-snapshot-0001", hash)
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.app.store.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{PublicID: "repeat-source-0001", RequestID: "repeat-source-0001", UserID: f.user.ID, UsernameSnapshot: f.user.Username,
		ProfileID: "repeat-profile", Family: "krea2", BaseModel: "base.safetensors", Name: "Original training", OutputName: "repeat_source", TriggerWord: "fixture", ConceptType: "object", Preset: "balanced",
		Resolution: 768, MaxTrainSteps: 1234, NetworkDim: 16, NetworkAlpha: 8, LearningRate: 0.00012, Seed: 1<<40 + 19, SampleCount: 5, DatasetBytes: 100, DatasetPath: "original-archive-already-removed.zip", DatasetSnapshotID: snapshot.ID, DatasetSnapshotHash: snapshot.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.UpdateLoraTrainingJob(ctx, source.ID, store.UpdateLoraTrainingJobParams{State: domain.LoraTrainingFailed, Stage: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	// The editable dataset now differs. Repeating the source must not use it.
	manifest.Settings.Name = "Changed working copy"
	for i := range manifest.Images {
		manifest.Images[i].Caption = "new caption"
		manifest.Images[i].Excluded = true
	}
	changedCipher, _, err := f.app.encryptLoraDatasetManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.SaveLoraDataset(ctx, f.user.ID, dataset.ID, dataset.Revision, manifest.Settings.Name, changedCipher, assets); err != nil {
		t.Fatal(err)
	}
	var protocol atomic.Int32
	protocol.Store(1)
	profileHash := (domain.LoraExecutionProfile{ID: source.ProfileID, Family: source.Family, BaseModel: source.BaseModel}).Hash()
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/profiles" {
			t.Errorf("unexpected training execution %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(loratraining.ProfilesResponse{Available: true, Profiles: []loratraining.Profile{{ID: source.ProfileID, Family: source.Family, BaseModel: source.BaseModel, Ready: true, RepeatProtocol: int(protocol.Load()), ProfileSHA256: profileHash}}})
	}))
	defer agent.Close()
	agentURL, _ := url.Parse(agent.URL)
	f.app.loraTraining = loratraining.NewClient(agentURL, strings.Repeat("test", 8))
	call := func(method, path string, values url.Values, user *User, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "repeat-session"})
		r = r.WithContext(context.WithValue(ctx, userCtxKey, user))
		w := httptest.NewRecorder()
		f.app.handleLoraTrainingRepeat(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	values := url.Values{"csrf": {f.app.csrfSigner.Token("repeat-session")}, "source_id": {source.PublicID}, "client_request_id": {"repeat-request-0001"}}
	call("POST", "/api/lora-training/repeats/quote", values, nil, 403)
	badCSRF := cloneGenerationValues(values)
	badCSRF.Set("csrf", "wrong")
	call("POST", "/api/lora-training/repeats/quote", badCSRF, &f.user, 403)
	protocol.Store(0)
	call("POST", "/api/lora-training/repeats/quote", values, &f.user, 409)
	protocol.Store(1)
	w := call("POST", "/api/lora-training/repeats/quote", values, &f.user, 200)
	var quoted struct {
		Confirmation string                         `json:"confirmation"`
		Parameters   domain.LoraExecutionParameters `json:"parameters"`
		Snapshot     domain.LoraDatasetSnapshot     `json:"snapshot"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &quoted); err != nil {
		t.Fatal(err)
	}
	if quoted.Parameters.Seed != 19 || quoted.Parameters.MaxSteps != 1234 || quoted.Snapshot.ID != snapshot.ID || strings.Contains(w.Body.String(), f.app.mediaSpoolDir()) || strings.Contains(w.Body.String(), "manifest_cipher") {
		t.Fatal("quote changed or leaked input", w.Body.String())
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM lora_training_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatal("quote queued work", count, err)
	}
	values.Set("confirmation", quoted.Confirmation)
	expired, err := f.app.decodeLoraRepeatConfirmation(quoted.Confirmation)
	if err != nil {
		t.Fatal(err)
	}
	expired.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	expiredToken, _ := f.app.encodeLoraRepeatConfirmation(expired)
	stale := cloneGenerationValues(values)
	stale.Set("confirmation", expiredToken)
	call("POST", "/api/lora-training/repeats/enqueue", stale, &f.user, 409)
	protocol.Store(0)
	call("POST", "/api/lora-training/repeats/enqueue", values, &f.user, 409)
	protocol.Store(1)
	w = call("POST", "/api/lora-training/repeats/enqueue", values, &f.user, 201)
	var receipt struct {
		JobID   string `json:"job_id"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	child, err := f.app.store.LoraTrainingJobByPublicID(ctx, receipt.JobID, f.user.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if child.Seed != 19 || child.MaxTrainSteps != 1234 || child.DatasetSnapshotID != snapshot.ID || child.State != domain.LoraTrainingQueued {
		t.Fatal("wrong queued training", child)
	}
	checkPassport := func(available bool) {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/lora-training/passports/"+child.PublicID, nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &f.user))
		w := httptest.NewRecorder()
		f.app.handleLoraTrainingPassport(w, r)
		var view loraPassportView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.RepeatSource == nil || view.RepeatSource.ID != source.PublicID || view.RepeatSource.Available != available {
			t.Fatal("repeat lineage lost", w.Body.String())
		}
	}
	checkPassport(true)
	archive, err := zip.OpenReader(child.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 10 {
		t.Fatal("snapshot images lost")
	}
	for _, file := range archive.File {
		if strings.HasSuffix(file.Name, ".txt") {
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(reader)
			reader.Close()
			if err != nil || !strings.Contains(string(data), "original caption") {
				t.Fatal("current draft caption replaced source", string(data), err)
			}
		}
	}
	archive.Close()
	call("POST", "/api/lora-training/repeats/enqueue", stale, &f.user, 200)
	call("GET", "/api/lora-training/repeats?request="+values.Get("client_request_id"), nil, &f.user, 200)
	if err := f.db.QueryRow(`SELECT count(*) FROM lora_training_jobs`).Scan(&count); err != nil || count != 2 {
		t.Fatal("replay created work", count, err)
	}
	entries, err := os.ReadDir(f.app.mediaSpoolDir() + "/lora-training")
	if err != nil || len(entries) != 1 {
		t.Fatal("quote leaked staging archive", entries, err)
	}
	if _, err := f.db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	call("GET", "/api/lora-training/repeats?request="+values.Get("client_request_id"), nil, &f.user, 403)
	if _, err := f.db.Exec(`UPDATE users SET disabled=false WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`DELETE FROM lora_training_jobs WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	checkPassport(false)
	if _, err := f.db.Exec(`DELETE FROM lora_training_jobs WHERE id=$1`, child.ID); err != nil {
		t.Fatal(err)
	}
	w = call("POST", "/api/lora-training/repeats/enqueue", stale, &f.user, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || !receipt.Deleted {
		t.Fatal("deleted repeat was not recovered", err)
	}
}
