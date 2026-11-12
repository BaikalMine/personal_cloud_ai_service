package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

func (a *App) cleanupComfyStorageRetention(ctx context.Context) (int64, error) {
	if a.cfg.RestoreDrillMode || !a.cfg.ComfyStorageCleanupEnabled {
		return 0, nil
	}
	if err := a.comfyStorageMutationAllowed(); err != nil {
		return 0, err
	}
	check, cancel := context.WithTimeout(ctx, 30*time.Second)
	status, err := a.updates.ComfyStorageStatus(check)
	cancel()
	if err != nil {
		return 0, err
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 0, err
	}
	var op store.ComfyStorageOperation
	var claimed bool
	err = a.store.WithGPUAdmissionGuard(ctx, func() error {
		var err error
		op, claimed, err = a.store.ClaimComfyStorageRetention(ctx, hex.EncodeToString(random[:]), a.updates.ComfyStorageHostKey(), status)
		return err
	})
	if errors.Is(err, store.ErrComfyStorageBusy) || errors.Is(err, store.ErrComfyStorageConflict) || errors.Is(err, store.ErrGPUWorkConflict) || errors.Is(err, updates.ErrComfyStorageBusy) {
		return 0, nil
	}
	if err != nil || !claimed {
		return 0, err
	}
	op, err = a.executeClaimedComfyStorageOperation(ctx, op)
	if err != nil {
		return 0, err
	}
	if op.Active() {
		return 0, fmt.Errorf("ComfyUI retention operation %s awaits confirmation: %s", op.ID, op.LastCode)
	}
	var completed int64
	if op.Result != nil {
		for _, item := range op.Result.Items {
			if item.Outcome == "deleted" || item.Outcome == "missing" {
				completed++
			}
		}
	}
	return completed, nil
}
