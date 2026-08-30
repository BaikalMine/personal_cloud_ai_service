package gateway

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"ai-access-gateway/internal/updates"
)

const expiredComfyMediaBatch = 50
const expiredComfyInputBatch = 50
const comfyCleanupRetryDelay = 6 * time.Hour

// deleteExpiredComfyMedia removes the encrypted database copy only after the
// update agent has examined the matching Windows output file. The agent itself
// deletes a file only when its name, location, size, and SHA-256 all match.
func (a *App) deleteExpiredComfyMedia(ctx context.Context) (int64, error) {
	items, err := a.store.ExpiredComfyMedia(ctx, expiredComfyMediaBatch)
	if err != nil {
		return 0, err
	}
	archivedRows := int64(0)
	if len(items) > 0 {
		archivedRows, err = a.store.QueueExpiredComfyOutputCleanup(ctx, items)
		if err != nil {
			return 0, err
		}
	}
	tombstones, err := a.store.DueComfyOutputCleanup(ctx, expiredComfyMediaBatch)
	if err != nil || len(tombstones) == 0 {
		return archivedRows, err
	}
	files := make([]updates.ComfyOutputFile, 0, len(tombstones))
	idsByKey := make(map[string][]int64, len(tombstones))
	allIDs := make([]int64, 0, len(tombstones))
	for _, item := range tombstones {
		file := updates.ComfyOutputFile{Filename: item.Filename, Subfolder: item.Subfolder, StorageType: item.StorageType, SizeBytes: item.SizeBytes, SHA256: item.ContentHash}
		files = append(files, file)
		key := comfyAssetCleanupKey(file.Filename, file.Subfolder, file.StorageType, file.SizeBytes, file.SHA256)
		idsByKey[key] = append(idsByKey[key], item.ID)
		allIDs = append(allIDs, item.ID)
	}
	result, cleanupErr := a.updates.DeleteComfyOutputs(ctx, files)
	if cleanupErr != nil {
		deferCtx, deferCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = a.store.DeferComfyOutputCleanup(deferCtx, allIDs, comfyCleanupRetryDelay)
		deferCancel()
		return archivedRows, fmt.Errorf("request matched ComfyUI output cleanup: %w", cleanupErr)
	}
	confirmed, deferred := confirmedComfyCleanupIDs(result, idsByKey, allIDs, len(files))
	deletedTombstones, err := a.store.DeleteComfyOutputCleanupByIDs(ctx, confirmed)
	if err != nil {
		return archivedRows, err
	}
	if _, err := a.store.DeferComfyOutputCleanup(ctx, deferred, comfyCleanupRetryDelay); err != nil {
		return archivedRows + deletedTombstones, err
	}
	if result.Deleted > 0 || result.Missing > 0 || result.Mismatched > 0 || result.Rejected > 0 {
		log.Printf("processed expired ComfyUI output tombstones: deleted=%d missing=%d mismatched=%d rejected=%d", result.Deleted, result.Missing, result.Mismatched, result.Rejected)
	}
	if result.Rejected > 0 {
		return archivedRows + deletedTombstones, fmt.Errorf("agent rejected %d ComfyUI output cleanup records", result.Rejected)
	}
	return archivedRows + deletedTombstones, nil
}

// deleteExpiredComfyInputs removes only files whose exact tracked bytes are
// confirmed by the host agent. Mismatches remain registered so they cannot
// silently escape the global disk quota.
func (a *App) deleteExpiredComfyInputs(ctx context.Context) (int64, error) {
	items, err := a.store.ExpiredComfyInputAssets(ctx, expiredComfyInputBatch)
	if err != nil || len(items) == 0 {
		return 0, err
	}
	files := make([]updates.ComfyAssetFile, 0, len(items))
	idsByKey := make(map[string][]string, len(items))
	allIDs := make([]string, 0, len(items))
	for _, item := range items {
		file := updates.ComfyAssetFile{
			Filename: item.Filename, Subfolder: item.Subfolder, StorageType: item.StorageType,
			SizeBytes: item.SizeBytes, SHA256: item.ContentHash,
		}
		if item.State == "reserved" {
			asset := comfyUploadAsset{Filename: item.Filename, Subfolder: item.Subfolder}
			if sizeBytes, contentHash, fingerprintErr := a.comfyStoredInputFingerprint(ctx, asset); fingerprintErr == nil {
				file.SizeBytes = sizeBytes
				file.SHA256 = contentHash
			}
		}
		files = append(files, file)
		key := comfyAssetCleanupKey(file.Filename, file.Subfolder, file.StorageType, file.SizeBytes, file.SHA256)
		idsByKey[key] = append(idsByKey[key], item.ID)
		allIDs = append(allIDs, item.ID)
	}
	result, err := a.updates.DeleteComfyAssets(ctx, files)
	if err != nil {
		deferCtx, deferCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = a.store.DeferComfyInputCleanup(deferCtx, allIDs, comfyCleanupRetryDelay)
		deferCancel()
		return 0, fmt.Errorf("request matched ComfyUI input cleanup: %w", err)
	}
	ids, deferred := confirmedComfyCleanupIDs(result, idsByKey, allIDs, len(files))
	if result.Deleted > 0 || result.Missing > 0 || result.Mismatched > 0 {
		log.Printf("processed expired ComfyUI input records: deleted=%d missing=%d mismatched=%d", result.Deleted, result.Missing, result.Mismatched)
	}
	deleted, err := a.store.DeleteComfyInputAssetsByIDs(ctx, ids)
	if err != nil {
		return deleted, err
	}
	if _, err := a.store.DeferComfyInputCleanup(ctx, deferred, comfyCleanupRetryDelay); err != nil {
		return deleted, err
	}
	if result.Rejected > 0 {
		return deleted, fmt.Errorf("agent rejected %d ComfyUI input cleanup records", result.Rejected)
	}
	return deleted, nil
}

type comfyCleanupID interface{ ~int64 | ~string }

func confirmedComfyCleanupIDs[T comfyCleanupID](result updates.ComfyAssetDeleteResult, idsByKey map[string][]T, allIDs []T, fileCount int) (confirmed, deferred []T) {
	confirmedSet := make(map[T]struct{}, len(allIDs))
	if len(result.Items) == 0 && result.Mismatched == 0 && result.Rejected == 0 && result.Deleted+result.Missing == fileCount {
		confirmed = append(confirmed, allIDs...)
		return confirmed, deferred
	}
	for _, outcome := range result.Items {
		if outcome.Status != "deleted" && outcome.Status != "missing" {
			continue
		}
		key := comfyAssetCleanupKey(outcome.Filename, outcome.Subfolder, outcome.StorageType, outcome.SizeBytes, outcome.SHA256)
		for _, id := range idsByKey[key] {
			confirmedSet[id] = struct{}{}
		}
	}
	for _, id := range allIDs {
		if _, ok := confirmedSet[id]; ok {
			confirmed = append(confirmed, id)
		} else {
			deferred = append(deferred, id)
		}
	}
	return confirmed, deferred
}

func comfyAssetCleanupKey(filename, subfolder, storageType string, sizeBytes int64, contentHash string) string {
	return storageType + "\x00" + subfolder + "\x00" + filename + "\x00" + strconv.FormatInt(sizeBytes, 10) + "\x00" + contentHash
}
