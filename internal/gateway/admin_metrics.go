package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type metricsChartBucket struct {
	store.MetricsBucket
	ActiveHeight, OverdueHeight int
}

type adminMetricsView struct {
	At, From                time.Time
	Period, PeriodLabel     string
	Step                    time.Duration
	Generation              store.MetricsGeneration
	Buckets                 []metricsChartBucket
	ChartMax, Samples       int64
	MissingBuckets          int
	Outcomes                []domain.GenerationOutcomeGroup
	Failures                []domain.GenerationFailureSummary
	Latencies               []domain.ServiceLatencySummary
	OutcomesLimited         bool
	FailuresLimited         bool
	Gateway                 *domain.GatewayObservation
	GatewayState, HostState string
	Host                    *domain.HostMetric
	Dependencies            []DependencyStatus
	Workers                 []MaintenanceWorkerState
	Stats                   domain.AdminStats
	ServiceStats            []domain.ServiceUsage
	OverdueAfter            time.Duration
}

func newAdminMetricsView(now time.Time, period string) (adminMetricsView, error) {
	view := adminMetricsView{At: now.UTC(), Period: period, OverdueAfter: generationOverdueAfter, GatewayState: "Нет снимка", HostState: "Нет снимка"}
	var duration time.Duration
	switch period {
	case "1h":
		duration, view.Step, view.PeriodLabel = time.Hour, 5*time.Minute, "1 час"
	case "", "24h":
		view.Period = "24h"
		duration, view.Step, view.PeriodLabel = 24*time.Hour, 30*time.Minute, "24 часа"
	case "7d":
		duration, view.Step, view.PeriodLabel = 7*24*time.Hour, 3*time.Hour, "7 дней"
	default:
		return view, errors.New("Выберите период: 1 час, 24 часа или 7 дней.")
	}
	view.From = view.At.Add(-duration)
	return view, nil
}

func metricsSnapshotState(at, now time.Time, maxAge time.Duration) string {
	if at.IsZero() {
		return "Нет снимка"
	}
	if at.After(now.Add(5 * time.Second)) {
		return "Время не подтверждено"
	}
	if now.Sub(at) > maxAge {
		return "Устаревший снимок"
	}
	return "Свежий снимок"
}

func (view *adminMetricsView) setHistory(rows []store.MetricsBucket) {
	view.Buckets, view.Samples, view.MissingBuckets, view.ChartMax = nil, 0, 0, 0
	for _, row := range rows {
		if row.Samples > 0 {
			view.Samples += row.Samples
			view.ChartMax = max(view.ChartMax, row.ActivePeak, row.OverduePeak)
		} else {
			view.MissingBuckets++
		}
	}
	for _, row := range rows {
		item := metricsChartBucket{MetricsBucket: row}
		if row.Samples > 0 && view.ChartMax > 0 {
			item.ActiveHeight = int(float64(row.ActivePeak) / float64(view.ChartMax) * 100)
			item.OverdueHeight = int(float64(row.OverduePeak) / float64(view.ChartMax) * 100)
			if row.ActivePeak > 0 {
				item.ActiveHeight = max(1, item.ActiveHeight)
			}
			if row.OverduePeak > 0 {
				item.OverdueHeight = max(1, item.OverdueHeight)
			}
		}
		view.Buckets = append(view.Buckets, item)
	}
}

func (a *App) loadAdminMetrics(ctx context.Context, view *adminMetricsView) error {
	var err error
	if view.Generation, err = a.store.MetricsGeneration(ctx, view.From, view.At, generationOverdueAfter); err != nil {
		return err
	}
	rows, err := a.store.MetricsGatewayHistory(ctx, view.From, view.At, view.Step)
	if err != nil {
		return err
	}
	view.setHistory(rows)
	if view.Outcomes, err = a.store.GenerationOutcomeGroupsBetween(ctx, view.From, 31, view.At); err != nil {
		return err
	}
	if len(view.Outcomes) > 30 {
		view.Outcomes, view.OutcomesLimited = view.Outcomes[:30], true
	}
	if view.Failures, err = a.store.GenerationFailureSummariesBetween(ctx, view.From, 21, view.At); err != nil {
		return err
	}
	if len(view.Failures) > 20 {
		view.Failures, view.FailuresLimited = view.Failures[:20], true
	}
	if view.Latencies, err = a.store.ServiceLatencySummariesBetween(ctx, view.From, view.At); err != nil {
		return err
	}
	gateway, err := a.store.GatewayObservationSummary(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		view.Gateway = &gateway.Latest
		view.GatewayState = metricsSnapshotState(gateway.Latest.RecordedAt, view.At, 2*gatewayObservationInterval)
	}
	host, err := a.store.MetricsLatestHost(ctx, view.At)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		view.Host = &host
		view.HostState = metricsSnapshotState(host.RecordedAt, view.At, 2*time.Minute)
	}
	if view.Stats, err = a.store.AdminStats(ctx); err != nil {
		return err
	}
	view.ServiceStats = view.Stats.UsageByService
	view.Dependencies, view.Workers = a.dependencyStatuses(), a.maintenanceWorkerStates()
	return nil
}

func (a *App) handleAdminMetricsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if user := a.currentUser(r); user == nil || user.Role != "admin" {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	view, err := newAdminMetricsView(time.Now(), r.URL.Query().Get("period"))
	status, message := http.StatusOK, ""
	if err != nil {
		view, _ = newAdminMetricsView(time.Now(), "24h")
		status, message = http.StatusBadRequest, err.Error()
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err = a.loadAdminMetrics(ctx, &view); err != nil {
			status, message = http.StatusServiceUnavailable, "Не удалось загрузить метрики. Повторите запрос."
		}
	}
	a.renderStatus(w, r, status, "admin_metrics", map[string]any{"Title": "Метрики", "Metrics": view, "MetricsError": message})
}

func (view adminMetricsView) ChartDescription() string {
	return fmt.Sprintf("Пики активных заданий и заданий без смены этапа более %s. Максимум %d. Снимков %d. Интервалов без снимков %d.", formatDurationLong(view.OverdueAfter), view.ChartMax, view.Samples, view.MissingBuckets)
}
