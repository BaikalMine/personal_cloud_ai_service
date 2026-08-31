package gateway

import (
	"context"
	"errors"
	"log"
	"time"

	"ai-access-gateway/internal/domain"
)

const databaseRetentionTimeout = 2 * time.Minute

func (a *App) runDatabaseRetentionCleanup(ctx context.Context) (domain.DatabaseCleanupReport, error) {
	now := time.Now().UTC()
	policy := a.retentionPolicy()
	report, cleanupErr := a.store.CleanupDatabaseRetention(ctx, domain.DatabaseRetentionCutoffs{
		ProxyRequests:      now.Add(-policy.ProxyRequests),
		WebSocketSessions:  now.Add(-policy.WebSocketSessions),
		GenerationRequests: now.Add(-policy.GenerationRequests),
		GenerationJobs:     now.Add(-policy.GenerationRequests),
		DailyUsage:         now.Add(-policy.DailyUsage),
		InviteHistory:      now.Add(-policy.InviteHistory),
		AuditLog:           now.Add(-policy.AuditLog),
		HostMetrics:        now.Add(-policy.HostMetrics),
		GenerationVariants: now.Add(-policy.GenerationHistory),
		OutputOwnerships:   now,
	}, a.cfg.DatabaseCleanupBatchSize, a.cfg.DatabaseCleanupMaxBatches)

	// Preserve the cleanup result even when the cleanup itself consumed its
	// deadline. This write remains bounded and is awaited by the worker.
	stateCtx, stateCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	stateErr := a.store.SaveDatabaseCleanupState(stateCtx, report)
	stateCancel()
	if cleanupErr != nil {
		log.Printf("database retention cleanup completed with errors: %v", cleanupErr)
	} else if deleted := report.TotalDeleted(); deleted > 0 {
		log.Printf("database retention cleanup deleted %d rows", deleted)
	}
	if stateErr != nil {
		log.Printf("save database retention cleanup state: %v", stateErr)
	}
	return report, errors.Join(cleanupErr, stateErr)
}
