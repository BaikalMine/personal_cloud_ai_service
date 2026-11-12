package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/updates"
)

const expiredComfyMediaBatch = 50
const expiredComfyInputBatch = 50
const comfyCleanupRetryDelay = 6 * time.Hour

// Expired database media becomes a durable host-cleanup intent. Closing that
// intent and retaining the exact host outcome happen in one store transaction.
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
	if a.cfg.ComfyStorageCleanupEnabled {
		// The guarded retention worker owns host deletion in this mode.
		return archivedRows, nil
	}
	tombstones, err := a.store.DueComfyOutputCleanup(ctx, expiredComfyMediaBatch)
	if err != nil || len(tombstones) == 0 {
		return archivedRows, err
	}
	files := make([]updates.ComfyOutputFile, 0, len(tombstones))
	for _, item := range tombstones {
		file := updates.ComfyOutputFile{Filename: item.Filename, Subfolder: item.Subfolder, StorageType: item.StorageType, SizeBytes: item.SizeBytes, SHA256: item.ContentHash}
		files = append(files, file)
	}
	result, cleanupErr := a.updates.DeleteComfyOutputs(ctx, files)
	outcomes := comfyOutputCleanupResults(tombstones, result, cleanupErr)
	receiptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	recorded, err := a.store.RecordComfyOutputCleanup(receiptCtx, outcomes, comfyCleanupRetryDelay)
	cancel()
	if err != nil {
		return archivedRows, errors.Join(cleanupErr, err)
	}
	if result.Deleted > 0 || result.Missing > 0 || result.Mismatched > 0 || result.Rejected > 0 {
		log.Printf("processed expired ComfyUI output tombstones: deleted=%d missing=%d mismatched=%d rejected=%d", result.Deleted, result.Missing, result.Mismatched, result.Rejected)
	}
	if recorded.Deferred > 0 {
		return archivedRows + recorded.Completed, errors.Join(cleanupErr, fmt.Errorf("%d ComfyUI output cleanup records require retry", recorded.Deferred))
	}
	return archivedRows + recorded.Completed, cleanupErr
}

func comfyOutputCleanupResults(items []domain.ComfyOutputCleanupTombstone, result updates.ComfyAssetDeleteResult, requestErr error) []domain.ComfyOutputCleanupResult {
	files := make([]updates.ComfyAssetFile, 0, len(items))
	for _, item := range items {
		files = append(files, updates.ComfyAssetFile{Filename: item.Filename, Subfolder: item.Subfolder, StorageType: item.StorageType, SizeBytes: item.SizeBytes, SHA256: item.ContentHash})
	}
	statuses := comfyCleanupOutcomes(files, result, requestErr)
	outcomes := make([]domain.ComfyOutputCleanupResult, 0, len(items))
	for i, item := range items {
		outcomes = append(outcomes, domain.ComfyOutputCleanupResult{ComfyOutputCleanupTombstone: item, Outcome: statuses[i]})
	}
	return outcomes
}

func comfyCleanupOutcomes(files []updates.ComfyAssetFile, result updates.ComfyAssetDeleteResult, requestErr error) []string {
	fallback := "unconfirmed"
	if errors.Is(requestErr, updates.ErrUnavailable) {
		fallback = "host_unavailable"
	} else if requestErr == nil && len(result.Items) == 0 && result.Deleted >= 0 && result.Missing >= 0 &&
		result.Deleted <= len(files) && result.Missing <= len(files) && result.Deleted+result.Missing == len(files) && result.Mismatched == 0 && result.Rejected == 0 {
		// An aggregate does not say which file was deleted versus already absent.
		fallback = "legacy_confirmed"
	}
	statuses := make(map[string]string, len(result.Items))
	for _, outcome := range result.Items {
		key := comfyAssetCleanupKey(outcome.Filename, outcome.Subfolder, outcome.StorageType, outcome.SizeBytes, outcome.SHA256)
		status := outcome.Status
		switch status {
		case "deleted", "missing", "mismatched", "rejected":
		default:
			status = "unconfirmed"
		}
		if previous, exists := statuses[key]; exists && previous != status {
			status = "unconfirmed"
		}
		statuses[key] = status
	}
	outcomes := make([]string, 0, len(files))
	for _, item := range files {
		status := fallback
		key := comfyAssetCleanupKey(item.Filename, item.Subfolder, item.StorageType, item.SizeBytes, item.SHA256)
		if value, ok := statuses[key]; ok {
			status = value
		}
		outcomes = append(outcomes, status)
	}
	return outcomes
}

// deleteExpiredComfyInputs removes only files whose exact tracked bytes are
// confirmed by the host agent. Mismatches remain registered so they cannot
// silently escape the global disk quota.
func (a *App) deleteExpiredComfyInputs(ctx context.Context) (int64, error) {
	if a.cfg.ComfyStorageCleanupEnabled {
		return 0, nil
	}
	items, err := a.store.ExpiredComfyInputAssets(ctx, expiredComfyInputBatch)
	if err != nil || len(items) == 0 {
		return 0, err
	}
	files := make([]updates.ComfyAssetFile, 0, len(items))
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
	}
	result, cleanupErr := a.updates.DeleteComfyAssets(ctx, files)
	statuses := comfyCleanupOutcomes(files, result, cleanupErr)
	outcomes := make([]domain.ComfyInputCleanupResult, 0, len(items))
	for i, item := range items {
		outcomes = append(outcomes, domain.ComfyInputCleanupResult{ExpiredComfyInputAsset: item, ObservedSizeBytes: files[i].SizeBytes, ObservedHash: files[i].SHA256, Outcome: statuses[i]})
	}
	receiptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	recorded, err := a.store.RecordComfyInputCleanup(receiptCtx, outcomes, comfyCleanupRetryDelay)
	cancel()
	if err != nil {
		return 0, errors.Join(cleanupErr, err)
	}
	if result.Deleted > 0 || result.Missing > 0 || result.Mismatched > 0 {
		log.Printf("processed expired ComfyUI input records: deleted=%d missing=%d mismatched=%d", result.Deleted, result.Missing, result.Mismatched)
	}
	if recorded.Deferred > 0 {
		return recorded.Completed, errors.Join(cleanupErr, fmt.Errorf("%d ComfyUI input cleanup records require retry", recorded.Deferred))
	}
	return recorded.Completed, cleanupErr
}

func comfyAssetCleanupKey(filename, subfolder, storageType string, sizeBytes int64, contentHash string) string {
	return storageType + "\x00" + subfolder + "\x00" + filename + "\x00" + strconv.FormatInt(sizeBytes, 10) + "\x00" + contentHash
}
