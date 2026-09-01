package gateway

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

func (a *App) handlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	usersTotal, err := a.store.CountUsers(r.Context())
	if err != nil {
		http.Error(w, "метрики недоступны", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "gateway_requests_total %d\n", a.requestsTotal.Load())
	a.proxyMu.Lock()
	counts := make(map[string]int64, len(a.proxyCounts))
	keys := make([]string, 0, len(a.proxyCounts))
	for key, count := range a.proxyCounts {
		keys = append(keys, key)
		counts[key] = count
	}
	a.proxyMu.Unlock()
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) == 2 {
			fmt.Fprintf(w, "gateway_proxy_requests_total{service=%q,status=%q} %d\n", parts[0], parts[1], counts[key])
		}
	}
	fmt.Fprintf(w, "gateway_active_websockets %d\n", a.activeWS.Load())
	fmt.Fprintf(w, "gateway_login_failures_total %d\n", a.loginFailures.Load())
	fmt.Fprintf(w, "gateway_users_total %d\n", usersTotal)

	if summary, summaryErr := a.store.GenerationObservabilitySummary(r.Context(), time.Now().Add(-24*time.Hour), time.Now().Add(-generationOverdueAfter)); summaryErr == nil {
		fmt.Fprintf(w, "gateway_generation_jobs_active %d\n", summary.ActiveJobs)
		fmt.Fprintf(w, "gateway_generation_jobs_overdue %d\n", summary.OverdueJobs)
		fmt.Fprintf(w, "gateway_generation_jobs_completed_24h %d\n", summary.Completed)
		fmt.Fprintf(w, "gateway_generation_jobs_failed_24h %d\n", summary.Failed)
		fmt.Fprintf(w, "gateway_generation_queue_p50_ms %d\n", summary.QueueP50MS)
		fmt.Fprintf(w, "gateway_generation_queue_p95_ms %d\n", summary.QueueP95MS)
		fmt.Fprintf(w, "gateway_generation_execution_p50_ms %d\n", summary.ExecutionP50MS)
		fmt.Fprintf(w, "gateway_generation_execution_p95_ms %d\n", summary.ExecutionP95MS)
	}
	if summary, summaryErr := a.store.GatewayObservationSummary(r.Context()); summaryErr == nil {
		fmt.Fprintf(w, "gateway_database_bytes %d\n", summary.Latest.DatabaseBytes)
		fmt.Fprintf(w, "gateway_database_growth_24h_bytes %d\n", summary.DatabaseGrowth24Hours)
		fmt.Fprintf(w, "gateway_mining_leases_active %d\n", summary.Latest.ActiveLeases)
		fmt.Fprintf(w, "gateway_content_moderation_backlog %d\n", summary.Latest.ContentModerationBacklog)
		fmt.Fprintf(w, "gateway_media_moderation_backlog %d\n", summary.Latest.MediaModerationBacklog)
		fmt.Fprintf(w, "gateway_cleanup_age_seconds %d\n", summary.Latest.CleanupAgeSeconds)
	}
	for _, dependency := range a.dependencyStatuses() {
		online := 0
		if dependency.State == DependencyOnline {
			online = 1
		}
		fmt.Fprintf(w, "gateway_dependency_ready{component=%q} %d\n", dependency.Key, online)
		fmt.Fprintf(w, "gateway_dependency_last_latency_ms{component=%q} %d\n", dependency.Key, dependency.LatencyMillis)
	}
	for _, histogram := range a.serviceLatencyRegistry().snapshot() {
		for index, upperBound := range serviceLatencyBucketsMS {
			fmt.Fprintf(w, "gateway_service_latency_ms_bucket{component=%q,operation=%q,le=%q} %d\n",
				histogram.Component, histogram.Operation, fmt.Sprint(upperBound), histogram.Buckets[index])
		}
		fmt.Fprintf(w, "gateway_service_latency_ms_bucket{component=%q,operation=%q,le=%q} %d\n",
			histogram.Component, histogram.Operation, "+Inf", histogram.Count)
		fmt.Fprintf(w, "gateway_service_latency_ms_sum{component=%q,operation=%q} %d\n",
			histogram.Component, histogram.Operation, histogram.SumMS)
		fmt.Fprintf(w, "gateway_service_latency_ms_count{component=%q,operation=%q} %d\n",
			histogram.Component, histogram.Operation, histogram.Count)
	}
	for _, metric := range a.mediaOperationRegistry().snapshot() {
		for index, upperBound := range mediaDurationBucketsMS {
			fmt.Fprintf(w, "gateway_media_operation_duration_ms_bucket{stage=%q,outcome=%q,le=%q} %d\n",
				metric.Stage, metric.Outcome, fmt.Sprint(upperBound), metric.DurationBuckets[index])
		}
		fmt.Fprintf(w, "gateway_media_operation_duration_ms_bucket{stage=%q,outcome=%q,le=%q} %d\n",
			metric.Stage, metric.Outcome, "+Inf", metric.Count)
		fmt.Fprintf(w, "gateway_media_operation_duration_ms_sum{stage=%q,outcome=%q} %d\n",
			metric.Stage, metric.Outcome, metric.DurationSumMS)
		fmt.Fprintf(w, "gateway_media_operation_duration_ms_count{stage=%q,outcome=%q} %d\n",
			metric.Stage, metric.Outcome, metric.Count)
		for index, upperBound := range mediaSizeBucketsBytes {
			fmt.Fprintf(w, "gateway_media_operation_size_bytes_bucket{stage=%q,outcome=%q,le=%q} %d\n",
				metric.Stage, metric.Outcome, fmt.Sprint(upperBound), metric.SizeBuckets[index])
		}
		fmt.Fprintf(w, "gateway_media_operation_size_bytes_bucket{stage=%q,outcome=%q,le=%q} %d\n",
			metric.Stage, metric.Outcome, "+Inf", metric.Count)
		fmt.Fprintf(w, "gateway_media_operation_size_bytes_sum{stage=%q,outcome=%q} %d\n",
			metric.Stage, metric.Outcome, metric.BytesTotal)
		fmt.Fprintf(w, "gateway_media_operation_size_bytes_count{stage=%q,outcome=%q} %d\n",
			metric.Stage, metric.Outcome, metric.Count)
	}
	mediaBytes := a.mediaByteLimiter().snapshot()
	fmt.Fprintf(w, "gateway_media_inflight_bytes %d\n", mediaBytes.InUse)
	fmt.Fprintf(w, "gateway_media_inflight_capacity_bytes %d\n", mediaBytes.Capacity)
	fmt.Fprintf(w, "gateway_media_inflight_high_water_bytes %d\n", mediaBytes.HighWater)
	fmt.Fprintf(w, "gateway_media_backpressure_rejections_total %d\n", mediaBytes.Rejections)
}

func (a *App) recordProxyRequest(ctx context.Context, userID int64, service, method, path string, status int, duration time.Duration, bytesIn, bytesOut int64, ws bool, ip, userAgent string) {
	if a.store == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	err := a.store.RecordProxyRequest(writeCtx, domain.ProxyRequestRecord{
		UserID:          userID,
		RequestID:       requestIDFromContext(ctx),
		CorrelationID:   correlationIDFromContext(ctx),
		GenerationJobID: generationJobIDFromContext(ctx),
		Service:         service,
		Method:          method,
		Path:            truncate(path, 1000),
		Status:          status,
		DurationMS:      duration.Milliseconds(),
		BytesIn:         bytesIn,
		BytesOut:        bytesOut,
		WebSocket:       ws,
		ClientIP:        ip,
		UserAgent:       truncate(userAgent, 500),
	})
	if err != nil {
		log.Printf("record proxy request: %v", err)
	}
}

func (a *App) openWebSocketSession(ctx context.Context, userID int64, service, ip, userAgent string) int64 {
	id, err := a.store.OpenWebSocketSession(ctx, userID, service, ip, truncate(userAgent, 500))
	if err != nil {
		log.Printf("open websocket session: %v", err)
		return 0
	}
	return id
}

func (a *App) closeWebSocketSession(ctx context.Context, id int64, duration time.Duration) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := a.store.CloseWebSocketSession(writeCtx, id, duration.Milliseconds()); err != nil {
		log.Printf("close websocket session: %v", err)
	}
}

func (a *App) incProxyCount(service string, status int) {
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	a.proxyCounts[fmt.Sprintf("%s|%d", service, status)]++
}

func (a *App) audit(ctx context.Context, actor *int64, action, targetType string, targetID *int64, ip, userAgent string, metadata map[string]any) {
	err := a.store.RecordAudit(ctx, domain.AuditEvent{
		ActorUserID:     actor,
		RequestID:       requestIDFromContext(ctx),
		CorrelationID:   correlationIDFromContext(ctx),
		GenerationJobID: generationJobIDFromContext(ctx),
		Action:          action,
		TargetType:      targetType,
		TargetID:        targetID,
		IP:              ip,
		UserAgent:       truncate(userAgent, 500),
		Metadata:        metadata,
	})
	if err != nil {
		log.Printf("record audit event %q: %v", action, err)
	}
}

func serviceDisplayName(service string) string {
	switch service {
	case "quick_generation":
		return "Быстрая генерация"
	case "comfyui":
		return "ComfyUI"
	case "openwebui":
		return "OpenWebUI"
	default:
		return service
	}
}
