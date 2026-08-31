package gateway

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"ai-access-gateway/internal/mining"
)

func (a *App) captureHostMetric(ctx context.Context) (int64, error) {
	if a.systemMonitor == nil {
		a.dependencyMonitor().failure(dependencySystemMonitor, "Windows-агент мониторинга не настроен.", true, 0)
		return 0, errors.New("windows-агент мониторинга не настроен")
	}
	started := time.Now()
	metrics, err := a.systemMonitor.System(ctx)
	if err != nil {
		a.dependencyMonitor().failure(
			dependencySystemMonitor,
			dependencyCallError(metrics.Message, err),
			errors.Is(err, mining.ErrUnavailable) && (a.systemMonitor == nil || !a.systemMonitor.Configured()),
			time.Since(started),
		)
		return 0, err
	}
	metric := hostMetricFromSystem(metrics)
	a.dependencyMonitor().success(dependencySystemMonitor, "Показатели Windows получены.", &metric.RecordedAt, time.Since(started))
	if metric.MemoryTotalBytes <= 0 {
		return 0, nil
	}
	if err := a.store.RecordHostMetric(ctx, metric); err != nil {
		log.Printf("record host metric: %v", err)
		return 0, err
	}
	return 1, nil
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
	} else if a.systemMonitor != nil && a.systemMonitor.Configured() {
		started := time.Now()
		metrics, metricErr := a.systemMonitor.System(ctx)
		if metricErr != nil {
			a.dependencyMonitor().failure(dependencySystemMonitor, dependencyCallError(metrics.Message, metricErr), false, time.Since(started))
		} else {
			metric := hostMetricFromSystem(metrics)
			a.dependencyMonitor().success(dependencySystemMonitor, "Показатели Windows получены.", &metric.RecordedAt, time.Since(started))
			if metric.MemoryTotalBytes > 0 {
				overview.Host = &metric
				overview.History = []HostMetric{metric}
				if err := a.store.RecordHostMetric(ctx, metric); err != nil {
					log.Printf("record initial host metric: %v", err)
				}
			}
		}
	}
	if overview.Host != nil {
		a.dependencyMonitor().observeData(dependencySystemMonitor, overview.Host.RecordedAt)
	}
	overview.Agent = a.dependencyStatus(dependencySystemMonitor)
	overview.AgentAvailable = overview.Agent.State == DependencyOnline
	overview.AgentMessage = overview.Agent.Detail
	overview.Dependencies = a.dependencyStatuses()
	overview.Workers = a.maintenanceWorkerStates()
	return overview, nil
}

func hostMetricFromSystem(metrics mining.SystemMetrics) HostMetric {
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
	return metric
}

func dependencyCallError(message string, err error) string {
	if message = strings.TrimSpace(message); message != "" {
		return message
	}
	if err != nil {
		return err.Error()
	}
	return "Проверка завершилась с ошибкой."
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
