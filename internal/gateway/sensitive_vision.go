package gateway

import (
	"context"
	"errors"
	"log"
	"time"

	"ai-access-gateway/internal/moderation"
)

const sensitiveMediaClassificationTimeout = 75 * time.Second

// queueSensitiveMediaClassification serializes image checks so background
// moderation never competes with an active generation through parallel calls.
func (a *App) queueSensitiveMediaClassification() {
	if a.store == nil || a.contentCipher == nil || a.contentModerator == nil || !a.contentModerator.Configured() || a.sensitiveMediaSlots == nil {
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
		for {
			items, err := a.store.ListPendingVisualSensitiveMedia(ctx, 8)
			if err != nil {
				log.Printf("list media awaiting visual sensitivity classification: %v", err)
				return
			}
			if len(items) == 0 {
				return
			}
			for _, item := range items {
				jobID := int64(0)
				if item.GenerationJobID != nil {
					jobID = *item.GenerationJobID
				}
				itemCtx := traceContext(ctx, item.CorrelationID, jobID, "")
				image, err := a.contentCipher.DecryptBytes(item.PayloadCipher)
				if err != nil {
					log.Printf("decrypt media %d for visual sensitivity classification: %v", item.ID, err)
					continue
				}
				imageCtx, imageCancel := context.WithTimeout(itemCtx, sensitiveMediaClassificationTimeout)
				started := time.Now()
				sensitive, err := a.contentModerator.ClassifyImage(imageCtx, image, item.MIMEType)
				imageCancel()
				a.observeServiceCall(itemCtx, dependencyModerator, "classify_image", started, err, false, "moderation_failed", "")
				if err != nil {
					if errors.Is(err, moderation.ErrUnsupportedImage) {
						// Large or unsupported files stay behind the privacy curtain.
						// Marking them avoids one bad file blocking the entire queue.
						if storeErr := a.store.SetContentMediaVisualSensitive(ctx, item.ID, item.EventID, true); storeErr != nil {
							log.Printf("store fallback sensitivity classification for media %d: %v", item.ID, storeErr)
							return
						}
						continue
					}
					// Leave the record pending. The next low-traffic page visit retries it.
					log.Printf("classify media %d for sensitive content: %v", item.ID, err)
					return
				}
				if err := a.store.SetContentMediaVisualSensitive(ctx, item.ID, item.EventID, sensitive); err != nil {
					log.Printf("store visual sensitivity classification for media %d: %v", item.ID, err)
					return
				}
			}
		}
	}()
}
