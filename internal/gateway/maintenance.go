package gateway

import (
	"context"
	"log"
	"time"
)

const maintenanceInterval = 15 * time.Minute
const generationRefreshInterval = 30 * time.Second
const comfyMemoryMonitorInterval = 10 * time.Second

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
	virusTotalCtx, virusTotalCancel := context.WithTimeout(ctx, 2*time.Minute)
	a.refreshFeatureSuggestionScans(virusTotalCtx)
	virusTotalCancel()
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	generationTicker := time.NewTicker(generationRefreshInterval)
	defer generationTicker.Stop()
	comfyMemoryTicker := time.NewTicker(comfyMemoryMonitorInterval)
	defer comfyMemoryTicker.Stop()
	websocketTicker := time.NewTicker(websocketAuthorizationRefreshInterval)
	defer websocketTicker.Stop()
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
		case <-comfyMemoryTicker.C:
			memoryCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			a.observeComfyQueueForMemoryRelease(memoryCtx)
			cancel()
		case <-websocketTicker.C:
			websocketCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			a.pruneUnauthorizedWebSockets(websocketCtx)
			cancel()
		case <-ticker.C:
			virusTotalCtx, virusTotalCancel := context.WithTimeout(ctx, 2*time.Minute)
			a.refreshFeatureSuggestionScans(virusTotalCtx)
			virusTotalCancel()
			backfillCtx, backfillCancel := context.WithTimeout(ctx, 2*time.Minute)
			a.backfillComfyContentMedia(backfillCtx)
			a.backfillContentMediaHashes(backfillCtx)
			backfillCancel()
			inputCleanupCtx, inputCleanupCancel := context.WithTimeout(ctx, 3*time.Minute)
			deletedInputs, inputErr := a.deleteExpiredComfyInputs(inputCleanupCtx)
			inputCleanupCancel()
			if inputErr != nil {
				log.Printf("delete expired ComfyUI inputs: %v", inputErr)
			} else if deletedInputs > 0 {
				log.Printf("deleted %d expired ComfyUI input records", deletedInputs)
			}
			mediaCleanupCtx, mediaCleanupCancel := context.WithTimeout(ctx, 3*time.Minute)
			deletedMedia, mediaErr := a.deleteExpiredComfyMedia(mediaCleanupCtx)
			mediaCleanupCancel()
			if mediaErr != nil {
				log.Printf("delete expired ComfyUI media: %v", mediaErr)
			} else if deletedMedia > 0 {
				log.Printf("deleted %d expired ComfyUI media items", deletedMedia)
			}
			cleanupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
			deletedTemporaryUsers, err := a.store.DeleteExpiredTemporaryUsers(cleanupCtx)
			if err != nil {
				cancel()
				log.Printf("delete expired temporary users: %v", err)
				continue
			}
			if deletedTemporaryUsers > 0 {
				log.Printf("deleted %d expired temporary users", deletedTemporaryUsers)
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
			deletedVariants, variantErr := a.store.DeleteExpiredGenerationVariants(cleanupCtx, time.Now().Add(-24*time.Hour))
			if variantErr != nil {
				log.Printf("delete expired generation variants: %v", variantErr)
			} else if deletedVariants > 0 {
				log.Printf("deleted %d expired generation history items", deletedVariants)
			}
			if _, err := a.store.DeleteHostMetricsBefore(cleanupCtx, time.Now().Add(-hostMetricRetention)); err != nil {
				log.Printf("delete expired host metrics: %v", err)
			}
			cancel()
		}
	}
}
