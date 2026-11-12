package trainingagent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/loratraining"
)

func backupCleanupFixture(t *testing.T, config Config, state string) (loratraining.StorageCleanupRequest, loratraining.StorageCleanupResult) {
	t.Helper()
	request, hash, err := loratraining.CanonicalStorageCleanupRequest(loratraining.StorageCleanupRequest{
		ID: strings.Repeat("a", 32), Items: []loratraining.StorageSelection{{ID: "training_cache:agent-job-001", Version: strings.Repeat("b", 64)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	result := loratraining.StorageCleanupResult{ProtocolVersion: 1, ID: request.ID, RequestHash: hash, State: state, StartedAt: now,
		Items: []loratraining.StorageCleanupOutcome{{ID: request.Items[0].ID, Kind: "training_cache", Label: "Training cache", Version: request.Items[0].Version, Outcome: "not_run"}}}
	if state == "completed" {
		result.FinishedAt = &now
		result.Items[0].Outcome, result.Items[0].FreedBytes, result.Items[0].DeletedFiles = "deleted", 12, 1
	}
	if err := (&Controller{config: config}).writeStorageCleanupResult(result); err != nil {
		t.Fatal(err)
	}
	return request, result
}

func restoreBackupBytes(t *testing.T, data []byte) (TrainingBackupManifest, string, error) {
	t.Helper()
	parent := t.TempDir()
	archive, destination := filepath.Join(parent, "training.zip"), filepath.Join(parent, "restored")
	if err := os.WriteFile(archive, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := RestoreTrainingBackup(context.Background(), archive, destination)
	if err != nil {
		entries, readErr := os.ReadDir(parent)
		if readErr != nil || len(entries) != 1 {
			t.Fatal("failed restore published files", entries, readErr)
		}
	}
	return manifest, destination, err
}

func TestTrainingBackupStorageJournalRoundTripNeverReplays(t *testing.T) {
	for _, state := range []string{"completed", "running", "expired"} {
		t.Run(state, func(t *testing.T) {
			config, _ := backupFixture(t)
			savedState := state
			if state == "expired" {
				savedState = "completed"
			}
			request, result := backupCleanupFixture(t, config, savedState)
			controller := &Controller{config: config}
			if state == "expired" {
				if err := controller.pruneStorageCleanupReceipts(time.Now().Add(91 * 24 * time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			var archive bytes.Buffer
			manifest, err := ExportTrainingBackup(context.Background(), config, &archive, "database")
			if err != nil || manifest.Version != 2 || manifest.CleanupJournalVersion != 1 || len(manifest.Files) != 3 {
				t.Fatal(manifest, err)
			}
			_, destination, err := restoreBackupBytes(t, archive.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			config.RootDir, config.ComfyLoraDir = destination, destination
			controller = &Controller{config: config}
			cache := filepath.Join(destination, "jobs", "agent-job-001", "cache")
			if err := os.MkdirAll(cache, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(cache, "rebuilt.bin")
			if err := os.WriteFile(file, []byte("new cache"), 0o600); err != nil {
				t.Fatal(err)
			}
			replayed, err := controller.CleanupStorage(context.Background(), request)
			switch state {
			case "completed":
				if err != nil || replayed.State != "completed" || replayed.Items[0].FreedBytes != result.Items[0].FreedBytes {
					t.Fatal(replayed, err)
				}
			case "running":
				if err != nil || replayed.State != "unconfirmed" || replayed.Items[0].FreedBytes != 0 {
					t.Fatal(replayed, err)
				}
			case "expired":
				if !errors.Is(err, ErrStorageResultExpired) {
					t.Fatal(err)
				}
			}
			fenced, err := controller.FenceStorageCleanup(context.Background(), request)
			if err != nil || !fenced.Fenced || !fenced.Settled {
				t.Fatal(fenced, err)
			}
			if state != "completed" && fenced.Result.State != "closed_unconfirmed" {
				t.Fatal(fenced)
			}
			data, err := os.ReadFile(file)
			if err != nil || string(data) != "new cache" {
				t.Fatal("replayed delete touched rebuilt cache", err)
			}
		})
	}
}

type mutateBackupWriter struct {
	writer io.Writer
	mutate func()
}

func (w *mutateBackupWriter) Write(p []byte) (int, error) {
	if w.mutate != nil {
		fn := w.mutate
		w.mutate = nil
		fn()
	}
	return w.writer.Write(p)
}

func TestTrainingBackupStorageJournalRejectsConcurrentChange(t *testing.T) {
	for _, mode := range []string{"create", "update", "prune"} {
		t.Run(mode, func(t *testing.T) {
			config, _ := backupFixture(t)
			var result loratraining.StorageCleanupResult
			if mode != "create" {
				_, result = backupCleanupFixture(t, config, "completed")
			}
			writer := &mutateBackupWriter{writer: io.Discard, mutate: func() {
				switch mode {
				case "create":
					backupCleanupFixture(t, config, "completed")
				case "update":
					result.Items[0].FreedBytes++
					if err := (&Controller{config: config}).writeStorageCleanupResult(result); err != nil {
						t.Fatal(err)
					}
				case "prune":
					if err := (&Controller{config: config}).pruneStorageCleanupReceipts(time.Now().Add(91 * 24 * time.Hour)); err != nil {
						t.Fatal(err)
					}
				}
			}}
			if _, err := ExportTrainingBackup(context.Background(), config, writer, "database"); err == nil {
				t.Fatal("changed journal accepted")
			}
		})
	}
}

func TestTrainingBackupStorageJournalRejectsInvalidLocalState(t *testing.T) {
	for _, mode := range []string{"corrupt", "wrong_id", "conflict", "unknown", "directory", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			config, _ := backupFixture(t)
			request, result := backupCleanupFixture(t, config, "completed")
			directory := filepath.Join(config.RootDir, storageReceiptDirectory)
			filename := filepath.Join(directory, request.ID+".json")
			switch mode {
			case "corrupt":
				if err := os.WriteFile(filename, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "wrong_id":
				result.ID = strings.Repeat("c", 32)
				data, _ := json.Marshal(result)
				if err := os.WriteFile(filename, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "conflict":
				data, _ := json.Marshal(storageClosedID{ID: request.ID, RequestHash: strings.Repeat("c", 64)})
				if err := os.WriteFile(filepath.Join(directory, request.ID+".closed"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if err := os.WriteFile(filepath.Join(directory, "unknown"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(filepath.Join(directory, "nested"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(filename, bytes.Repeat([]byte(" "), maxStorageReceiptBytes+1), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ExportTrainingBackup(context.Background(), config, io.Discard, "database"); err == nil {
				t.Fatal("invalid journal accepted")
			}
		})
	}
}

func TestTrainingBackupStorageJournalRestoreValidatesHashedPayload(t *testing.T) {
	config, _ := backupFixture(t)
	backupCleanupFixture(t, config, "completed")
	var source bytes.Buffer
	if _, err := ExportTrainingBackup(context.Background(), config, &source, "database"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"bad_result", "bad_closed", "unknown_file", "legacy", "bad_version"} {
		t.Run(mode, func(t *testing.T) {
			reader, err := zip.NewReader(bytes.NewReader(source.Bytes()), int64(source.Len()))
			if err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{}
			var manifest TrainingBackupManifest
			for _, entry := range reader.File {
				r, _ := entry.Open()
				data, _ := io.ReadAll(r)
				r.Close()
				if entry.Name == "manifest.json" {
					if err := json.Unmarshal(data, &manifest); err != nil {
						t.Fatal(err)
					}
				} else {
					files[entry.Name] = data
				}
			}
			for i := range manifest.Files {
				item := &manifest.Files[i]
				if !strings.HasPrefix(item.Path, storageReceiptDirectory+"/") {
					continue
				}
				delete(files, item.Path)
				data := []byte(`{"id":"wrong"}`)
				if mode == "bad_closed" {
					item.Path = strings.TrimSuffix(item.Path, ".json") + ".closed"
				}
				if mode == "unknown_file" {
					item.Path = storageReceiptDirectory + "/unknown.json"
				}
				if mode == "legacy" {
					manifest.Version, manifest.CleanupJournalVersion = 1, 0
				}
				if mode == "bad_version" {
					manifest.CleanupJournalVersion = 99
				}
				hash := sha256.Sum256(data)
				item.Bytes, item.SHA256 = int64(len(data)), hex.EncodeToString(hash[:])
				files[item.Path] = data
			}
			var changed bytes.Buffer
			writer := zip.NewWriter(&changed)
			for name, data := range files {
				entry, _ := writer.Create(name)
				_, _ = entry.Write(data)
			}
			data, _ := json.Marshal(manifest)
			entry, _ := writer.Create("manifest.json")
			_, _ = entry.Write(data)
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := restoreBackupBytes(t, changed.Bytes()); err == nil {
				t.Fatal("invalid journal accepted with matching hash")
			}
		})
	}
}

func TestTrainingBackupStorageJournalLegacyAndEmpty(t *testing.T) {
	config, _ := backupFixture(t)
	var source bytes.Buffer
	if _, err := ExportTrainingBackup(context.Background(), config, &source, "database"); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := restoreBackupBytes(t, source.Bytes())
	if err != nil || manifest.CleanupJournalVersion != 1 || len(manifest.Warnings) != 0 {
		t.Fatal(manifest, err)
	}
	reader, err := zip.NewReader(bytes.NewReader(source.Bytes()), int64(source.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var legacy bytes.Buffer
	writer := zip.NewWriter(&legacy)
	for _, entry := range reader.File {
		r, _ := entry.Open()
		data, _ := io.ReadAll(r)
		r.Close()
		if entry.Name == "manifest.json" {
			manifest.Version, manifest.CleanupJournalVersion = 1, 0
			data, _ = json.Marshal(manifest)
		}
		file, _ := writer.Create(entry.Name)
		_, _ = file.Write(data)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, _, err = restoreBackupBytes(t, legacy.Bytes())
	if err != nil || len(manifest.Warnings) != 1 || !strings.Contains(manifest.Warnings[0], "no storage cleanup journal") {
		t.Fatal(manifest, err)
	}
}

func TestTrainingBackupStorageJournalPartialAndLinkedFiles(t *testing.T) {
	config, _ := backupFixture(t)
	request, _ := backupCleanupFixture(t, config, "running")
	directory := filepath.Join(config.RootDir, storageReceiptDirectory)
	partial := filepath.Join(directory, request.ID+"-unfinished.partial")
	if err := os.WriteFile(partial, []byte("unfinished"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := ExportTrainingBackup(context.Background(), config, io.Discard, "database")
	if err != nil || len(manifest.Files) != 3 {
		t.Fatal(manifest, err)
	}
	outside := filepath.Join(t.TempDir(), "external.json")
	if err := os.WriteFile(outside, []byte("not a receipt"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, strings.Repeat("c", 32)+".json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ExportTrainingBackup(context.Background(), config, io.Discard, "database"); err == nil {
		t.Fatal("linked journal accepted")
	}
}
