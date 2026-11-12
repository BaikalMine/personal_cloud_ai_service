package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func registerMetricsPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, now time.Time, source map[string]any, services []domain.ServiceUsage) {
	now = now.UTC()
	page := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method", 405)
			return
		}
		period, scenario := r.URL.Query().Get("period"), r.URL.Query().Get("scenario")
		duration, step, label := 24*time.Hour, 30*time.Minute, "24 часа"
		message := ""
		switch period {
		case "", "24h":
			period = "24h"
		case "1h":
			duration, step, label = time.Hour, 5*time.Minute, "1 час"
		case "7d":
			duration, step, label = 7*24*time.Hour, 3*time.Hour, "7 дней"
		default:
			period, message = "24h", "Выберите период: 1 час, 24 часа или 7 дней."
		}
		from := now.Add(-duration)
		generation := store.MetricsGeneration{GenerationObservabilitySummary: source["Generation"].(domain.GenerationObservabilitySummary), QueueSamples: 22, ExecutionSamples: 21}
		gateway := source["Gateway"].(domain.GatewayObservationSummary).Latest
		host := *source["Host"].(*domain.HostMetric)
		host.RecordedAt = now.Add(-20 * time.Second)
		outcomes := append([]domain.GenerationOutcomeGroup(nil), source["Outcomes"].([]domain.GenerationOutcomeGroup)...)
		failures := append([]domain.GenerationFailureSummary(nil), source["Failures"].([]domain.GenerationFailureSummary)...)
		latencies := append([]domain.ServiceLatencySummary(nil), source["Latencies"].([]domain.ServiceLatencySummary)...)
		if period == "1h" {
			generation.Completed, generation.Failed, generation.Cancelled = 2, 1, 0
			generation.SuccessRate = 67
			failures = failures[:1]
		}
		if scenario == "long" {
			outcomes[0].WorkflowID = strings.Repeat("workflow-0123456789", 8)
			outcomes[0].ModelName = strings.Repeat("ОченьДлиннаяМодель", 10)
			failures[0].ErrorCode = strings.Repeat("ERROR_CODE_", 18)
			failures[0].ErrorMessage = strings.Repeat("<script>never()</script> Нужна проверка зависимостей. ", 30)
			latencies[0].LastDetail = strings.Repeat("diagnostic-without-spaces", 30)
		}
		if scenario == "cancelled" {
			generation = store.MetricsGeneration{GenerationObservabilitySummary: domain.GenerationObservabilitySummary{Cancelled: 3, Expired: 2}}
			outcomes = []domain.GenerationOutcomeGroup{{WorkflowID: "cancelled-only", ModelName: "Flux2", Total: 5, Cancelled: 5}}
			failures = nil
		}
		buckets := []map[string]any{}
		var samples int64
		missing := 0
		for i := 0; i < int(duration/step); i++ {
			start := from.Add(time.Duration(i) * step)
			active, overdue, n := (i*7+3)%10, 0, int64(step/(30*time.Second))
			if active > 6 {
				overdue = 1
			}
			if i == 5 || i == 6 {
				n = 0
			}
			if scenario == "empty" {
				n = 0
			}
			if scenario == "zero" {
				active, overdue, n = 0, 0, 1
			}
			if n == 0 {
				missing++
			}
			samples += n
			buckets = append(buckets, map[string]any{"From": start, "Until": start.Add(step), "Samples": n, "ActivePeak": active, "OverduePeak": overdue, "ActiveHeight": active * 10, "OverdueHeight": overdue * 10, "FirstAt": start, "LastAt": start.Add(step - 30*time.Second)})
		}
		view := map[string]any{"At": now, "From": from, "Period": period, "PeriodLabel": label, "Step": step, "Generation": generation, "Buckets": buckets, "Samples": samples, "MissingBuckets": missing, "ChartMax": 10, "ChartDescription": fmt.Sprintf("Пики активных заданий. Снимков %d. Интервалов без снимков %d.", samples, missing), "Gateway": &gateway, "Host": &host, "GatewayState": "Свежий снимок", "HostState": "Свежий снимок", "Outcomes": outcomes, "Failures": failures, "Latencies": latencies, "OutcomesLimited": false, "FailuresLimited": false, "OverdueAfter": 45 * time.Minute, "Stats": domain.AdminStats{RequestsToday: 147, Requests7Days: 824, ActiveWebSockets: 2, ErrorRate: "0,8%"}, "ServiceStats": services, "Dependencies": source["Dependencies"], "Workers": source["WorkerIssues"]}
		if scenario == "empty" {
			view["Generation"], view["Outcomes"], view["Failures"], view["Latencies"] = store.MetricsGeneration{}, []domain.GenerationOutcomeGroup{}, []domain.GenerationFailureSummary{}, []domain.ServiceLatencySummary{}
			view["Gateway"], view["Host"], view["GatewayState"], view["HostState"] = nil, nil, "Нет снимка", "Нет снимка"
			view["Stats"], view["ServiceStats"], view["Dependencies"], view["Workers"] = domain.AdminStats{}, nil, nil, nil
		}
		if scenario == "zero" {
			view["ChartMax"] = 0
		}
		if scenario == "stale" {
			gateway.RecordedAt, host.RecordedAt, host.GPUAvailable = now.Add(-time.Hour), now.Add(-time.Hour), false
			view["GatewayState"], view["HostState"] = "Устаревший снимок", "Устаревший снимок"
		}
		if scenario == "limited" {
			view["OutcomesLimited"], view["FailuresLimited"] = true, true
		}
		if scenario == "error" {
			message = "Не удалось загрузить метрики. Повторите запрос."
		}
		render("admin_metrics", "Метрики", map[string]any{"Metrics": view, "MetricsError": message})(w, r)
	}
	mux.HandleFunc("/preview/metrics", page)
	mux.HandleFunc("/admin/metrics", page)
}
