package updateagent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"ai-access-gateway/internal/updates"
)

const maxComfyStorageBackupFiles = 65540
const maxComfyStorageBackupBytes = 128 << 20
const maxComfyStorageBackupManifestBytes = 16 << 20

type ComfyStorageBackupFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type ComfyStorageBackupManifest struct {
	Version        int                      `json:"version"`
	CreatedAt      time.Time                `json:"created_at"`
	ExecutorID     string                   `json:"executor_id"`
	GuardJournalID string                   `json:"guard_journal_id"`
	PendingID      string                   `json:"pending_id"`
	HeldID         string                   `json:"held_id"`
	HostReceipts   int                      `json:"host_receipts"`
	GuardWindows   int                      `json:"guard_windows"`
	GPUReceipts    int                      `json:"gpu_receipts"`
	Files          []ComfyStorageBackupFile `json:"files"`
}

func validComfyBackupFile(name string) bool {
	if name == "executor/identity.json" || name == "executor/pending.json" || name == "guard/snapshot.json" {
		return true
	}
	return strings.HasPrefix(name, "executor/receipts/") && strings.HasSuffix(name, ".json") &&
		updates.ValidComfyStorageID(strings.TrimSuffix(strings.TrimPrefix(name, "executor/receipts/"), ".json"))
}

func comfyBackupDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func comfyBackupPartial(name string) bool {
	return strings.HasPrefix(name, ".write-") && strings.HasSuffix(name, ".partial") && updates.ComfyStorageHex(strings.TrimSuffix(strings.TrimPrefix(name, ".write-"), ".partial"), 16)
}

// Export never initializes a missing journal or invokes any update controller.
func ExportComfyStorageBackup(ctx context.Context, target ComfyTarget, destination io.Writer) (ComfyStorageBackupManifest, error) {
	if err := ctx.Err(); err != nil {
		return ComfyStorageBackupManifest{}, err
	}
	executor, err := newComfyStorageExecutor(target, false)
	if err != nil {
		return ComfyStorageBackupManifest{}, err
	}
	defer executor.guard.client.CloseIdleConnections()
	return executor.ExportBackup(ctx, destination)
}

func (e *ComfyStorageExecutor) ExportBackup(ctx context.Context, destination io.Writer) (ComfyStorageBackupManifest, error) {
	var manifest ComfyStorageBackupManifest
	root, lock, err := e.openJournal()
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	defer lock.Close()
	files, err := e.backupJournal(ctx, root)
	if err != nil {
		return manifest, err
	}
	guard, err := e.guard.backup(ctx)
	if err != nil {
		return manifest, err
	}
	payload, err := json.Marshal(guard)
	if err != nil || len(payload) > maxComfyGuardBackupBytes {
		return manifest, ErrComfyStorageBackup
	}
	files["guard/snapshot.json"] = payload
	manifest, err = inspectComfyBackupFiles(files)
	if err != nil {
		return manifest, err
	}
	manifest.Version, manifest.CreatedAt = 1, time.Now().UTC()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	archive := zip.NewWriter(destination)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return manifest, err
		}
		data := files[name]
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o600)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return manifest, err
		}
		if _, err := writer.Write(data); err != nil {
			return manifest, err
		}
		manifest.Files = append(manifest.Files, ComfyStorageBackupFile{Path: name, Bytes: int64(len(data)), SHA256: comfyBackupDigest(data)})
	}
	metadata, err := json.Marshal(manifest)
	if err != nil || len(metadata) > maxComfyStorageBackupManifestBytes {
		return manifest, ErrComfyStorageBackup
	}
	header := &zip.FileHeader{Name: "manifest.json", Method: zip.Deflate}
	header.SetMode(0o600)
	writer, err := archive.CreateHeader(header)
	if err != nil {
		return manifest, err
	}
	if _, err := writer.Write(metadata); err != nil {
		return manifest, err
	}
	if err := archive.Close(); err != nil {
		return manifest, err
	}
	// The file lock excludes cooperating executors and pruning. A second guard
	// snapshot also rejects independent SQLite changes while the ZIP was written.
	after, err := e.guard.backup(ctx)
	if err != nil {
		return manifest, err
	}
	afterPayload, err := json.Marshal(after)
	if err != nil || !bytes.Equal(payload, afterPayload) {
		return manifest, ErrComfyStorageBackup
	}
	current, err := e.currentIdentity()
	if err != nil || current != e.identity {
		return manifest, ErrComfyStorageIdentity
	}
	return manifest, ctx.Err()
}

func (e *ComfyStorageExecutor) backupJournal(ctx context.Context, root *os.Root) (map[string][]byte, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil || len(entries) > maxComfyStorageBackupFiles {
		return nil, ErrComfyStorageBackup
	}
	files := map[string][]byte{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if name == "receipts" && entry.IsDir() {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, ErrComfyStorageBackup
		}
		switch name {
		case ".lock":
		case "identity.json":
			data, _ := json.Marshal(e.identity)
			files["executor/identity.json"] = data
		case comfyStoragePendingFile:
			result, err := readComfyStorageRecord(root, name, "")
			if err != nil {
				return nil, err
			}
			files["executor/pending.json"], _ = json.Marshal(result)
		default:
			if !comfyBackupPartial(name) {
				return nil, ErrComfyStorageBackup
			}
		}
	}
	receipts, err := comfyStorageReceiptsRoot(root)
	if err != nil {
		return nil, err
	}
	defer receipts.Close()
	entries, err = fs.ReadDir(receipts.FS(), ".")
	if err != nil || len(entries) > maxComfyStorageBackupFiles-3 {
		return nil, ErrComfyStorageBackup
	}
	var total int
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if !entry.Type().IsRegular() {
			return nil, ErrComfyStorageBackup
		}
		if comfyBackupPartial(name) {
			continue
		}
		if !validComfyBackupFile("executor/receipts/" + name) {
			return nil, ErrComfyStorageBackup
		}
		result, err := readComfyStorageRecord(receipts, name, strings.TrimSuffix(name, ".json"))
		if err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(result)
		total += len(payload)
		if total > 64<<20 {
			return nil, ErrComfyStorageBackup
		}
		files["executor/receipts/"+name] = payload
	}
	return files, nil
}

func inspectComfyBackupFiles(files map[string][]byte) (ComfyStorageBackupManifest, error) {
	var manifest ComfyStorageBackupManifest
	var identity comfyStorageIdentity
	var guard comfyGuardBackup
	if len(files) < 2 || len(files) > maxComfyStorageBackupFiles || decodeComfyBackupJSON(files["executor/identity.json"], &identity) != nil || identity.Protocol != 1 || decodeComfyBackupJSON(files["guard/snapshot.json"], &guard) != nil || guard.validate() != nil {
		return manifest, ErrComfyStorageBackup
	}
	for _, hash := range []string{identity.ExecutorID, identity.JournalRoot, identity.ReceiptsRoot, identity.InputRoot, identity.OutputRoot, identity.GuardBinding} {
		if !updates.ComfyStorageHex(hash, 32) {
			return manifest, ErrComfyStorageBackup
		}
	}
	manifest.ExecutorID, manifest.GuardJournalID = identity.ExecutorID, guard.JournalID
	manifest.GuardWindows, manifest.GPUReceipts = len(guard.Windows), len(guard.Receipts)
	windows := map[string]comfyGuardBackupWindow{}
	for _, window := range guard.Windows {
		windows[window.ID] = window
		if window.State == "held" {
			manifest.HeldID = window.ID
		}
	}
	known, aliases := map[string]string{}, map[string]bool{}
	var pending *updates.ComfyStorageResult
	var total int64
	for name, data := range files {
		alias := strings.ToLower(name)
		total += int64(len(data))
		if !validComfyBackupFile(name) || aliases[alias] || total > maxComfyStorageBackupBytes {
			return manifest, ErrComfyStorageBackup
		}
		aliases[alias] = true
		if name == "executor/identity.json" || name == "guard/snapshot.json" {
			continue
		}
		var result updates.ComfyStorageResult
		if len(data) > maxComfyStorageRecordBytes || decodeComfyBackupJSON(data, &result) != nil || updates.ValidateComfyStorageResult(result) != nil || result.ExecutorID != identity.ExecutorID || result.GuardJournalID != guard.JournalID {
			return manifest, ErrComfyStorageBackup
		}
		if old, found := known[result.ID]; found && old != result.RequestHash {
			return manifest, ErrComfyStorageBackup
		}
		known[result.ID] = result.RequestHash
		window, found := windows[result.ID]
		if found && (window.ExecutorID != result.ExecutorID || window.RequestHash != result.RequestHash) {
			return manifest, ErrComfyStorageBackup
		}
		if name == "executor/pending.json" {
			pending = &result
			manifest.PendingID = result.ID
			if result.GuardClosed && (!found || window.State != "closed") || result.MutationStarted && (!found || window.State == "closed" && !result.ExecutorFenced) {
				return manifest, ErrComfyStorageBackup
			}
		} else {
			manifest.HostReceipts++
			if name != "executor/receipts/"+result.ID+".json" || !result.ExecutorFenced || !result.GuardClosed || !found || window.State != "closed" {
				return manifest, ErrComfyStorageBackup
			}
		}
	}
	for _, window := range guard.Windows {
		if window.ExecutorID == identity.ExecutorID && known[window.ID] != window.RequestHash {
			return manifest, ErrComfyStorageBackup
		}
		if window.State == "held" && (pending == nil || pending.ID != window.ID || pending.ExecutorID != window.ExecutorID || pending.GuardClosed) {
			return manifest, ErrComfyStorageBackup
		}
	}
	return manifest, nil
}

func sameComfyBackupSummary(a, b ComfyStorageBackupManifest) bool {
	return a.ExecutorID == b.ExecutorID && a.GuardJournalID == b.GuardJournalID && a.PendingID == b.PendingID && a.HeldID == b.HeldID && a.HostReceipts == b.HostReceipts && a.GuardWindows == b.GuardWindows && a.GPUReceipts == b.GPUReceipts
}

func readComfyBackupEntry(entry *zip.File, limit int64) ([]byte, error) {
	if entry == nil || entry.UncompressedSize64 > uint64(limit) {
		return nil, ErrComfyStorageBackup
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || len(payload) > int(limit) || uint64(len(payload)) != entry.UncompressedSize64 {
		return nil, errors.Join(ErrComfyStorageBackup, err)
	}
	return payload, nil
}
