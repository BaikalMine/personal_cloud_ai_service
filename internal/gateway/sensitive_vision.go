package gateway

import (
	"context"
	"log"
	"time"
)

const sensitiveMediaClassificationTimeout = 75 * time.Second

// queueSensitiveMediaClassification serializes image checks so background
// moderation never competes with an active generation through parallel calls.
func (a *App) queueSensitiveMediaClassification() {
	if a.store == nil || a.contentCipher == nil || a.promptAssistant == nil || !a.promptAssistant.Configured() || a.sensitiveMediaSlots == nil {
		return
	}
	select {
	case a.sensitiveMediaSlots <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-a.sensitiveMediaSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		items, err := a.store.ListPendingVisualSensitiveMedia(ctx, 8)
		if err != nil {
			log.Printf("list media awaiting visual sensitivity classification: %v", err)
			return
		}
		for _, item := range items {
			image, err := a.contentCipher.DecryptBytes(item.PayloadCipher)
			if err != nil {
				log.Printf("decrypt media %d for visual sensitivity classification: %v", item.ID, err)
				continue
			}
			imageCtx, imageCancel := context.WithTimeout(ctx, sensitiveMediaClassificationTimeout)
			sensitive, err := a.promptAssistant.ClassifyImage(imageCtx, image, item.MIMEType)
			imageCancel()
			if err != nil {
				// Leave the record pending. The next low-traffic page visit retries it.
				log.Printf("classify media %d for sensitive content: %v", item.ID, err)
				continue
			}
			if err := a.store.SetContentMediaVisualSensitive(ctx, item.ID, item.EventID, sensitive); err != nil {
				log.Printf("store visual sensitivity classification for media %d: %v", item.ID, err)
			}
		}
	}()
}
