package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
)

func (a *App) backfillContentMediaHashes(ctx context.Context) {
	if a.store == nil || a.contentCipher == nil {
		return
	}
	items, err := a.store.UnhashedComfyMedia(ctx, 100)
	if err != nil {
		log.Printf("find unhashed ComfyUI media: %v", err)
		return
	}
	for _, item := range items {
		payload, err := a.contentCipher.DecryptBytes(item.PayloadCipher)
		if err != nil {
			log.Printf("decrypt ComfyUI media %d for hash backfill: %v", item.ID, err)
			continue
		}
		digest := sha256.Sum256(payload)
		if err := a.store.SetContentMediaHash(ctx, item.ID, hex.EncodeToString(digest[:])); err != nil {
			log.Printf("store ComfyUI media %d hash: %v", item.ID, err)
		}
	}
}
