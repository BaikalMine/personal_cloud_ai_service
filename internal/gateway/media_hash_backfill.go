package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"ai-access-gateway/internal/domain"
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
		payload, err := a.materializeContentMedia(ctx, domain.ContentMediaRow{
			ID: item.ID, PayloadCipher: item.PayloadCipher,
			SizeBytes: item.SizeBytes, StorageFormat: item.StorageFormat,
		})
		if err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("materialize media %d: %w", item.ID, err))
			continue
		}
		digest := sha256.New()
		_, hashErr := io.Copy(digest, payload)
		closeErr := payload.Close()
		if hashErr != nil || closeErr != nil {
			updateErrors = append(updateErrors, fmt.Errorf("hash media %d: %w", item.ID, errors.Join(hashErr, closeErr)))
			continue
		}
		if err := a.store.SetContentMediaHash(ctx, item.ID, hex.EncodeToString(digest.Sum(nil))); err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("store media %d hash: %w", item.ID, err))
			continue
		}
		updated++
	}
	return updated, errors.Join(updateErrors...)
}
