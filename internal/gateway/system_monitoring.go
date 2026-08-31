package gateway

import (
	"context"
	"log"
	"time"
)

func (a *App) captureHostMetric(ctx context.Context) {
	metrics, err := a.systemMonitor.System(ctx)
	if err != nil {
		return
	}
	metric := HostMetric{
		RecordedAt:          metrics.CollectedAt,
		CPUPercent:          clampPercent(metrics.CPUPercent),
		MemoryUsedBytes:     metrics.MemoryUsedBytes,
		MemoryTotalBytes:    metrics.MemoryTotalBytes,
		GPUAvailable:        metrics.GPUAvailable,
		GPUName:             metrics.GPUName,
		GPUPercent:          clampPercent(metrics.GPUPercent),
		GPUMemoryUsedBytes:  metrics.GPUMemoryUsedBytes,
		GPUMemoryTotalBytes: metrics.GPUMemoryTotalBytes,
	}
	if metric.RecordedAt.IsZero() {
		metric.RecordedAt = time.Now().UTC()
	}
	if metric.MemoryTotalBytes <= 0 {
		return
	}
	if err := a.store.RecordHostMetric(ctx, metric); err != nil {
		log.Printf("record host metric: %v", err)
	}
}

func (a *App) systemOverview(ctx context.Context) (SystemOverview, error) {
	var overview SystemOverview
	var err error
	if overview.DatabaseBytes, err = a.store.DatabaseSize(ctx); err != nil {
		return SystemOverview{}, err
	}
	if overview.OnlineUsers, err = a.store.OnlineUsers(ctx, 100); err != nil {
		return SystemOverview{}, err
	}
	if overview.History, err = a.store.HostMetrics(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		return SystemOverview{}, err
	}
	if len(overview.History) > 0 {
		overview.Host = &overview.History[len(overview.History)-1]
		overview.AgentAvailable = true
		return overview, nil
	}

	metrics, metricErr := a.systemMonitor.System(ctx)
	if metricErr != nil {
		overview.AgentMessage = metrics.Message
		return overview, nil
	}
	metric := HostMetric{
		RecordedAt:          metrics.CollectedAt,
		CPUPercent:          clampPercent(metrics.CPUPercent),
		MemoryUsedBytes:     metrics.MemoryUsedBytes,
		MemoryTotalBytes:    metrics.MemoryTotalBytes,
		GPUAvailable:        metrics.GPUAvailable,
		GPUName:             metrics.GPUName,
		GPUPercent:          clampPercent(metrics.GPUPercent),
		GPUMemoryUsedBytes:  metrics.GPUMemoryUsedBytes,
		GPUMemoryTotalBytes: metrics.GPUMemoryTotalBytes,
	}
	if metric.RecordedAt.IsZero() {
		metric.RecordedAt = time.Now().UTC()
	}
	if metric.MemoryTotalBytes > 0 {
		overview.Host = &metric
		overview.History = []HostMetric{metric}
		overview.AgentAvailable = true
		if err := a.store.RecordHostMetric(ctx, metric); err != nil {
			log.Printf("record initial host metric: %v", err)
		}
	}
	return overview, nil
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
