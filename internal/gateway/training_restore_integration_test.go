package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/trainingagent"
	"github.com/lib/pq"
)

// This opt-in test creates its own databases. It never migrates, truncates or
// restores into the database named by TEST_RESTORE_DATABASE_URL.
func TestTrainingBackupPostgresRestoreIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_RESTORE_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_RESTORE_DATABASE_URL is not set; requires disposable PostgreSQL and matching pg_dump/pg_restore")
	}
	for _, command := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	createDatabase := func(suffix string) (*sql.DB, string, string) {
		t.Helper()
		name := "training_restore_" + strings.ToLower(newRequestID()[:12]) + "_" + suffix
		if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(name)); err != nil {
			t.Fatal(err)
		}
		targetURL, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		targetURL.Path = "/" + name
		target, err := database.Open(ctx, targetURL.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			target.Close()
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if _, err := admin.ExecContext(cleanup, "DROP DATABASE IF EXISTS "+pq.QuoteIdentifier(name)); err != nil {
				t.Errorf("remove isolated database %s: %v", name, err)
			}
		})
		return target, targetURL.String(), name
	}
	source, sourceDSN, sourceName := createDatabase("source")
	restored, restoredDSN, _ := createDatabase("target")
	if err := database.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	const secret = "training-restore-test-secret-32-bytes"
	newApp := func(db *sql.DB, key string) *App {
		t.Helper()
		cipher, err := contentcrypto.NewCipher(key)
		if err != nil {
			t.Fatal(err)
		}
		return &App{cfg: Config{MediaSpoolDir: t.TempDir(), MediaInFlightLimitBytes: 64 << 20, SessionIdleTimeout: time.Hour},
			store: store.New(db), contentCipher: cipher, csrfSigner: security.NewCSRFSigner(key)}
	}
	sourceApp := newApp(source, secret)
	owners := map[string]int64{}
	for _, username := range []string{"restore-alice", "restore-bob"} {
		var id int64
		if err := source.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,can_train_image_lora) VALUES($1,'disabled','user',true) RETURNING id`, username).Scan(&id); err != nil {
			t.Fatal(err)
		}
		owners[username] = id
		if err := sourceApp.store.CreateSession(ctx, id, security.HashToken(username+"-fixture-session"), time.Now().Add(time.Hour), "restore-test", "127.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	owner := owners["restore-alice"]
	imageBytes := datasetTestPNG(t, 768, 768)
	asset, err := sourceApp.persistLoraDatasetImage(ctx, owner, "portrait.png", bytes.NewReader(imageBytes))
	if err != nil {
		t.Fatal(err)
	}
	if len(imageBytes) <= loraDatasetChunkBytes {
		t.Fatal("fixture must span multiple encrypted chunks")
	}
	manifest := domain.LoraDatasetManifest{Version: 1,
		Settings: domain.LoraDatasetSettings{Name: "Restore portrait", TriggerWord: "restore_person", ConceptType: "character", Resolution: 512},
		Images:   []domain.LoraDatasetImage{{ID: "frame-1", AssetID: asset.ID, Caption: "restore_person, portrait beside a window"}, {ID: "frame-2", AssetID: asset.ID, Caption: "excluded handwritten caption", Excluded: true}}}
	cipher, manifestHash, err := sourceApp.encryptLoraDatasetManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := sourceApp.store.CreateLoraDataset(ctx, owner, newRequestID(), manifest.Settings.Name, cipher)
	if err != nil {
		t.Fatal(err)
	}
	dataset, err = sourceApp.store.SaveLoraDataset(ctx, owner, dataset.ID, dataset.Revision, dataset.Name, cipher, []string{asset.ID})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := sourceApp.store.CreateLoraDatasetSnapshot(ctx, owner, dataset.ID, dataset.Revision, newRequestID(), manifestHash)
	if err != nil {
		t.Fatal(err)
	}
	job, err := sourceApp.store.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{
		PublicID: newRequestID(), UserID: owner, UsernameSnapshot: "restore-alice", RequestID: newRequestID(),
		ProfileID: "restore-krea", Family: "krea2", BaseModel: "fixture", Name: "Restore portrait", OutputName: "restore_portrait",
		TriggerWord: "restore_person", ConceptType: "character", Preset: "quick", Resolution: 512, MaxTrainSteps: 100,
		NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.0001, Seed: 42, SampleCount: 5, DatasetBytes: 12, DatasetPath: "fixture.zip",
		DatasetSnapshotID: snapshot.ID, DatasetSnapshotHash: snapshot.Hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	const agentID = "restore-agent-job-001"
	const artifactName = "restore_portrait.safetensors"
	artifactBytes := []byte("synthetic LoRA bytes; never loaded as a model")
	if _, err := source.ExecContext(ctx, `UPDATE lora_training_jobs SET state='completed',agent_job_id=$2,artifact_name=$3,artifact_bytes=$4,progress=100,finished_at=now() WHERE id=$1`, job.ID, agentID, artifactName, len(artifactBytes)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	file := filepath.Join(root, "source", "Trained", "Krea2", "alice", artifactName)
	writeFile := func(filename string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(file, artifactBytes)
	model := filepath.Join(root, "external-model.fixture")
	writeFile(model, []byte("base model inventory only"))
	profile := trainingagent.ProfileConfig{ID: job.ProfileID, Family: "krea2", Name: "Restore fixture", BaseModel: "fixture", DiT: model, VAE: model, TextEncoder: model}
	agentConfig := trainingagent.Config{RootDir: filepath.Join(root, "source"), ComfyLoraDir: filepath.Join(root, "source"), Token: "restore-agent-test-token-000000000000", Profiles: []trainingagent.ProfileConfig{profile}}
	record := map[string]any{
		"spec":          loratraining.JobSpec{GatewayJobID: job.PublicID, ProfileID: profile.ID, Owner: job.UsernameSnapshot},
		"status":        loratraining.JobStatus{ID: agentID, GatewayJobID: job.PublicID, ProfileID: profile.ID, State: "completed", ArtifactName: artifactName, ArtifactBytes: int64(len(artifactBytes))},
		"artifact_path": file, "profile_snapshot": profile,
	}
	recordBytes, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(agentConfig.RootDir, "jobs", agentID, "status.json"), recordBytes)
	var archived bytes.Buffer
	archiveManifest, err := trainingagent.ExportTrainingBackup(ctx, agentConfig, &archived, "database")
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "training.zip")
	writeFile(archivePath, archived.Bytes())
	dump := filepath.Join(root, "gateway.dump")
	for _, command := range [][]string{
		{"pg_dump", "--dbname", sourceDSN, "--format=custom", "--file", dump},
		{"pg_restore", "--dbname", restoredDSN, "--no-owner", "--no-privileges", "--exit-on-error", dump},
	} {
		output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v: %s", command[0], err, output)
		}
	}
	// Eliminate source data so success cannot come from a surviving source path.
	source.Close()
	if _, err := admin.ExecContext(ctx, "DROP DATABASE "+pq.QuoteIdentifier(sourceName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	restoreRoot := filepath.Join(root, "restored")
	if _, err := trainingagent.RestoreTrainingBackup(ctx, archivePath, restoreRoot); err != nil {
		t.Fatal(err)
	}
	agentConfig.RootDir, agentConfig.ComfyLoraDir = restoreRoot, restoreRoot
	controller, err := trainingagent.NewController(agentConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	agent, err := trainingagent.NewServer(agentConfig.Token, controller)
	if err != nil {
		t.Fatal(err)
	}
	agentHTTP := httptest.NewServer(agent.Handler())
	defer agentHTTP.Close()
	agentURL, _ := url.Parse(agentHTTP.URL)
	app := newApp(restored, secret)
	verified, err := VerifyTrainingDatasetRestore(ctx, restored, secret)
	if err != nil || verified.Datasets != 1 || verified.Snapshots != 1 || verified.Assets != 1 || verified.Bytes != int64(len(imageBytes)) {
		t.Fatal("dataset restore verifier failed", verified, err)
	}
	if _, err := VerifyTrainingDatasetRestore(ctx, restored, "wrong-restore-secret-00000000000000"); err == nil {
		t.Fatal("dataset verifier accepted wrong key")
	}
	app.loraTraining = loratraining.NewClient(agentURL, agentConfig.Token)
	mux := http.NewServeMux()
	app.registerLoraTrainingRoutes(mux)
	gatewayHTTP := httptest.NewServer(mux)
	defer gatewayHTTP.Close()
	request := func(username, target string, want int) []byte {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayHTTP.URL+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: username + "-fixture-session"})
		response, err := gatewayHTTP.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("GET %s as %s: %d want %d, %v: %s", target, username, response.StatusCode, want, err, body)
		}
		return body
	}
	var jobs struct {
		Jobs []loraTrainingJobJSON `json:"jobs"`
	}
	if err := json.Unmarshal(request("restore-alice", "/api/lora-training/jobs", 200), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].ID != job.PublicID || !jobs.Jobs[0].CanDownload || jobs.Jobs[0].DatasetSnapshotID != snapshot.ID || jobs.Jobs[0].DatasetSnapshotHash != manifestHash {
		t.Fatalf("restored job/snapshot missing from owner API: %+v", jobs)
	}
	if got := request("restore-alice", jobs.Jobs[0].DownloadURL, 200); !bytes.Equal(got, artifactBytes) {
		t.Fatal("downloaded LoRA differs from original")
	}
	request("restore-bob", jobs.Jobs[0].DownloadURL, 404)
	var foreign struct {
		Jobs []loraTrainingJobJSON `json:"jobs"`
	}
	if err := json.Unmarshal(request("restore-bob", "/api/lora-training/jobs", 200), &foreign); err != nil || len(foreign.Jobs) != 0 {
		t.Fatal("restored owner association changed", err, foreign)
	}
	for _, target := range []string{"/api/lora-datasets/" + dataset.ID, "/api/lora-datasets/versions/" + snapshot.ID} {
		var view struct {
			Manifest domain.LoraDatasetManifest `json:"manifest"`
		}
		if err := json.Unmarshal(request("restore-alice", target, 200), &view); err != nil {
			t.Fatal(err)
		}
		if len(view.Manifest.Images) != 2 || view.Manifest.Images[0].Caption != manifest.Images[0].Caption || !view.Manifest.Images[1].Excluded || view.Manifest.Settings.TriggerWord != "restore_person" {
			t.Fatalf("restored caption/settings changed: %+v", view.Manifest)
		}
		request("restore-bob", target, 404)
	}
	assetURL := "/api/lora-datasets/assets/" + asset.ID
	if got := request("restore-alice", assetURL, 200); !bytes.Equal(got, imageBytes) {
		t.Fatal("restored encrypted image bytes changed")
	}
	request("restore-bob", assetURL, 404)
	exported := request("restore-alice", "/api/lora-datasets/versions/"+snapshot.ID+"/export", 200)
	zipReader, err := zip.NewReader(bytes.NewReader(exported), int64(len(exported)))
	if err != nil {
		t.Fatal(err)
	}
	var foundImage, foundCaption bool
	for _, entry := range zipReader.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		foundImage = foundImage || bytes.Equal(data, imageBytes)
		foundCaption = foundCaption || bytes.Contains(data, []byte(manifest.Images[0].Caption))
	}
	if !foundImage || !foundCaption {
		t.Fatal("restored dataset ZIP lost image/caption")
	}
	wrongApp := newApp(restored, "wrong-restore-secret-00000000000000")
	wrongMux := http.NewServeMux()
	wrongApp.registerLoraTrainingRoutes(wrongMux)
	wrongRequest := httptest.NewRequest(http.MethodGet, "/api/lora-datasets/"+dataset.ID, nil)
	wrongRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "restore-alice-fixture-session"})
	wrongResponse := httptest.NewRecorder()
	wrongMux.ServeHTTP(wrongResponse, wrongRequest)
	if wrongResponse.Code != 500 {
		t.Fatal("incorrect restore key did not fail", wrongResponse.Code)
	}
	var original []byte
	if err := restored.QueryRowContext(ctx, `SELECT payload_cipher FROM lora_dataset_asset_chunks WHERE asset_id=$1 AND chunk_index=0`, asset.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(original)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := restored.ExecContext(ctx, `UPDATE lora_dataset_asset_chunks SET payload_cipher=$2 WHERE asset_id=$1 AND chunk_index=0`, asset.ID, corrupt); err != nil {
		t.Fatal(err)
	}
	request("restore-alice", assetURL, 500)
	if _, err := VerifyTrainingDatasetRestore(ctx, restored, secret); err == nil || !strings.Contains(err.Error(), asset.ID) {
		t.Fatal("corrupt asset not attributed in verification report", err)
	}
	if _, err := restored.ExecContext(ctx, `UPDATE lora_dataset_asset_chunks SET payload_cipher=$2 WHERE asset_id=$1 AND chunk_index=0`, asset.ID, original); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.ExecContext(ctx, `UPDATE lora_dataset_snapshots SET manifest_hash=$2 WHERE id=$1`, snapshot.ID, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTrainingDatasetRestore(ctx, restored, secret); err == nil || !strings.Contains(err.Error(), snapshot.ID) {
		t.Fatal("snapshot checksum mismatch not attributed", err)
	}
	if _, err := restored.ExecContext(ctx, `UPDATE lora_dataset_snapshots SET manifest_hash=$2 WHERE id=$1`, snapshot.ID, manifestHash); err != nil {
		t.Fatal(err)
	}
	if binary := os.Getenv("TEST_RESTORE_GATEWAY_BINARY"); binary != "" {
		assertRestoreGatewayBinary(t, ctx, binary, restoredDSN, restored, secret, owners["restore-bob"])
	}
	if entries, err := os.ReadDir(app.mediaSpoolDir()); err != nil || len(entries) != 0 {
		t.Fatal("restore verification leaked decrypted files", err, entries)
	}
	restoredArtifact := filepath.Join(restoreRoot, filepath.FromSlash(archiveManifest.Jobs[0].Artifact))
	if err := os.Remove(restoredArtifact); err != nil {
		t.Fatal(err)
	}
	request("restore-alice", jobs.Jobs[0].DownloadURL, 502)
	imageHash := sha256.Sum256(imageBytes)
	t.Logf("pg_dump/pg_restore + authenticated Gateway routes: LoRA owner/download, dataset/snapshot/captions, %d image bytes (sha256 %s), ZIP, wrong key, corrupt chunk and missing artifact verified", len(imageBytes), hex.EncodeToString(imageHash[:]))
}

func assertRestoreGatewayBinary(t *testing.T, ctx context.Context, binary, dsn string, db *sql.DB, secret string, owner int64) {
	t.Helper()
	for _, key := range []string{secret, "incorrect-restore-key-000000000000"} {
		command := exec.CommandContext(ctx, binary, "-verify-training-datasets")
		command.Env = append(os.Environ(), "DATABASE_URL="+dsn, "SESSION_SECRET="+key)
		output, err := command.CombinedOutput()
		var result struct {
			Success   bool                         `json:"success"`
			Inventory TrainingDatasetRestoreReport `json:"inventory"`
		}
		if decodeErr := json.Unmarshal(output, &result); decodeErr != nil {
			t.Fatalf("invalid verifier CLI report: %s: %v", output, decodeErr)
		}
		if key == secret {
			if err != nil || !result.Success || result.Inventory.Assets != 1 {
				t.Fatalf("verifier CLI: %s: %v", output, err)
			}
		} else if err == nil || result.Success {
			t.Fatal("verifier CLI accepted incorrect key")
		}
	}
	pending, err := store.New(db).CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{
		PublicID: newRequestID(), UserID: owner, UsernameSnapshot: "restore-bob", RequestID: newRequestID(),
		ProfileID: "restore-krea", Family: "krea2", BaseModel: "fixture", Name: "Unfinished restore fixture", OutputName: "pending_fixture",
		TriggerWord: "restore_person", ConceptType: "character", Preset: "quick", Resolution: 512, MaxTrainSteps: 100,
		NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.0001, Seed: 42, SampleCount: 5, DatasetBytes: 12, DatasetPath: "must-not-run.zip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE lora_training_jobs SET state='uploading' WHERE id=$1`, pending.ID); err != nil {
		t.Fatal(err)
	}
	var externalCalls atomic.Int32
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalCalls.Add(1)
		http.Error(w, "fixture unavailable", 503)
	}))
	defer unavailable.Close()
	freeAddress := func() string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().String()
	}
	public, admin := freeAddress(), freeAddress()
	for admin == public {
		admin = freeAddress()
	}
	command := exec.CommandContext(ctx, binary)
	command.Env = append(os.Environ(), "DATABASE_URL="+dsn, "SESSION_SECRET="+secret, "ADMIN_PASSWORD=restore-test-admin-password",
		"RESTORE_DRILL_MODE=true", "PUBLIC_ADDR="+public, "ADMIN_ADDR="+admin, "MEDIA_SPOOL_DIR="+t.TempDir())
	for _, name := range []string{"COMFYUI_UPSTREAM", "OPENWEBUI_UPSTREAM", "OLLAMA_UPSTREAM", "CONTENT_MODERATOR_UPSTREAM", "MINING_AGENT_URL", "SYSTEM_MONITOR_AGENT_URL", "UPDATE_AGENT_URL", "LORA_TRAINING_AGENT_URL", "INFERENCE_WORKER_URL"} {
		command.Env = append(command.Env, name+"="+unavailable.URL)
	}
	for _, name := range []string{"MINING_AGENT_TOKEN", "SYSTEM_MONITOR_AGENT_TOKEN", "UPDATE_AGENT_TOKEN", "LORA_TRAINING_AGENT_TOKEN"} {
		command.Env = append(command.Env, name+"=restore-only-test-token-0000000000000000")
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var runErr error
	go func() { runErr = command.Wait(); close(done) }()
	defer func() {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatalf("restore Gateway exited: %v: %s", runErr, output.String())
		default:
		}
		response, err := client.Get("http://" + public + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		t.Fatal("restore Gateway did not start")
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM lora_training_jobs WHERE id=$1`, pending.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "uploading" || externalCalls.Load() != 0 {
		t.Fatalf("restore mode resumed work: state=%s calls=%d", state, externalCalls.Load())
	}
	var users int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 2 {
		t.Fatal("restore mode changed account inventory", users)
	}
	t.Log("real Gateway binary: dataset verification CLI and restore startup without job recovery/external calls passed")
}
