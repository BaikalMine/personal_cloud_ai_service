package gateway

import (
	"context"
	"fmt"
	"log"

	"ai-access-gateway/internal/updates"
)

const expiredComfyMediaBatch = 50

// deleteExpiredComfyMedia removes the encrypted database copy only after the
// update agent has examined the matching Windows output file. The agent itself
// deletes a file only when its name, location, size, and SHA-256 all match.
func (a *App) deleteExpiredComfyMedia(ctx context.Context) (int64, error) {
	items, err := a.store.ExpiredComfyMedia(ctx, expiredComfyMediaBatch)
	if err != nil || len(items) == 0 {
		return 0, err
	}

	ids := make([]int64, 0, len(items))
	files := make([]updates.ComfyOutputFile, 0, len(items))
	for _, item := range items {
		if !item.HasOwnership || item.StorageType != "output" || len(item.ContentHash) != 64 {
			// Older records without an exact archive hash cannot authorize an
			// output deletion, but their expired database payload can be removed.
			ids = append(ids, item.ID)
			continue
		}
		files = append(files, updates.ComfyOutputFile{
			Filename: item.Filename, Subfolder: item.Subfolder, StorageType: item.StorageType,
			SizeBytes: item.SizeBytes, SHA256: item.ContentHash,
		})
		ids = append(ids, item.ID)
	}

	if len(files) > 0 {
		result, err := a.updates.DeleteComfyOutputs(ctx, files)
		if err != nil {
			return 0, fmt.Errorf("request matched ComfyUI output cleanup: %w", err)
		}
		if result.Rejected > 0 {
			return 0, fmt.Errorf("agent rejected %d ComfyUI output cleanup records", result.Rejected)
		}
		if result.Deleted > 0 || result.Missing > 0 || result.Mismatched > 0 {
			log.Printf("processed expired ComfyUI output records: deleted=%d missing=%d mismatched=%d", result.Deleted, result.Missing, result.Mismatched)
		}
	}
	return a.store.DeleteContentMediaByIDs(ctx, ids)
}
