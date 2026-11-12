package trainingagent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/loratraining"
)

func backupFixture(t *testing.T) (Config, jobRecord) {
	t.Helper()
	root := t.TempDir()
	model := filepath.Join(root, "base.safetensors")
	if err := os.WriteFile(model, []byte("base model not archived"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := ProfileConfig{ID: "krea-test", Family: "krea2", Name: "Krea", BaseModel: "fixture", DiT: model, VAE: model, TextEncoder: model}
	config := Config{Token: "fixture-training-secret-not-exported", RootDir: filepath.Join(root, "agent"), ComfyLoraDir: filepath.Join(root, "loras"), Profiles: []ProfileConfig{profile}}
	record := jobRecord{
		Spec:         loratraining.JobSpec{GatewayJobID: "gateway-job-001", ProfileID: profile.ID, Owner: "alice", Name: "Portrait", TriggerWord: "alice_token"},
		Status:       loratraining.JobStatus{ID: "agent-job-001", GatewayJobID: "gateway-job-001", ProfileID: profile.ID, State: "completed", ArtifactName: "portrait.safetensors", ArtifactBytes: 12, FinishedAt: time.Now()},
		ArtifactPath: filepath.Join(config.ComfyLoraDir, "Trained", "Krea2", "alice", "portrait.safetensors"), ProfileSnapshot: &profile,
	}
	if err := os.MkdirAll(filepath.Dir(record.ArtifactPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record.ArtifactPath, []byte("trained LoRA"), 0o600); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(config.RootDir, "jobs", record.Status.ID)
	if err := os.MkdirAll(filepath.Join(jobDir, "cache"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "cache", "rebuildable.bin"), []byte("cache not archived"), 0o600); err != nil {
		t.Fatal(err)
	}
	controller := &Controller{config: config}
	if err := controller.persist(&record); err != nil {
		t.Fatal(err)
	}
	return config, record
}

func TestTrainingBackupRoundTripRestoresOwnerAndDownload(t *testing.T) {
	config, record := backupFixture(t)
	var output bytes.Buffer
	manifest, err := ExportTrainingBackup(context.Background(), config, &output, "database")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Jobs) != 1 || manifest.Jobs[0].Owner != "alice" || len(manifest.Jobs[0].ProfileSHA256) != 64 || len(manifest.Models) != 1 || len(manifest.Models[0].SHA256) != 64 {
		t.Fatal(manifest)
	}
	if len(manifest.Files) != 2 || strings.Contains(output.String(), config.Token) {
		t.Fatal("unexpected cache/model/secret payload")
	}
	archive := filepath.Join(t.TempDir(), "training.zip")
	if err := os.WriteFile(archive, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreTrainingBackup(context.Background(), archive, destination); err != nil {
		t.Fatal(err)
	}
	config.RootDir, config.ComfyLoraDir = destination, destination
	controller, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if controller.record(record.Status.ID).Spec.Owner != "alice" {
		t.Fatal("owner was lost")
	}
	server, err := NewServer(config.Token, controller)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+record.Status.ID+"/artifact", nil)
	request.Header.Set("Authorization", "Bearer "+config.Token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 200 || response.Body.String() != "trained LoRA" {
		t.Fatal(response.Code, response.Body.String())
	}
	if _, err := RestoreTrainingBackup(context.Background(), archive, destination); err == nil {
		t.Fatal("existing destination overwritten")
	}
}

func TestTrainingBackupReportsMissingArtifactAndUnknownProfile(t *testing.T) {
	config, record := backupFixture(t)
	record.ProfileSnapshot = nil
	if err := (&Controller{config: config}).persist(&record); err != nil {
		t.Fatal(err)
	}
	manifest, err := ExportTrainingBackup(context.Background(), config, io.Discard, "database")
	if err != nil || len(manifest.Warnings) != 1 || manifest.Jobs[0].ProfileSHA256 != "" {
		t.Fatal(manifest, err)
	}
	if err := os.Remove(record.ArtifactPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportTrainingBackup(context.Background(), config, io.Discard, "database"); err == nil || !strings.Contains(err.Error(), record.Spec.GatewayJobID) {
		t.Fatal("missing artifact not attributed to its run", err)
	}
	if _, err := ExportTrainingBackup(context.Background(), config, io.Discard, ""); err == nil {
		t.Fatal("implicit dataset policy accepted")
	}
}

func TestTrainingBackupRejectsCorruptionAndUnsafeRestore(t *testing.T) {
	config, _ := backupFixture(t)
	var source bytes.Buffer
	if _, err := ExportTrainingBackup(context.Background(), config, &source, "database"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"content", "missing", "unlisted", "traversal", "duplicate", "owner", "profile"} {
		t.Run(mode, func(t *testing.T) {
			reader, err := zip.NewReader(bytes.NewReader(source.Bytes()), int64(source.Len()))
			if err != nil {
				t.Fatal(err)
			}
			var changed bytes.Buffer
			writer := zip.NewWriter(&changed)
			for _, item := range reader.File {
				if mode == "missing" && strings.HasPrefix(item.Name, "Trained/") {
					continue
				}
				r, _ := item.Open()
				data, _ := io.ReadAll(r)
				r.Close()
				if mode == "content" && strings.HasPrefix(item.Name, "Trained/") {
					data[0] ^= 1
				}
				if item.Name == "manifest.json" && (mode == "owner" || mode == "profile") {
					var manifest TrainingBackupManifest
					if err := json.Unmarshal(data, &manifest); err != nil {
						t.Fatal(err)
					}
					if mode == "owner" {
						manifest.Jobs[0].Owner = "mallory"
					} else {
						manifest.Jobs[0].ProfileSHA256 = strings.Repeat("0", 64)
					}
					data, _ = json.Marshal(manifest)
				}
				entry, _ := writer.Create(item.Name)
				_, _ = entry.Write(data)
			}
			if mode == "unlisted" || mode == "traversal" || mode == "duplicate" {
				name := "unexpected.txt"
				if mode == "traversal" {
					name = "../outside.txt"
				}
				if mode == "duplicate" {
					name = "MANIFEST.JSON"
				}
				entry, _ := writer.Create(name)
				_, _ = entry.Write([]byte("untrusted"))
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			parent := t.TempDir()
			archive, destination := filepath.Join(parent, "changed.zip"), filepath.Join(parent, "restored")
			if err := os.WriteFile(archive, changed.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := RestoreTrainingBackup(context.Background(), archive, destination); err == nil {
				t.Fatal("invalid archive accepted")
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 1 || entries[0].Name() != "changed.zip" {
				t.Fatal("partial restore was published or escaped", entries, err)
			}
		})
	}
}

func TestTrainingBackupCancellationDoesNotPublishRestore(t *testing.T) {
	config, _ := backupFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ExportTrainingBackup(ctx, config, io.Discard, "database"); err == nil {
		t.Fatal("cancelled export succeeded")
	}
	var output bytes.Buffer
	if _, err := ExportTrainingBackup(context.Background(), config, &output, "database"); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	archive, destination := filepath.Join(parent, "training.zip"), filepath.Join(parent, "restore")
	if err := os.WriteFile(archive, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreTrainingBackup(ctx, archive, destination); err == nil {
		t.Fatal("cancelled restore succeeded")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "training.zip" {
		t.Fatal("cancelled restore left files behind", entries, err)
	}
}

func TestTrainingBackupExcludesPartialInstalls(t *testing.T) {
	config, record := backupFixture(t)
	if err := os.WriteFile(record.ArtifactPath+".partial", []byte("unfinished install"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := ExportTrainingBackup(context.Background(), config, io.Discard, "database")
	if err != nil || len(manifest.Files) != 2 || len(manifest.Warnings) != 0 {
		t.Fatal(manifest, err)
	}
}

func TestTrainingBackupPersistsProfileAtSubmission(t *testing.T) {
	controller, spec := fenceTestController(t)
	status, err := controller.Submit(context.Background(), spec, strings.NewReader("dataset"))
	if err != nil {
		t.Fatal(err)
	}
	controller.config.Profiles[0].DiT = "a later config version"
	data, err := os.ReadFile(filepath.Join(controller.config.RootDir, "jobs", status.ID, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record jobRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.ProfileSnapshot == nil || record.ProfileSnapshot.ID != spec.ProfileID || record.ProfileSnapshot.DiT == controller.config.Profiles[0].DiT {
		t.Fatal("submission did not preserve the original profile")
	}
	cloned := cloneRecord(&record)
	cloned.ProfileSnapshot.DiT = "changed clone"
	if record.ProfileSnapshot.DiT == cloned.ProfileSnapshot.DiT {
		t.Fatal("profile snapshot alias")
	}
}
