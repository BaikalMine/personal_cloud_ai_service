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
}

func (a *App) recordProxyRequest(ctx context.Context, userID int64, service, method, path string, status int, duration time.Duration, bytesIn, bytesOut int64, ws bool, ip, userAgent string) {
	if a.store == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	err := a.store.RecordProxyRequest(writeCtx, domain.ProxyRequestRecord{
		UserID:     userID,
		Service:    service,
		Method:     method,
		Path:       truncate(path, 1000),
		Status:     status,
		DurationMS: duration.Milliseconds(),
		BytesIn:    bytesIn,
		BytesOut:   bytesOut,
		WebSocket:  ws,
		ClientIP:   ip,
		UserAgent:  truncate(userAgent, 500),
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
		ActorUserID: actor,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		IP:          ip,
		UserAgent:   truncate(userAgent, 500),
		Metadata:    metadata,
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
