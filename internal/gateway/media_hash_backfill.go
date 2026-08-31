package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

func (a *App) backfillContentMediaHashes(ctx context.Context) (int64, error) {
	if a.store == nil || a.contentCipher == nil {
		return 0, nil
	}
	items, err := a.store.UnhashedComfyMedia(ctx, 100)
	if err != nil {
		return 0, err
	}
	var updated int64
	var updateErrors []error
	for _, item := range items {
		payload, err := a.contentCipher.DecryptBytes(item.PayloadCipher)
		if err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("decrypt media %d: %w", item.ID, err))
			continue
		}
		digest := sha256.Sum256(payload)
		if err := a.store.SetContentMediaHash(ctx, item.ID, hex.EncodeToString(digest[:])); err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("store media %d hash: %w", item.ID, err))
			continue
		}
		updated++
	}
	return updated, errors.Join(updateErrors...)
}
