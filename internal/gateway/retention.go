package gateway

import (
	"context"
	"fmt"
	"time"

	"ai-access-gateway/internal/config"
	"ai-access-gateway/internal/domain"
)

type retentionPolicyView struct {
	GenerationHistoryLabel string
	GenerationMediaLabel   string
	AIContentLabel         string
	ComfyInputsLabel       string
	HostMetricsLabel       string
	AuditLogLabel          string
}

func (a *App) retentionPolicy() config.RetentionPolicy {
	return a.cfg.Retention.WithDefaults()
}

func newRetentionPolicyView(policy config.RetentionPolicy) retentionPolicyView {
	policy = policy.WithDefaults()
	return retentionPolicyView{
		GenerationHistoryLabel: retentionHourLabel(policy.GenerationHistory),
		GenerationMediaLabel:   retentionHourLabel(policy.GenerationMedia),
		AIContentLabel:         retentionDurationLabel(policy.AIContent),
		ComfyInputsLabel:       retentionDurationLabel(policy.ComfyInputs),
		HostMetricsLabel:       retentionDurationLabel(policy.HostMetrics),
		AuditLogLabel:          retentionDurationLabel(policy.AuditLog),
	}
}

func retentionDurationLabel(value time.Duration) string {
	return accountLifetimeLabel(int64(value.Seconds()))
}

func retentionHourLabel(value time.Duration) string {
	hours := int64(value / time.Hour)
	if hours > 0 && value == time.Duration(hours)*time.Hour {
		return fmt.Sprintf("%d %s", hours, russianHourLabel(hours))
	}
	return retentionDurationLabel(value)
}

func (a *App) insertComfyOutputOwnerships(ctx context.Context, userID int64, outputs []domain.ComfyOutputOwnership) error {
	expiresAt := time.Now().Add(a.retentionPolicy().GenerationMedia)
	for index := range outputs {
		outputs[index].ExpiresAt = expiresAt
	}
	return a.store.InsertComfyOutputOwnerships(ctx, userID, outputs)
}
