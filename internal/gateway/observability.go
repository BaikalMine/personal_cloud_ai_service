package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/domain"
)

const (
	gatewayObservationInterval = 30 * time.Second
	generationOverdueAfter     = 45 * time.Minute
)

var serviceLatencyBucketsMS = []int64{10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000}

type serviceLatencyKey struct {
	Component string
	Operation string
}

type serviceLatencyValue struct {
	Count   int64
	SumMS   int64
	Buckets []int64
}

type serviceLatencySnapshot struct {
	Component string
	Operation string
	Count     int64
	SumMS     int64
	Buckets   []int64
}

type serviceLatencyRegistry struct {
	mu     sync.RWMutex
	values map[serviceLatencyKey]*serviceLatencyValue
}

func newServiceLatencyRegistry() *serviceLatencyRegistry {
	return &serviceLatencyRegistry{values: make(map[serviceLatencyKey]*serviceLatencyValue)}
}

func (registry *serviceLatencyRegistry) observe(component, operation string, latency time.Duration) {
	if registry == nil {
		return
	}
	latencyMS := latency.Milliseconds()
	if latencyMS < 0 {
		latencyMS = 0
	}
	key := serviceLatencyKey{Component: component, Operation: operation}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	value := registry.values[key]
	if value == nil {
		value = &serviceLatencyValue{Buckets: make([]int64, len(serviceLatencyBucketsMS))}
		registry.values[key] = value
	}
	value.Count++
	value.SumMS += latencyMS
	for index, upperBound := range serviceLatencyBucketsMS {
		if latencyMS <= upperBound {
			value.Buckets[index]++
		}
	}
}

func (registry *serviceLatencyRegistry) snapshot() []serviceLatencySnapshot {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]serviceLatencySnapshot, 0, len(registry.values))
	for key, value := range registry.values {
		result = append(result, serviceLatencySnapshot{
			Component: key.Component,
			Operation: key.Operation,
			Count:     value.Count,
			SumMS:     value.SumMS,
			Buckets:   append([]int64(nil), value.Buckets...),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Component == result[j].Component {
			return result[i].Operation < result[j].Operation
		}
		return result[i].Component < result[j].Component
	})
	return result
}

func (a *App) serviceLatencyRegistry() *serviceLatencyRegistry {
	if a == nil {
		return nil
	}
	a.observabilityOnce.Do(func() {
		if a.serviceLatencies == nil {
			a.serviceLatencies = newServiceLatencyRegistry()
		}
	})
	return a.serviceLatencies
}

func serviceObservationOutcome(err error, misconfigured bool) string {
	if misconfigured {
		return "misconfigured"
	}
	if err == nil {
		return "ok"
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
		return "timeout"
	}
	return "error"
}

func (a *App) observeServiceCall(ctx context.Context, component, operation string, started time.Time, callErr error, misconfigured bool, errorCode, detail string) {
	component = truncate(strings.TrimSpace(component), 80)
	operation = truncate(strings.TrimSpace(operation), 120)
	if component == "" || operation == "" {
		return
	}
	latency := time.Since(started)
	a.serviceLatencyRegistry().observe(component, operation, latency)
	if a.store == nil {
		return
	}
	if callErr != nil && strings.TrimSpace(detail) == "" {
		detail = callErr.Error()
	}
	observation := domain.ServiceObservationRecord{
		Component:       component,
		Operation:       operation,
		Outcome:         serviceObservationOutcome(callErr, misconfigured),
		LatencyMS:       max(0, latency.Milliseconds()),
		GenerationJobID: generationJobIDFromContext(ctx),
		CorrelationID:   correlationIDFromContext(ctx),
		ErrorCode:       truncate(strings.TrimSpace(errorCode), 80),
		Detail:          truncate(strings.TrimSpace(detail), 1000),
		ObservedAt:      time.Now().UTC(),
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := a.store.RecordServiceObservation(writeCtx, observation); err != nil {
		logGateway(ctx, slog.LevelError, "service_observation_persist_failed", "Failed to persist service observation",
			"component", component,
			"operation", operation,
			"error", err,
		)
	}
}

func (a *App) captureGatewayObservation(ctx context.Context) (int64, error) {
	started := time.Now()
	observation, err := a.store.CollectGatewayObservation(ctx, generationOverdueAfter)
	if err != nil {
		a.observeServiceCall(ctx, "database", "gateway_snapshot", started, err, false, "snapshot_query_failed", "")
		return 0, err
	}
	err = a.store.RecordGatewayObservation(ctx, observation)
	a.observeServiceCall(ctx, "database", "gateway_snapshot", started, err, false, "snapshot_persist_failed", "")
	if err != nil {
		return 0, err
	}
	return 1, nil
}
