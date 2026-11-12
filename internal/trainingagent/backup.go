package trainingagent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type TrainingBackupFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type TrainingBackupJob struct {
	AgentID       string `json:"agent_id"`
	GatewayID     string `json:"gateway_id"`
	Owner         string `json:"owner"`
	Artifact      string `json:"artifact"`
	ProfileID     string `json:"profile_id"`
	ProfileSHA256 string `json:"profile_sha256,omitempty"`
}

type TrainingBackupModel struct {
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256,omitempty"`
	Error    string `json:"error,omitempty"`
	Recovery string `json:"recovery"`
}

type TrainingBackupManifest struct {
	Version               int                   `json:"version"`
	CleanupJournalVersion int                   `json:"cleanup_journal_version,omitempty"`
	CreatedAt             time.Time             `json:"created_at"`
	DatasetPolicy         string                `json:"dataset_policy"`
	Files                 []TrainingBackupFile  `json:"files"`
	Jobs                  []TrainingBackupJob   `json:"jobs"`
	Profiles              []ProfileConfig       `json:"profiles_at_backup"`
	Models                []TrainingBackupModel `json:"external_models"`
	Warnings              []string              `json:"warnings,omitempty"`
}

type backupReader struct {
	ctx context.Context
	io.Reader
}

func (r backupReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func backupPath(name string) bool {
	if name == "" || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00") || !fs.ValidPath(name) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimRight(part, ". ") != part || !filepath.IsLocal(part) {
			return false
		}
	}
	return true
}

func backupDigest(ctx context.Context, writer io.Writer, reader io.Reader) (int64, string, error) {
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(writer, hash), backupReader{ctx, reader})
	return n, hex.EncodeToString(hash.Sum(nil)), err
}

func backupFileKey(filename string) string {
	filename = filepath.Clean(filename)
	if runtime.GOOS == "windows" {
		return strings.ToLower(filename)
	}
	return filename
}

// ExportTrainingBackup reads local state only. It never starts the training agent
// or copies cache/model payloads. Saved dataset bytes belong to the companion DB dump.
func ExportTrainingBackup(ctx context.Context, config Config, destination io.Writer, datasetPolicy string) (TrainingBackupManifest, error) {
	manifest := TrainingBackupManifest{Version: 2, CleanupJournalVersion: 1, CreatedAt: time.Now().UTC(), DatasetPolicy: datasetPolicy, Profiles: config.Profiles}
	if datasetPolicy != "database" {
		return manifest, errors.New("dataset policy must explicitly be database")
	}
	journal, err := snapshotTrainingBackupJournal(ctx, config.RootDir)
	if err != nil {
		return manifest, err
	}
	archive := zip.NewWriter(destination)
	add := func(name string, reader io.Reader) (TrainingBackupFile, error) {
		item := TrainingBackupFile{Path: name}
		if !backupPath(name) {
			return item, fmt.Errorf("invalid backup path %q", name)
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			return item, err
		}
		item.Bytes, item.SHA256, err = backupDigest(ctx, entry, reader)
		if err == nil {
			manifest.Files = append(manifest.Files, item)
		}
		return item, err
	}
	trained := filepath.Join(config.ComfyLoraDir, "Trained")
	weights := make(map[string]TrainingBackupFile)
	if _, err := os.Lstat(trained); err == nil {
		root, err := os.OpenRoot(trained)
		if err != nil {
			return manifest, err
		}
		defer root.Close()
		err = filepath.WalkDir(trained, func(filename string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("linked training file is not backed up: %s", filename)
			}
			if entry.IsDir() {
				return nil
			}
			if strings.HasSuffix(entry.Name(), ".partial") {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("not a regular training file: %s", filename)
			}
			relative, err := filepath.Rel(trained, filename)
			if err != nil {
				return err
			}
			file, err := root.Open(relative)
			if err != nil {
				return err
			}
			defer file.Close()
			before, err := file.Stat()
			if err != nil {
				return err
			}
			item, err := add("Trained/"+filepath.ToSlash(relative), file)
			if err != nil {
				return err
			}
			after, err := file.Stat()
			if err != nil || item.Bytes != before.Size() || after.Size() != before.Size() || after.ModTime() != before.ModTime() {
				return fmt.Errorf("training file changed during backup: %s", filename)
			}
			weights[backupFileKey(filename)] = item
			return nil
		})
		if err != nil {
			return manifest, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return manifest, err
	}

	filenames, err := filepath.Glob(filepath.Join(config.RootDir, "jobs", "*", "status.json"))
	if err != nil {
		return manifest, err
	}
	var stateRoot *os.Root
	if len(filenames) > 0 {
		stateRoot, err = os.OpenRoot(config.RootDir)
		if err != nil {
			return manifest, err
		}
		defer stateRoot.Close()
	}
	used := make(map[string]bool)
	for _, filename := range filenames {
		relative, err := filepath.Rel(config.RootDir, filename)
		if err != nil {
			return manifest, err
		}
		file, err := stateRoot.Open(relative)
		if err != nil {
			return manifest, err
		}
		data, err := io.ReadAll(io.LimitReader(backupReader{ctx, file}, (8<<20)+1))
		file.Close()
		if err != nil {
			return manifest, err
		}
		if len(data) > 8<<20 {
			return manifest, fmt.Errorf("oversized execution record: %s", filename)
		}
		var record jobRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return manifest, fmt.Errorf("read execution record %s: %w", filename, err)
		}
		if record.Status.State != "completed" {
			continue
		}
		if record.Status.ExecutionUnconfirmed || record.Status.ID != filepath.Base(filepath.Dir(filename)) || record.Status.ID == "" || record.Spec.GatewayJobID == "" || record.Status.GatewayJobID != record.Spec.GatewayJobID || record.Spec.Owner == "" {
			return manifest, fmt.Errorf("incomplete completed execution record: %s", filename)
		}
		item, exists := weights[backupFileKey(record.ArtifactPath)]
		if !exists || item.Bytes != record.Status.ArtifactBytes {
			return manifest, fmt.Errorf("artifact missing or changed for run %s", record.Spec.GatewayJobID)
		}
		job := TrainingBackupJob{AgentID: record.Status.ID, GatewayID: record.Spec.GatewayJobID, Owner: record.Spec.Owner, Artifact: item.Path, ProfileID: record.Spec.ProfileID}
		if record.ProfileSnapshot != nil {
			profile, _ := json.Marshal(record.ProfileSnapshot)
			hash := sha256.Sum256(profile)
			job.ProfileSHA256 = hex.EncodeToString(hash[:])
		} else {
			manifest.Warnings = append(manifest.Warnings, "Historical profile is unknown for run "+job.GatewayID)
		}
		record.ArtifactPath, record.DatasetPath = item.Path, ""
		record.Status.LogTail = nil
		data, err = json.Marshal(record)
		if err != nil {
			return manifest, err
		}
		if _, err = add("jobs/"+record.Status.ID+"/status.json", bytes.NewReader(data)); err != nil {
			return manifest, err
		}
		manifest.Jobs = append(manifest.Jobs, job)
		used[item.Path] = true
	}
	for _, file := range weights {
		if !used[file.Path] {
			manifest.Warnings = append(manifest.Warnings, "Unlinked Trained file: "+file.Path)
		}
	}
	sort.Strings(manifest.Warnings)
	modelPaths := make(map[string]bool)
	for _, profile := range config.Profiles {
		for _, filename := range []string{profile.DiT, profile.VAE, profile.TextEncoder} {
			if modelPaths[filename] {
				continue
			}
			modelPaths[filename] = true
			model := TrainingBackupModel{Path: filename, Recovery: "Restore this model from a separate model backup or its original source; verify SHA-256 before training."}
			file, err := os.Open(filename)
			if err == nil {
				model.Bytes, model.SHA256, err = backupDigest(ctx, io.Discard, file)
				file.Close()
			}
			if err != nil {
				model.Error = err.Error()
				model.SHA256 = ""
			}
			manifest.Models = append(manifest.Models, model)
		}
	}
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	for _, item := range journal {
		if _, err := add(storageReceiptDirectory+"/"+item.name, bytes.NewReader(item.payload)); err != nil {
			return manifest, err
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return manifest, err
	}
	entry, err := archive.Create("manifest.json")
	if err == nil {
		_, err = entry.Write(data)
	}
	if err != nil {
		return manifest, err
	}
	if err := archive.Close(); err != nil {
		return manifest, err
	}
	currentJournal, err := snapshotTrainingBackupJournal(ctx, config.RootDir)
	if err != nil {
		return manifest, err
	}
	if !sameTrainingBackupJournal(journal, currentJournal) {
		return manifest, errors.New("storage cleanup journal changed during backup; retry with cleanup paused")
	}
	return manifest, ctx.Err()
}

// RestoreTrainingBackup only publishes into a new directory. Existing files are
// never overwritten and every archived byte must be covered by the manifest.
func RestoreTrainingBackup(ctx context.Context, filename, destination string) (manifest TrainingBackupManifest, err error) {
	archive, err := zip.OpenReader(filename)
	if err != nil {
		return manifest, err
	}
	defer archive.Close()
	entries := make(map[string]*zip.File)
	for _, entry := range archive.File {
		key := strings.ToLower(entry.Name)
		if !backupPath(entry.Name) || !entry.Mode().IsRegular() || entries[key] != nil {
			return manifest, fmt.Errorf("invalid or duplicate archive entry %q", entry.Name)
		}
		entries[key] = entry
	}
	metadata := entries["manifest.json"]
	if metadata == nil || metadata.UncompressedSize64 > 8<<20 {
		return manifest, errors.New("missing or oversized training manifest")
	}
	reader, err := metadata.Open()
	if err != nil {
		return manifest, err
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	err = json.Unmarshal(data, &manifest)
	reader.Close()
	if readErr != nil || err != nil || len(data) > 8<<20 || (manifest.Version != 1 && manifest.Version != 2) || manifest.DatasetPolicy != "database" || len(manifest.Files)+1 != len(entries) {
		return manifest, errors.New("invalid training manifest")
	}
	if manifest.Version == 2 && manifest.CleanupJournalVersion != 1 || manifest.Version == 1 && manifest.CleanupJournalVersion != 0 {
		return manifest, errors.New("unsupported cleanup journal manifest")
	}
	if manifest.Version == 1 {
		manifest.Warnings = append(manifest.Warnings, "Legacy archive has no storage cleanup journal. Do not resume cleanup or reconcile old operations until the original host journal has been recovered.")
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return manifest, err
	}
	if _, err = os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return manifest, errors.New("restore destination must not exist")
	}
	parent := filepath.Dir(destination)
	staging, err := os.MkdirTemp(parent, ".training-restore-")
	if err != nil {
		return manifest, err
	}
	defer func() {
		if pathWithinDirectory(parent, staging, false) && strings.HasPrefix(filepath.Base(staging), ".training-restore-") {
			_ = os.RemoveAll(staging)
		}
	}()
	root, err := os.OpenRoot(staging)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	seen := make(map[string]bool)
	fileSizes := make(map[string]int64)
	var journalBytes int64
	journalFiles := 0
	for _, item := range manifest.Files {
		key := strings.ToLower(item.Path)
		entry := entries[key]
		if !backupPath(item.Path) || item.Path == "manifest.json" || seen[key] || entry == nil || item.Bytes < 0 || item.Bytes > 64<<30 || uint64(item.Bytes) != entry.UncompressedSize64 {
			return manifest, fmt.Errorf("missing or invalid manifest file %q", item.Path)
		}
		if strings.HasPrefix(key, storageReceiptDirectory+"/") {
			name := strings.TrimPrefix(item.Path, storageReceiptDirectory+"/")
			limit := int64(maxStorageReceiptBytes)
			if strings.HasSuffix(name, ".closed") {
				limit = 1024
			}
			journalBytes += item.Bytes
			journalFiles++
			if manifest.Version != 2 || !validTrainingBackupJournalName(name) || item.Bytes > limit || journalBytes > 64<<20 || journalFiles > 65536 {
				return manifest, errors.New("invalid or oversized cleanup journal")
			}
		}
		seen[key] = true
		fileSizes[key] = item.Bytes
		if err = root.MkdirAll(filepath.FromSlash(path.Dir(item.Path)), 0o750); err != nil {
			return manifest, err
		}
		file, err := root.OpenFile(filepath.FromSlash(item.Path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err != nil {
			return manifest, err
		}
		reader, err := entry.Open()
		if err != nil {
			file.Close()
			return manifest, err
		}
		n, hash, copyErr := backupDigest(ctx, file, io.LimitReader(reader, item.Bytes+1))
		closeErr := file.Close()
		reader.Close()
		if copyErr != nil || closeErr != nil || n != item.Bytes || hash != item.SHA256 {
			return manifest, fmt.Errorf("checksum mismatch or unreadable backup file %q", item.Path)
		}
	}
	jobFiles, gatewayIDs := make(map[string]bool), make(map[string]bool)
	for _, job := range manifest.Jobs {
		if !backupPath(job.AgentID) || job.GatewayID == "" || job.Owner == "" || strings.Contains(job.AgentID, "/") || !seen[strings.ToLower(job.Artifact)] || !strings.HasPrefix(job.Artifact, "Trained/") {
			return manifest, errors.New("invalid restored job linkage")
		}
		name := "jobs/" + job.AgentID + "/status.json"
		if jobFiles[strings.ToLower(name)] || gatewayIDs[job.GatewayID] {
			return manifest, errors.New("duplicate restored job")
		}
		jobFiles[strings.ToLower(name)], gatewayIDs[job.GatewayID] = true, true
		data, err := root.ReadFile(filepath.FromSlash(name))
		if err != nil {
			return manifest, err
		}
		var record jobRecord
		if json.Unmarshal(data, &record) != nil || record.Spec.GatewayJobID != job.GatewayID || record.Status.GatewayJobID != job.GatewayID || record.Status.ID != job.AgentID || record.Spec.ProfileID != job.ProfileID || record.Spec.Owner != job.Owner || record.ArtifactPath != job.Artifact || record.Status.State != "completed" || record.Status.ExecutionUnconfirmed {
			return manifest, errors.New("restored owner/run linkage mismatch")
		}
		if record.Status.ArtifactBytes != fileSizes[strings.ToLower(job.Artifact)] {
			return manifest, errors.New("restored artifact size does not match its execution record")
		}
		profileHash := ""
		if record.ProfileSnapshot != nil {
			profile, _ := json.Marshal(record.ProfileSnapshot)
			hash := sha256.Sum256(profile)
			profileHash = hex.EncodeToString(hash[:])
		}
		if profileHash != job.ProfileSHA256 {
			return manifest, errors.New("restored profile checksum mismatch")
		}
		record.ArtifactPath = filepath.Join(destination, filepath.FromSlash(job.Artifact))
		data, err = json.Marshal(record)
		if err != nil {
			return manifest, err
		}
		if err = root.WriteFile(filepath.FromSlash(name), data, 0o640); err != nil {
			return manifest, err
		}
	}
	for _, item := range manifest.Files {
		journalFile := manifest.Version == 2 && strings.HasPrefix(item.Path, storageReceiptDirectory+"/") && validTrainingBackupJournalName(strings.TrimPrefix(item.Path, storageReceiptDirectory+"/"))
		if !strings.HasPrefix(item.Path, "Trained/") && !jobFiles[strings.ToLower(item.Path)] && !journalFile {
			return manifest, fmt.Errorf("unexpected restored file %q", item.Path)
		}
	}
	if _, err := snapshotTrainingBackupJournal(ctx, staging); err != nil {
		return manifest, fmt.Errorf("invalid restored cleanup journal: %w", err)
	}
	root.Close()
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return manifest, errors.New("restore destination was created concurrently")
	}
	err = os.Rename(staging, destination)
	return manifest, err
}
