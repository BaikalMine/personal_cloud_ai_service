package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

type adminObservabilityView struct {
	Generation     domain.GenerationObservabilitySummary
	Gateway        domain.GatewayObservationSummary
	Outcomes       []domain.GenerationOutcomeGroup
	Failures       []domain.GenerationFailureSummary
	Latencies      []domain.ServiceLatencySummary
	Leases         []domain.QuickGenerationMiningLease
	Dependencies   []DependencyStatus
	Workers        []MaintenanceWorkerState
	WorkerIssues   []MaintenanceWorkerState
	HealthyWorkers int
	Host           *HostMetric
	GeneratedAt    time.Time
	OverdueAfter   time.Duration
}

func (a *App) loadAdminObservability(ctx context.Context) (view adminObservabilityView, err error) {
	started := time.Now()
	defer func() {
		a.observeServiceCall(ctx, "database", "observability_dashboard", started, err, false, "observability_query_failed", "")
	}()
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour)
	view.GeneratedAt = now
	view.OverdueAfter = generationOverdueAfter
	if view.Generation, err = a.store.GenerationObservabilitySummary(ctx, since, now.Add(-generationOverdueAfter)); err != nil {
		return view, err
	}
	if view.Outcomes, err = a.store.GenerationOutcomeGroups(ctx, since, 30); err != nil {
		return view, err
	}
	if view.Failures, err = a.store.GenerationFailureSummaries(ctx, since, 20); err != nil {
		return view, err
	}
	view.Gateway, err = a.store.GatewayObservationSummary(ctx)
	if errors.Is(err, sql.ErrNoRows) || err == nil && now.Sub(view.Gateway.Latest.RecordedAt) > 2*gatewayObservationInterval {
		if _, captureErr := a.captureGatewayObservation(ctx); captureErr != nil {
			return view, captureErr
		}
		view.Gateway, err = a.store.GatewayObservationSummary(ctx)
	}
	if err != nil {
		return view, err
	}
	if view.Latencies, err = a.store.ServiceLatencySummaries(ctx, since); err != nil {
		return view, err
	}
	if view.Leases, err = a.store.ListQuickGenerationMiningLeases(ctx); err != nil {
		return view, err
	}
	metrics, metricsErr := a.store.HostMetrics(ctx, now.Add(-15*time.Minute))
	if metricsErr != nil {
		return view, metricsErr
	}
	if len(metrics) > 0 {
		latest := metrics[len(metrics)-1]
		view.Host = &latest
	}
	view.Dependencies = a.dependencyStatuses()
	view.Workers = a.maintenanceWorkerStates()
	for _, worker := range view.Workers {
		if worker.Running || worker.ConsecutiveFailures > 0 || worker.Status == "stopped" {
			view.WorkerIssues = append(view.WorkerIssues, worker)
			continue
		}
		if worker.Status == "healthy" {
			view.HealthyWorkers++
		}
	}
	return view, nil
}

func (a *App) handleAdminGenerationJobTrace(w http.ResponseWriter, r *http.Request, rawPublicID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	publicID := strings.TrimSpace(strings.Trim(rawPublicID, "/"))
	if !validGenerationRequestID(publicID) {
		http.NotFound(w, r)
		return
	}
	started := time.Now()
	trace, err := a.store.AdminGenerationJobTrace(r.Context(), publicID)
	a.observeServiceCall(r.Context(), "database", "generation_trace", started, err, false, "generation_trace_query_failed", "")
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "не удалось загрузить трассу задания", http.StatusInternalServerError)
		return
	}
	traceContext := generationJobTraceContext(r.Context(), trace.Job)
	logGateway(traceContext, 0, "generation_trace_opened", "Administrator opened generation trace", "job_public_id", trace.Job.PublicID)
	a.render(w, r.WithContext(traceContext), "admin_job_trace", map[string]any{
		"Title": "Трасса генерации",
		"Trace": trace,
	})
}

func generationJobStateLabel(state domain.GenerationJobState) string {
	switch state {
	case domain.GenerationJobDraft:
		return "Создано"
	case domain.GenerationJobPreparing:
		return "Подготовка"
	case domain.GenerationJobUploading:
		return "Загрузка"
	case domain.GenerationJobWaitingForResources:
		return "Ожидание ресурсов"
	case domain.GenerationJobQueued:
		return "В очереди"
	case domain.GenerationJobRunning:
		return "Выполняется"
	case domain.GenerationJobPostprocessing:
		return "Обработка результата"
	case domain.GenerationJobArchiving:
		return "Архивация"
	case domain.GenerationJobCompleted:
		return "Готово"
	case domain.GenerationJobFailed:
		return "Ошибка"
	case domain.GenerationJobCancelled:
		return "Отменено"
	case domain.GenerationJobExpired:
		return "Истекло"
	default:
		return string(state)
	}
}

func serviceObservationOutcomeLabel(outcome string) string {
	switch outcome {
	case "ok":
		return "Успешно"
	case "degraded":
		return "С ухудшением"
	case "timeout":
		return "Тайм-аут"
	case "misconfigured":
		return "Не настроено"
	default:
		return "Ошибка"
	}
}

func observabilityComponentLabel(component string) string {
	switch component {
	case dependencyComfyUI:
		return "ComfyUI"
	case dependencyOllama:
		return "Ollama"
	case dependencyModerator:
		return "Модератор"
	case dependencyOpenWebUI:
		return "OpenWebUI"
	case dependencyMiningAgent:
		return "Майнинг-агент"
	case dependencySystemMonitor:
		return "Windows-агент"
	case "database":
		return "PostgreSQL"
	default:
		return component
	}
}

func observabilityOperationLabel(operation string) string {
	labels := map[string]string{
		"probe":                   "Проверка доступности",
		"health":                  "Проверка со страницы",
		"readiness":               "Readiness",
		"submit_prompt":           "Отправка workflow",
		"generation_status":       "Статус генерации",
		"queue":                   "Очередь",
		"enhance_image":           "Ассистент изображения",
		"enhance_video":           "Ассистент видео",
		"classify_image":          "Анализ изображения",
		"gateway_snapshot":        "Снимок Gateway",
		"observability_dashboard": "Экран наблюдаемости",
		"generation_trace":        "Трасса задания",
	}
	if label := labels[operation]; label != "" {
		return label
	}
	return operation
}

func formatSignedBytes(value int64) string {
	if value == 0 {
		return "без изменений"
	}
	prefix := "+"
	if value < 0 {
		prefix = "−"
		value = -value
	}
	return prefix + formatBytes(value)
}

func formatDurationLong(value time.Duration) string {
	if value <= 0 {
		return "-"
	}
	if value >= time.Hour {
		return fmt.Sprintf("%d ч. %d мин.", int(value/time.Hour), int(value%time.Hour/time.Minute))
	}
	if value >= time.Minute {
		return fmt.Sprintf("%d мин.", int(value/time.Minute))
	}
	return fmt.Sprintf("%d сек.", int(value/time.Second))
}

func formatAgeSeconds(value int64) string {
	if value <= 0 {
		return "только что"
	}
	return formatDurationLong(time.Duration(value) * time.Second)
}
