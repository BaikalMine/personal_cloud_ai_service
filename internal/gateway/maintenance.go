package gateway

import (
	"context"
	"log"
	"time"
)

const maintenanceInterval = 15 * time.Minute
const generationRefreshInterval = 30 * time.Second

func (a *App) runMaintenance(ctx context.Context) {
	backfillCtx, backfillCancel := context.WithTimeout(ctx, 2*time.Minute)
	a.backfillComfyContentMedia(backfillCtx)
	a.backfillContentMediaHashes(backfillCtx)
	backfillCancel()
	miningPauseCtx, miningPauseCancel := context.WithTimeout(ctx, 15*time.Second)
	a.refreshQuickGenerationMiningLeases(miningPauseCtx)
	miningPauseCancel()
	metricCtx, metricCancel := context.WithTimeout(ctx, 8*time.Second)
	a.captureHostMetric(metricCtx)
	metricCancel()
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	generationTicker := time.NewTicker(generationRefreshInterval)
	defer generationTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-generationTicker.C:
			refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			a.refreshTrackedGenerationStatuses(refreshCtx)
			a.refreshQuickGenerationMiningLeases(refreshCtx)
			cancel()
			metricCtx, metricCancel := context.WithTimeout(ctx, 8*time.Second)
			a.captureHostMetric(metricCtx)
			metricCancel()
		case <-ticker.C:
			backfillCtx, backfillCancel := context.WithTimeout(ctx, 2*time.Minute)
			a.backfillComfyContentMedia(backfillCtx)
			a.backfillContentMediaHashes(backfillCtx)
			backfillCancel()
			cleanupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			deletedMedia, mediaErr := a.deleteExpiredComfyMedia(cleanupCtx)
			if mediaErr != nil {
				log.Printf("delete expired ComfyUI media: %v", mediaErr)
			} else if deletedMedia > 0 {
				log.Printf("deleted %d expired ComfyUI media items", deletedMedia)
			}
			deletedOtherMedia, otherMediaErr := a.store.DeleteExpiredNonComfyMedia(cleanupCtx)
			if otherMediaErr != nil {
				log.Printf("delete expired non-ComfyUI media: %v", otherMediaErr)
			} else if deletedOtherMedia > 0 {
				log.Printf("deleted %d expired non-ComfyUI media items", deletedOtherMedia)
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
			if err != nil {
				cancel()
				log.Printf("delete expired content: %v", err)
				continue
			}
			if deletedContent > 0 {
				log.Printf("deleted %d expired content events", deletedContent)
			}
			if _, err := a.store.DeleteHostMetricsBefore(cleanupCtx, time.Now().Add(-hostMetricRetention)); err != nil {
				log.Printf("delete expired host metrics: %v", err)
			}
			cancel()
		}
	}
}
