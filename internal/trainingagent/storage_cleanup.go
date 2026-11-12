package trainingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"strings"
	"time"

	"ai-access-gateway/internal/loratraining"
)

const storageReceiptDirectory = ".storage-cleanup"
const maxStorageReceiptBytes = 1 << 20

var ErrStorageOperationConflict = errors.New("training storage operation ID was already used")
var ErrStorageResultExpired = errors.New("training storage result expired; operation ID remains closed")

func (controller *Controller) acquireStorageRead(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if controller.storageMu.TryRLock() {
			return nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (controller *Controller) CleanupStorage(ctx context.Context, input loratraining.StorageCleanupRequest) (loratraining.StorageCleanupResult, error) {
	request, hash, err := loratraining.CanonicalStorageCleanupRequest(input)
	if err != nil {
		return loratraining.StorageCleanupResult{}, err
	}
	if result, err := controller.StorageCleanupResult(ctx, request.ID); err == nil {
		if result.RequestHash != hash {
			return loratraining.StorageCleanupResult{}, ErrStorageOperationConflict
		}
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return loratraining.StorageCleanupResult{}, err
	}
	if !controller.storageMu.TryLock() {
		return loratraining.StorageCleanupResult{}, ErrStorageBusy
	}
	defer controller.storageMu.Unlock()
	if result, err := controller.StorageCleanupResult(ctx, request.ID); err == nil {
		if result.RequestHash != hash {
			return loratraining.StorageCleanupResult{}, ErrStorageOperationConflict
		}
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return loratraining.StorageCleanupResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return loratraining.StorageCleanupResult{}, err
	}
	targets, originals, err := controller.storageTargets()
	if err != nil {
		return loratraining.StorageCleanupResult{}, err
	}
	byID := make(map[string]storageTarget, len(targets))
	for _, target := range targets {
		byID[target.item.ID] = target
	}
	result := loratraining.StorageCleanupResult{ProtocolVersion: loratraining.StorageProtocolVersion, ID: request.ID, RequestHash: hash, State: "running", StartedAt: time.Now().UTC(), Items: []loratraining.StorageCleanupOutcome{}}
	snapshots := make([]storageSnapshot, 0, len(request.Items))
	defer func() {
		for i := range snapshots {
			snapshots[i].close()
		}
	}()
	rejected := false
	for _, selection := range request.Items {
		target, ok := byID[selection.ID]
		if !ok {
			return loratraining.StorageCleanupResult{}, loratraining.ErrStorageRequest
		}
		snapshot, inspectErr := inspectStorageTarget(ctx, target, originals)
		snapshots = append(snapshots, snapshot)
		outcome := loratraining.StorageCleanupOutcome{ID: selection.ID, Kind: target.item.Kind, Label: target.item.Label, Version: selection.Version, Outcome: "not_run"}
		switch {
		case inspectErr != nil:
			outcome.Outcome = "error"
			outcome.Reason = storageErrorReason(inspectErr)
			rejected = true
		case !loratraining.RebuildableStorageKind(target.item.Kind) || snapshot.item.ProtectedReason != "":
			outcome.Outcome = "protected"
			outcome.Reason = snapshot.item.ProtectedReason
			rejected = true
		case snapshot.item.State == "missing":
			outcome.Outcome = "missing"
		case !snapshot.item.CanCleanup:
			outcome.Outcome = "protected"
			outcome.Reason = "storage_unavailable"
			rejected = true
		case snapshot.item.Version != selection.Version:
			outcome.Outcome = "changed"
			outcome.Reason = "file_changed"
			rejected = true
		}
		result.Items = append(result.Items, outcome)
	}
	if rejected {
		result.State = "rejected"
		now := time.Now().UTC()
		result.FinishedAt = &now
		for i := range result.Items {
			if result.Items[i].Outcome == "not_run" {
				result.Items[i].Reason = "plan_rejected"
			}
		}
		return result, controller.writeStorageCleanupResult(result)
	}
	controller.mu.Lock()
	controller.activeStorageOperation = request.ID
	controller.mu.Unlock()
	defer func() { controller.mu.Lock(); controller.activeStorageOperation = ""; controller.mu.Unlock() }()
	// Persist the operation before the first unlink. Replaying an incomplete
	// journal never runs deletion again against a possibly rebuilt cache path.
	if err := controller.writeStorageCleanupResult(result); err != nil {
		return result, err
	}
	partial := false
	for i := range result.Items {
		if result.Items[i].Outcome == "missing" {
			continue
		}
		freed, files, removeErr := removeStorageSnapshot(ctx, &snapshots[i])
		outcome := &result.Items[i]
		outcome.FreedBytes, outcome.DeletedFiles = freed, files
		outcome.Outcome = "deleted"
		if removeErr != nil {
			partial = true
			outcome.Outcome = "error"
			if files > 0 {
				outcome.Outcome = "partial"
			}
			outcome.Reason = storageErrorReason(removeErr)
			log.Printf("training storage cleanup %s target %s: %v", request.ID, outcome.ID, removeErr)
		}
		if err := controller.writeStorageCleanupResult(result); err != nil {
			return result, err
		}
		if ctx.Err() != nil {
			partial = true
			break
		}
	}
	result.State = "completed"
	if partial {
		result.State = "partial"
	}
	now := time.Now().UTC()
	result.FinishedAt = &now
	return result, controller.writeStorageCleanupResult(result)
}

func storageErrorReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case errors.Is(err, errStorageChanged):
		return "file_changed"
	case errors.Is(err, errStoragePath):
		return "unsafe_path"
	case errors.Is(err, errStorageLimit):
		return "scan_limit"
	default:
		return "filesystem_error"
	}
}

// Remove only the inspected entries, never a recursive path chosen by a caller.
// New files make the final directory removal fail and remain on disk.
func removeStorageSnapshot(ctx context.Context, snapshot *storageSnapshot) (int64, int64, error) {
	if snapshot == nil || !snapshot.item.CanCleanup || snapshot.parent == nil || snapshot.info == nil {
		return 0, 0, errStoragePath
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if !snapshot.info.IsDir() {
		current, err := snapshot.parent.Lstat(snapshot.leaf)
		if err != nil {
			return 0, 0, err
		}
		if !sameStorageEntry(snapshot.info, current) {
			return 0, 0, errStorageChanged
		}
		if err := snapshot.parent.Remove(snapshot.leaf); err != nil {
			return 0, 0, err
		}
		return snapshot.info.Size(), 1, nil
	}
	if snapshot.root == nil {
		return 0, 0, errStoragePath
	}
	current, err := snapshot.parent.Lstat(snapshot.leaf)
	if err != nil {
		return 0, 0, err
	}
	if !os.SameFile(snapshot.info, current) || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return 0, 0, errStorageChanged
	}
	var freed, files int64
	for i := len(snapshot.entries) - 1; i >= 0; i-- {
		entry := snapshot.entries[i]
		if entry.name == "." {
			continue
		}
		if err := ctx.Err(); err != nil {
			return freed, files, err
		}
		current, err := snapshot.root.Lstat(entry.name)
		if err != nil {
			return freed, files, err
		}
		if !sameStorageEntry(entry.info, current) {
			return freed, files, errStorageChanged
		}
		if err := snapshot.root.Remove(entry.name); err != nil {
			return freed, files, err
		}
		if entry.info.Mode().IsRegular() {
			freed += entry.info.Size()
			files++
		}
	}
	current, err = snapshot.parent.Lstat(snapshot.leaf)
	if err != nil {
		return freed, files, err
	}
	if !os.SameFile(snapshot.info, current) || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return freed, files, errStorageChanged
	}
	// Closing the target handle permits directory removal on Windows. The
	// parent handle remains rooted and the operation never follows a new link.
	if err := snapshot.root.Close(); err != nil {
		snapshot.root = nil
		return freed, files, err
	}
	snapshot.root = nil
	if err := snapshot.parent.Remove(snapshot.leaf); err != nil {
		return freed, files, err
	}
	return freed, files, nil
}

func sameStorageEntry(before, after fs.FileInfo) bool {
	if before == nil || after == nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || after.Mode()&os.ModeSymlink != 0 {
		return false
	}
	// Removing children changes directory metadata during this very operation.
	return before.IsDir() || before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func (controller *Controller) StorageCleanupResult(ctx context.Context, id string) (loratraining.StorageCleanupResult, error) {
	if !loratraining.ValidStorageOperationID(id) {
		return loratraining.StorageCleanupResult{}, loratraining.ErrStorageRequest
	}
	if err := ctx.Err(); err != nil {
		return loratraining.StorageCleanupResult{}, err
	}
	root, err := os.OpenRoot(controller.config.RootDir)
	if err != nil {
		return loratraining.StorageCleanupResult{}, err
	}
	defer root.Close()
	directory, err := openStorageDirectory(root, storageReceiptDirectory)
	if err != nil {
		return loratraining.StorageCleanupResult{}, err
	}
	defer directory.Close()
	result, err := readStorageCleanupResult(directory, id)
	if errors.Is(err, os.ErrNotExist) {
		if _, closedErr := readStorageClosedID(directory, id); closedErr == nil {
			return result, ErrStorageResultExpired
		} else if !errors.Is(closedErr, os.ErrNotExist) {
			return result, closedErr
		}
	}
	if err != nil {
		return result, err
	}
	if result.State == "running" {
		controller.mu.RLock()
		active := controller.activeStorageOperation == id
		controller.mu.RUnlock()
		if !active {
			result.State = "unconfirmed"
			for i := range result.Items {
				if result.Items[i].Outcome == "not_run" {
					result.Items[i].Outcome = "unconfirmed"
					result.Items[i].Reason = "operation_unconfirmed"
				}
			}
		}
	}
	return result, nil
}

func readStorageCleanupResult(directory *os.Root, id string) (loratraining.StorageCleanupResult, error) {
	var result loratraining.StorageCleanupResult
	info, err := directory.Lstat(id + ".json")
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxStorageReceiptBytes {
		return result, errStoragePath
	}
	file, err := directory.Open(id + ".json")
	if err != nil {
		return result, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameStorageEntry(info, opened) {
		return result, errors.Join(err, errStorageChanged)
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxStorageReceiptBytes+1))
	if err != nil || len(payload) > maxStorageReceiptBytes {
		return result, errors.Join(err, errStorageLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return result, loratraining.ErrStorageRequest
	}
	if result.ID != id {
		return result, loratraining.ErrStorageRequest
	}
	return result, loratraining.ValidateStorageCleanupResult(result)
}

func (controller *Controller) writeStorageCleanupResult(result loratraining.StorageCleanupResult) error {
	if err := loratraining.ValidateStorageCleanupResult(result); err != nil {
		return err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(payload) > maxStorageReceiptBytes {
		return errStorageLimit
	}
	root, err := os.OpenRoot(controller.config.RootDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir(storageReceiptDirectory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	directory, err := openStorageDirectory(root, storageReceiptDirectory)
	if err != nil {
		return err
	}
	defer directory.Close()
	id, err := randomID()
	if err != nil {
		return err
	}
	temporary := result.ID + "-" + id + ".partial"
	file, err := directory.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer directory.Remove(temporary)
	_, writeErr := file.Write(payload)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	return directory.Rename(temporary, result.ID+".json")
}

func (controller *Controller) pruneStorageCleanupReceipts(now time.Time) error {
	root, err := os.OpenRoot(controller.config.RootDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := openStorageDirectory(root, storageReceiptDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer directory.Close()
	entries, err := fs.ReadDir(directory.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if id == entry.Name() || !loratraining.ValidStorageOperationID(id) {
			continue
		}
		result, err := readStorageCleanupResult(directory, id)
		if err != nil {
			return err
		}
		if result.State == "running" || result.State == "unconfirmed" || result.FinishedAt == nil || !result.FinishedAt.Before(now.Add(-loratraining.StorageReceiptRetention)) {
			continue
		}
		if err := writeStorageClosedID(directory, result.ID, result.RequestHash); err != nil {
			return err
		}
		if err := directory.Remove(entry.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

type storageClosedID struct {
	ID          string `json:"id"`
	RequestHash string `json:"request_hash"`
}

func readStorageClosedID(directory *os.Root, id string) (storageClosedID, error) {
	var closed storageClosedID
	info, err := directory.Lstat(id + ".closed")
	if err != nil {
		return closed, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 {
		return closed, errStoragePath
	}
	file, err := directory.Open(id + ".closed")
	if err != nil {
		return closed, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameStorageEntry(info, opened) {
		return closed, errors.Join(err, errStorageChanged)
	}
	payload, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil || len(payload) > 1024 {
		return closed, errors.Join(err, errStorageLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&closed); err != nil {
		return closed, err
	}
	if decoder.Decode(new(any)) != io.EOF || closed.ID != id || !loratraining.ValidStorageVersion(closed.RequestHash) {
		return closed, loratraining.ErrStorageRequest
	}
	return closed, nil
}

func writeStorageClosedID(directory *os.Root, id, hash string) error {
	if existing, err := readStorageClosedID(directory, id); err == nil {
		if existing.RequestHash != hash {
			return ErrStorageOperationConflict
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	payload, err := json.Marshal(storageClosedID{ID: id, RequestHash: hash})
	if err != nil {
		return err
	}
	nonce, err := randomID()
	if err != nil {
		return err
	}
	temporary := id + "-" + nonce + ".partial"
	file, err := directory.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer directory.Remove(temporary)
	_, writeErr := file.Write(payload)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	return directory.Rename(temporary, id+".closed")
}

// Closing an operation fences its ID, not an active process. A lost local
// journal outcome remains unknown even after no future execution is possible.
func (controller *Controller) FenceStorageCleanup(ctx context.Context, input loratraining.StorageCleanupRequest) (loratraining.StorageCleanupFenceResult, error) {
	request, hash, err := loratraining.CanonicalStorageCleanupRequest(input)
	if err != nil {
		return loratraining.StorageCleanupFenceResult{}, err
	}
	if !controller.storageMu.TryLock() {
		return loratraining.StorageCleanupFenceResult{}, ErrStorageBusy
	}
	defer controller.storageMu.Unlock()
	result, err := controller.StorageCleanupResult(ctx, request.ID)
	expired := errors.Is(err, ErrStorageResultExpired)
	if expired {
		root, openErr := os.OpenRoot(controller.config.RootDir)
		if openErr != nil {
			return loratraining.StorageCleanupFenceResult{}, openErr
		}
		defer root.Close()
		directory, openErr := openStorageDirectory(root, storageReceiptDirectory)
		if openErr != nil {
			return loratraining.StorageCleanupFenceResult{}, openErr
		}
		defer directory.Close()
		closed, readErr := readStorageClosedID(directory, request.ID)
		if readErr != nil {
			return loratraining.StorageCleanupFenceResult{}, readErr
		}
		if closed.RequestHash != hash {
			return loratraining.StorageCleanupFenceResult{}, ErrStorageOperationConflict
		}
	}
	if errors.Is(err, os.ErrNotExist) || expired {
		now := time.Now().UTC()
		result = loratraining.StorageCleanupResult{ProtocolVersion: 1, ID: request.ID, RequestHash: hash, State: "rejected", StartedAt: now, FinishedAt: &now, Items: []loratraining.StorageCleanupOutcome{}}
		for _, item := range request.Items {
			kind, _, _ := strings.Cut(item.ID, ":")
			outcome, reason := "not_run", "request_fenced"
			if expired {
				outcome, reason = "unconfirmed", "receipt_expired"
				result.State = "closed_unconfirmed"
			}
			result.Items = append(result.Items, loratraining.StorageCleanupOutcome{ID: item.ID, Kind: kind, Label: item.ID, Version: item.Version, Outcome: outcome, Reason: reason})
		}
	} else if err != nil {
		return loratraining.StorageCleanupFenceResult{}, err
	}
	if result.RequestHash != hash {
		return loratraining.StorageCleanupFenceResult{}, ErrStorageOperationConflict
	}
	if result.State == "running" {
		return loratraining.StorageCleanupFenceResult{}, ErrStorageBusy
	}
	if result.State == "unconfirmed" {
		result.State = "closed_unconfirmed"
		now := time.Now().UTC()
		result.FinishedAt = &now
	}
	if !expired {
		if err := controller.writeStorageCleanupResult(result); err != nil {
			return loratraining.StorageCleanupFenceResult{}, err
		}
	}
	return loratraining.StorageCleanupFenceResult{ProtocolVersion: 1, Fenced: true, Settled: true, Result: result}, nil
}
