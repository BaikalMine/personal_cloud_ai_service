package trainingagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ai-access-gateway/internal/loratraining"
)

const maxStorageEntries = 100000

var errStorageChanged = errors.New("training storage target changed")
var errStoragePath = errors.New("training storage path is not eligible")
var errStorageLimit = errors.New("training storage scan limit exceeded")
var ErrStorageBusy = errors.New("training storage is in use")

type storageTarget struct {
	item      loratraining.StorageItem
	root      string
	relative  string
	original  string
	directory bool
}

type storageEntry struct {
	name string
	info fs.FileInfo
}

type storageSnapshot struct {
	item    loratraining.StorageItem
	parent  *os.Root
	root    *os.Root
	leaf    string
	info    fs.FileInfo
	entries []storageEntry
}

func (snapshot *storageSnapshot) close() {
	if snapshot.root != nil {
		_ = snapshot.root.Close()
	}
	if snapshot.parent != nil {
		_ = snapshot.parent.Close()
	}
}

func storageID(kind, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return kind + ":" + hex.EncodeToString(sum[:16])
}

// The catalog comes from controller records/configuration and its private model
// directory. A request never supplies a path to inspect or delete.
func (controller *Controller) storageTargets() ([]storageTarget, []fs.FileInfo, error) {
	controller.mu.RLock()
	records := make([]*jobRecord, 0, len(controller.jobs))
	for _, record := range controller.jobs {
		if record != nil {
			records = append(records, cloneRecord(record))
		}
	}
	pending := controller.pendingSubmissions
	maintenance := controller.activeStorageOperation != ""
	controller.mu.RUnlock()
	profiles := append([]ProfileConfig(nil), controller.config.Profiles...)
	busy := ""
	if pending > 0 {
		busy = "submission_in_progress"
	}
	if maintenance {
		busy = "storage_cleanup_active"
	}
	for _, record := range records {
		if record.ProfileSnapshot != nil {
			profiles = append(profiles, *record.ProfileSnapshot)
		}
		if record.Status.ExecutionUnconfirmed {
			busy = "execution_unconfirmed"
		}
		if !record.Status.Terminal() && busy == "" {
			busy = "training_busy"
		}
	}
	originals := make(map[string]bool)
	var originalInfos []fs.FileInfo
	targets := make(map[string]storageTarget)
	for _, profile := range profiles {
		if profile.StripPrefix != "" {
			for _, suffix := range []string{"", ".partial"} {
				name := profile.ID + "-format-v2.safetensors" + suffix
				id := storageID("normalized_model", "models/"+name)
				targets[id] = storageTarget{item: loratraining.StorageItem{ID: id, Kind: "normalized_model", Label: name, ProfileID: profile.ID, ProtectedReason: busy}, root: controller.config.RootDir, relative: path.Join("models", name)}
			}
		}
		for _, filename := range []string{profile.DiT, profile.VAE, profile.TextEncoder} {
			if filename == "" {
				continue
			}
			filename = filepath.Clean(filename)
			if originals[filename] {
				continue
			}
			originals[filename] = true
			id := storageID("original_model", filename)
			targets[id] = storageTarget{item: loratraining.StorageItem{ID: id, Kind: "original_model", Label: filepath.Base(filename), ProtectedReason: "original_model"}, original: filename}
			if info, err := os.Stat(filename); err == nil {
				originalInfos = append(originalInfos, info)
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, nil, fmt.Errorf("inspect original model identity: %w", err)
			}
		}
	}
	normalizedProtection := make(map[string]string)
	for _, profile := range profiles {
		if profile.StripPrefix == "" {
			continue
		}
		reason := "source_model_missing"
		info, err := os.Stat(profile.DiT)
		if err == nil && info.Mode().IsRegular() {
			reason = ""
		} else if err == nil {
			reason = "source_model_unavailable"
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("inspect normalized model source: %w", err)
		}
		if existing, ok := normalizedProtection[profile.ID]; !ok || existing != "" {
			normalizedProtection[profile.ID] = reason
		}
	}
	root, err := os.OpenRoot(controller.config.RootDir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	models, err := openStorageDirectory(root, "models")
	if err != nil {
		return nil, nil, err
	}
	defer models.Close()
	entries, err := fs.ReadDir(models.FS(), ".")
	if err != nil {
		return nil, nil, err
	}
	if len(entries) > maxStorageEntries {
		return nil, nil, errStorageLimit
	}
	for _, entry := range entries {
		name := entry.Name()
		kind := "unmanaged_file"
		profile := strings.TrimSuffix(strings.TrimSuffix(name, ".partial"), "-format-v2.safetensors")
		if profileIDPattern.MatchString(profile) && (name == profile+"-format-v2.safetensors" || name == profile+"-format-v2.safetensors.partial") {
			kind = "normalized_model"
		}
		id := storageID(kind, "models/"+name)
		reason := busy
		if kind == "unmanaged_file" {
			reason = "unmanaged_path"
			profile = ""
		} else if reason == "" {
			var known bool
			reason, known = normalizedProtection[profile]
			if !known {
				reason = "source_profile_missing"
			}
		}
		targets[id] = storageTarget{item: loratraining.StorageItem{ID: id, Kind: kind, Label: name, ProfileID: profile, ProtectedReason: reason}, root: controller.config.RootDir, relative: path.Join("models", name), directory: entry.IsDir() && kind == "unmanaged_file"}
	}
	for _, record := range records {
		id := record.Status.ID
		// Invalid historical directory names remain outside the cleanup catalog.
		if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, "/\\:\x00") {
			return nil, nil, errStoragePath
		}
		reason := busy
		if record.Status.ExecutionUnconfirmed {
			reason = "execution_unconfirmed"
		} else if !record.Status.Terminal() {
			reason = "active_job"
		}
		for _, part := range []struct {
			kind, name string
			directory  bool
		}{
			{"dataset_archive", "dataset.zip", false}, {"dataset_files", "dataset", true}, {"training_cache", "cache", true}, {"training_output", "output", true},
		} {
			relative := path.Join("jobs", id, part.name)
			key := storageID(part.kind, relative)
			targets[key] = storageTarget{item: loratraining.StorageItem{ID: key, Kind: part.kind, Label: part.name, JobID: id, GatewayJobID: record.Spec.GatewayJobID, ProfileID: record.Spec.ProfileID, ProtectedReason: reason}, root: controller.config.RootDir, relative: relative, directory: part.directory}
		}
		if record.ArtifactPath != "" {
			relative, err := filepath.Rel(controller.config.ComfyLoraDir, record.ArtifactPath)
			if err != nil || !pathWithinDirectory(filepath.Join(controller.config.ComfyLoraDir, "Trained"), record.ArtifactPath, false) {
				return nil, nil, errStoragePath
			}
			key := storageID("trained_artifact", id)
			targets[key] = storageTarget{item: loratraining.StorageItem{ID: key, Kind: "trained_artifact", Label: record.Status.ArtifactName, JobID: id, GatewayJobID: record.Spec.GatewayJobID, ProfileID: record.Spec.ProfileID, ProtectedReason: "artifact_lifecycle"}, root: controller.config.ComfyLoraDir, relative: filepath.ToSlash(relative)}
		}
	}
	ordered := make([]storageTarget, 0, len(targets))
	for _, target := range targets {
		ordered = append(ordered, target)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].item.ID < ordered[j].item.ID })
	return ordered, originalInfos, nil
}

func (controller *Controller) StorageInventory(ctx context.Context, cursor string, limit int) (loratraining.StorageInventory, error) {
	if limit < 1 || limit > 100 || (cursor != "" && !loratraining.ValidStorageTargetID(cursor)) {
		return loratraining.StorageInventory{}, loratraining.ErrStorageRequest
	}
	targets, originals, err := controller.storageTargets()
	if err != nil {
		return loratraining.StorageInventory{}, err
	}
	result := loratraining.StorageInventory{ProtocolVersion: loratraining.StorageProtocolVersion, CheckedAt: time.Now().UTC(), Complete: true, TotalItems: len(targets), Items: []loratraining.StorageItem{}}
	for _, target := range targets {
		if target.item.ID <= cursor {
			continue
		}
		if len(result.Items) == limit {
			result.NextCursor = result.Items[len(result.Items)-1].ID
			break
		}
		snapshot, err := inspectStorageTarget(ctx, target, originals)
		snapshot.close()
		if err != nil {
			result.Complete = false
		}
		result.Items = append(result.Items, snapshot.item)
	}
	return result, nil
}

func (controller *Controller) PreviewStorageCleanup(ctx context.Context, ids []string) (loratraining.StorageInventory, error) {
	if err := loratraining.ValidateStorageIDs(ids); err != nil {
		return loratraining.StorageInventory{}, err
	}
	targets, originals, err := controller.storageTargets()
	if err != nil {
		return loratraining.StorageInventory{}, err
	}
	byID := make(map[string]storageTarget, len(targets))
	for _, target := range targets {
		byID[target.item.ID] = target
	}
	result := loratraining.StorageInventory{ProtocolVersion: loratraining.StorageProtocolVersion, CheckedAt: time.Now().UTC(), Complete: true, TotalItems: len(ids), Items: []loratraining.StorageItem{}}
	for _, id := range ids {
		target, ok := byID[id]
		if !ok {
			return loratraining.StorageInventory{}, loratraining.ErrStorageRequest
		}
		snapshot, err := inspectStorageTarget(ctx, target, originals)
		snapshot.close()
		if err != nil {
			result.Complete = false
		}
		result.Items = append(result.Items, snapshot.item)
	}
	return result, nil
}

func inspectStorageTarget(ctx context.Context, target storageTarget, originals []fs.FileInfo) (snapshot storageSnapshot, err error) {
	snapshot.item = target.item
	snapshot.item.State = "unavailable"
	defer func() {
		if err != nil {
			snapshot.item.State = "unavailable"
			snapshot.item.CanCleanup = false
			snapshot.item.Version = ""
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	if target.original != "" {
		snapshot.info, err = os.Stat(target.original)
	} else {
		var root *os.Root
		root, err = os.OpenRoot(target.root)
		if err != nil {
			return
		}
		defer root.Close()
		snapshot.parent, err = openStorageDirectory(root, path.Dir(target.relative))
		if err == nil {
			snapshot.leaf = path.Base(target.relative)
			snapshot.info, err = snapshot.parent.Lstat(snapshot.leaf)
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		err = nil
		snapshot.item.State = "missing"
		sum := sha256.Sum256([]byte(target.item.ID + "\x00missing"))
		snapshot.item.Version = hex.EncodeToString(sum[:])
		return
	}
	if err != nil {
		return
	}
	if snapshot.info.Mode()&os.ModeSymlink != 0 || snapshot.info.IsDir() != target.directory || (!snapshot.info.IsDir() && !snapshot.info.Mode().IsRegular()) {
		err = errStoragePath
		return
	}
	for _, original := range originals {
		if target.item.Kind != "original_model" && os.SameFile(original, snapshot.info) {
			snapshot.item.ProtectedReason = "original_model"
		}
	}
	if target.directory {
		snapshot.root, err = snapshot.parent.OpenRoot(snapshot.leaf)
		if err != nil {
			return
		}
		var opened fs.FileInfo
		opened, err = snapshot.root.Lstat(".")
		if err != nil {
			return
		}
		if !os.SameFile(snapshot.info, opened) {
			err = errStorageChanged
			return
		}
		err = fs.WalkDir(snapshot.root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(snapshot.entries) >= maxStorageEntries {
				return errStorageLimit
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				return errStoragePath
			}
			for _, original := range originals {
				if os.SameFile(original, info) {
					snapshot.item.ProtectedReason = "original_model"
				}
			}
			snapshot.entries = append(snapshot.entries, storageEntry{name: name, info: info})
			return nil
		})
		if err != nil {
			return
		}
	} else {
		snapshot.entries = []storageEntry{{name: ".", info: snapshot.info}}
	}
	digest := sha256.New()
	fmt.Fprintf(digest, "%s\x00", snapshot.item.ID)
	for _, entry := range snapshot.entries {
		// Windows may refresh a directory's cached mtime when it is traversed.
		// Membership is bound by entry names; only file metadata carries a time.
		if entry.info.IsDir() {
			fmt.Fprintf(digest, "%s\x00%d\n", entry.name, entry.info.Mode())
		} else {
			fmt.Fprintf(digest, "%s\x00%d\x00%d\x00%d\n", entry.name, entry.info.Mode(), entry.info.Size(), entry.info.ModTime().UnixNano())
		}
		if entry.info.Mode().IsRegular() {
			if entry.info.Size() < 0 || snapshot.item.SizeBytes > 1<<50-entry.info.Size() {
				err = errStorageLimit
				return
			}
			snapshot.item.SizeBytes += entry.info.Size()
			snapshot.item.Files++
		}
	}
	snapshot.item.Version = hex.EncodeToString(digest.Sum(nil))
	snapshot.item.State = "present"
	snapshot.item.CanCleanup = loratraining.RebuildableStorageKind(snapshot.item.Kind) && snapshot.item.ProtectedReason == ""
	return
}

// Each directory handle stays anchored after opening. Symlinks and changed
// components are refused even when their target would remain inside the root.
func openStorageDirectory(root *os.Root, relative string) (*os.Root, error) {
	if relative == "." {
		return root.OpenRoot(".")
	}
	if !fs.ValidPath(relative) {
		return nil, errStoragePath
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(relative, "/") {
		info, err := current.Lstat(part)
		if err != nil {
			current.Close()
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, errStoragePath
		}
		next, err := current.OpenRoot(part)
		current.Close()
		if err != nil {
			return nil, err
		}
		opened, err := next.Lstat(".")
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, errors.Join(err, errStorageChanged)
		}
		current = next
	}
	return current, nil
}
