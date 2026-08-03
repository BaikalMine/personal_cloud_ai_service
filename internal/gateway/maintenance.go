package gateway

import (
	"context"
	"log"
	"time"
)

const maintenanceInterval = time.Hour

func (a *App) runMaintenance(ctx context.Context) {
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			deletedMedia, mediaErr := a.store.DeleteExpiredMedia(cleanupCtx)
			if mediaErr != nil {
				log.Printf("delete expired media: %v", mediaErr)
			} else if deletedMedia > 0 {
				log.Printf("deleted %d expired media items", deletedMedia)
			}
			deleted, err := a.store.DeleteExpiredSessions(cleanupCtx, a.cfg.SessionIdleTimeout)
			if err != nil {
				cancel()
				log.Printf("delete expired sessions: %v", err)
				continue
			}
			if deleted > 0 {
				log.Printf("deleted %d expired sessions", deleted)
			}
			deletedContent, err := a.store.DeleteExpiredContent(cleanupCtx)
			cancel()
			if err != nil {
				log.Printf("delete expired content: %v", err)
				continue
			}
			if deletedContent > 0 {
				log.Printf("deleted %d expired content events", deletedContent)
			}
		}
	}
}
