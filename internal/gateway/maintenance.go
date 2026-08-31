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
	dependencyCtx, dependencyCancel := context.WithTimeout(ctx, 4*time.Second)
	a.refreshDependencyStatuses(dependencyCtx)
	dependencyCancel()
	virusTotalCtx, virusTotalCancel := context.WithTimeout(ctx, 2*time.Minute)
	a.refreshFeatureSuggestionScans(virusTotalCtx)
	virusTotalCancel()
	retentionCtx, retentionCancel := context.WithTimeout(ctx, databaseRetentionTimeout)
	a.runDatabaseRetentionCleanup(retentionCtx)
	retentionCancel()
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	generationTicker := time.NewTicker(generationRefreshInterval)
	defer generationTicker.Stop()
	comfyMemoryTicker := time.NewTicker(comfyMemoryMonitorInterval)
	defer comfyMemoryTicker.Stop()
	dependencyInterval := a.cfg.DependencyCheckInterval
	if dependencyInterval <= 0 {
		dependencyInterval = defaultDependencyCheckInterval
	}
	dependencyTicker := time.NewTicker(dependencyInterval)
	defer dependencyTicker.Stop()
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
		case <-dependencyTicker.C:
			dependencyCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			a.refreshDependencyStatuses(dependencyCtx)
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
			otherMediaCleanupCtx, otherMediaCleanupCancel := context.WithTimeout(ctx, 15*time.Second)
			deletedOtherMedia, otherMediaErr := a.store.DeleteExpiredNonComfyMedia(otherMediaCleanupCtx)
			otherMediaCleanupCancel()
			if otherMediaErr != nil {
				log.Printf("delete expired non-ComfyUI media: %v", otherMediaErr)
			} else if deletedOtherMedia > 0 {
				log.Printf("deleted %d expired non-ComfyUI media items", deletedOtherMedia)
			}
			retentionCtx, retentionCancel := context.WithTimeout(ctx, databaseRetentionTimeout)
			a.runDatabaseRetentionCleanup(retentionCtx)
			retentionCancel()
			cleanupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			deleted, err := a.store.DeleteExpiredSessions(cleanupCtx, a.cfg.SessionIdleTimeout)
			if err != nil {
				log.Printf("delete expired sessions: %v", err)
			} else if deleted > 0 {
				log.Printf("deleted %d expired sessions", deleted)
			}
			deletedTemporaryUsers, err := a.store.DeleteExpiredTemporaryUsers(cleanupCtx)
			if err != nil {
				log.Printf("delete expired temporary users: %v", err)
			} else if deletedTemporaryUsers > 0 {
				log.Printf("deleted %d expired temporary users", deletedTemporaryUsers)
			}
			deletedContent, err := a.store.DeleteExpiredContent(cleanupCtx)
			if err != nil {
				log.Printf("delete expired content: %v", err)
			} else if deletedContent > 0 {
				log.Printf("deleted %d expired content events", deletedContent)
			}
			cancel()
		}
	}
}
