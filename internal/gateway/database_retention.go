package gateway

import (
	"context"
	"log"
	"time"

	"ai-access-gateway/internal/domain"
)

const databaseRetentionTimeout = 2 * time.Minute

func (a *App) runDatabaseRetentionCleanup(ctx context.Context) domain.DatabaseCleanupReport {
	now := time.Now().UTC()
	policy := a.retentionPolicy()
	report, cleanupErr := a.store.CleanupDatabaseRetention(ctx, domain.DatabaseRetentionCutoffs{
		ProxyRequests:      now.Add(-policy.ProxyRequests),
		WebSocketSessions:  now.Add(-policy.WebSocketSessions),
		GenerationRequests: now.Add(-policy.GenerationRequests),
		DailyUsage:         now.Add(-policy.DailyUsage),
		InviteHistory:      now.Add(-policy.InviteHistory),
		AuditLog:           now.Add(-policy.AuditLog),
		HostMetrics:        now.Add(-policy.HostMetrics),
		GenerationVariants: now.Add(-policy.GenerationHistory),
		OutputOwnerships:   now,
	}, a.cfg.DatabaseCleanupBatchSize, a.cfg.DatabaseCleanupMaxBatches)

	stateCtx, stateCancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	return report
}
