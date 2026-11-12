package updateagent

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	comfyguardbackup "ai-access-gateway/integrations/comfyui_gpu_guard"
	"ai-access-gateway/internal/updates"
)

// Restore publishes only a new, isolated directory. Original host bindings are
// retained: restoring bytes is not proof that the original executor has stopped.
func RestoreComfyStorageBackup(ctx context.Context, filename, destination, python string) (ComfyStorageBackupManifest, error) {
	var manifest ComfyStorageBackupManifest
	if !filepath.IsAbs(python) {
		return manifest, errors.New("an absolute Python executable is required for SQLite restore")
	}
	info, err := os.Stat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxComfyStorageBackupBytes+maxComfyStorageBackupManifestBytes+(16<<20) {
		return manifest, ErrComfyStorageBackup
	}
	archive, err := zip.OpenReader(filename)
	if err != nil {
		return manifest, err
	}
	defer archive.Close()
	if len(archive.File) > maxComfyStorageBackupFiles+1 {
		return manifest, ErrComfyStorageBackup
	}
	entries := map[string]*zip.File{}
	aliases := map[string]bool{}
	for _, entry := range archive.File {
		alias := strings.ToLower(entry.Name)
		if !entry.Mode().IsRegular() || aliases[alias] || entry.Name != "manifest.json" && !validComfyBackupFile(entry.Name) {
			return manifest, ErrComfyStorageBackup
		}
		aliases[alias], entries[entry.Name] = true, entry
	}
	metadata, err := readComfyBackupEntry(entries["manifest.json"], maxComfyStorageBackupManifestBytes)
	if err != nil || decodeComfyBackupJSON(metadata, &manifest) != nil || manifest.Version != 1 || manifest.CreatedAt.IsZero() || len(manifest.Files)+1 != len(entries) {
		return manifest, ErrComfyStorageBackup
	}
	files := map[string][]byte{}
	var total int64
	for _, item := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return manifest, err
		}
		limit := int64(maxComfyStorageRecordBytes)
		if item.Path == "guard/snapshot.json" {
			limit = maxComfyGuardBackupBytes
		}
		total += item.Bytes
		if !validComfyBackupFile(item.Path) || files[item.Path] != nil || item.Bytes <= 0 || item.Bytes > limit || total > maxComfyStorageBackupBytes || !updates.ComfyStorageHex(item.SHA256, 32) {
			return manifest, ErrComfyStorageBackup
		}
		payload, err := readComfyBackupEntry(entries[item.Path], limit)
		if err != nil || int64(len(payload)) != item.Bytes || comfyBackupDigest(payload) != item.SHA256 {
			return manifest, ErrComfyStorageBackup
		}
		files[item.Path] = payload
	}
	actual, err := inspectComfyBackupFiles(files)
	if err != nil || !sameComfyBackupSummary(manifest, actual) {
		return manifest, ErrComfyStorageBackup
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return manifest, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return manifest, err
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return manifest, errors.New("restore destination must not exist")
	}
	staging, err := os.MkdirTemp(parent, ".comfy-storage-restore-")
	if err != nil {
		return manifest, err
	}
	defer func() {
		if filepath.Dir(staging) == parent && strings.HasPrefix(filepath.Base(staging), ".comfy-storage-restore-") {
			_ = os.RemoveAll(staging)
		}
	}()
	root, err := os.OpenRoot(staging)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	for _, directory := range []string{"executor/receipts", "guard"} {
		if err := root.MkdirAll(filepath.FromSlash(directory), 0o700); err != nil {
			return manifest, err
		}
	}
	for name, data := range files {
		if err := ctx.Err(); err != nil {
			return manifest, err
		}
		if err := writeComfyBackupRestoredFile(root, filepath.FromSlash(name), data); err != nil {
			return manifest, err
		}
	}
	if err := writeComfyBackupRestoredFile(root, "manifest.json", metadata); err != nil {
		return manifest, err
	}
	if err := writeComfyBackupRestoredFile(root, filepath.Join("executor", comfyStorageRestoreMarker), []byte("Isolated restore only. An older snapshot does not prove executor continuity.\n")); err != nil {
		return manifest, err
	}
	command := exec.CommandContext(ctx, python, "-I", "-S", "-B", "-c", comfyguardbackup.Script, filepath.Join(staging, "guard", "receipts.sqlite"))
	command.Stdin = bytes.NewReader(files["guard/snapshot.json"])
	var output bytes.Buffer
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return manifest, errors.Join(errors.New("isolated guard SQLite restore failed"), err)
	}
	var result struct {
		Success   bool   `json:"success"`
		JournalID string `json:"journal_id"`
		Windows   int    `json:"windows"`
		Receipts  int    `json:"receipts"`
	}
	if output.Len() > 4096 || decodeComfyBackupJSON(output.Bytes(), &result) != nil || !result.Success || result.JournalID != manifest.GuardJournalID || result.Windows != manifest.GuardWindows || result.Receipts != manifest.GPUReceipts {
		return manifest, ErrComfyStorageBackup
	}
	if err := root.Close(); err != nil {
		return manifest, err
	}
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	if err := publishComfyBackupRestore(staging, destination); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func writeComfyBackupRestoredFile(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Sync(), file.Close())
}
