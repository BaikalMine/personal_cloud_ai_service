package gateway

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

type maintenanceWorkerRun func(context.Context) (int64, error)

type maintenanceWorkerSpec struct {
	Key          string
	Name         string
	Interval     time.Duration
	Timeout      time.Duration
	InitialDelay time.Duration
	RetryDelay   time.Duration
	MaxBackoff   time.Duration
	Run          maintenanceWorkerRun
}

type MaintenanceWorkerState struct {
	Key                 string        `json:"key"`
	Name                string        `json:"name"`
	Status              string        `json:"status"`
	StatusLabel         string        `json:"status_label"`
	Interval            time.Duration `json:"-"`
	Timeout             time.Duration `json:"-"`
	IntervalSeconds     int64         `json:"interval_seconds"`
	TimeoutSeconds      int64         `json:"timeout_seconds"`
	Running             bool          `json:"running"`
	LastStartedAt       *time.Time    `json:"last_started_at,omitempty"`
	LastFinishedAt      *time.Time    `json:"last_finished_at,omitempty"`
	LastSuccessAt       *time.Time    `json:"last_success_at,omitempty"`
	NextRunAt           *time.Time    `json:"next_run_at,omitempty"`
	LastDurationMillis  int64         `json:"last_duration_ms"`
	LastItems           int64         `json:"last_items"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
	LastError           string        `json:"last_error,omitempty"`
}

func (s MaintenanceWorkerState) IntervalText() string {
	return maintenanceDurationText(s.Interval)
}

func (s MaintenanceWorkerState) TimeoutText() string {
	return maintenanceDurationText(s.Timeout)
}

func maintenanceDurationText(value time.Duration) string {
	if value <= 0 {
		return "-"
	}
	if value%time.Hour == 0 {
		return fmt.Sprintf("%d ч.", int64(value/time.Hour))
	}
	if value%time.Minute == 0 {
		return fmt.Sprintf("%d мин.", int64(value/time.Minute))
	}
	if value%time.Second == 0 {
		return fmt.Sprintf("%d сек.", int64(value/time.Second))
	}
	return value.Round(time.Millisecond).String()
}

type maintenanceRegistry struct {
	mu     sync.RWMutex
	now    func() time.Time
	order  []string
	states map[string]MaintenanceWorkerState
}

func newMaintenanceRegistry(now func() time.Time) *maintenanceRegistry {
	if now == nil {
		now = time.Now
	}
	return &maintenanceRegistry{now: now, states: make(map[string]MaintenanceWorkerState)}
}

func (a *App) maintenanceWorkerRegistry() *maintenanceRegistry {
	if a == nil {
		return nil
	}
	a.maintenanceOnce.Do(func() {
		if a.maintenanceWorkers == nil {
			a.maintenanceWorkers = newMaintenanceRegistry(time.Now)
		}
	})
	return a.maintenanceWorkers
}

func (a *App) maintenanceWorkerStates() []MaintenanceWorkerState {
	registry := a.maintenanceWorkerRegistry()
	if registry == nil {
		return nil
	}
	return registry.snapshot()
}

func (r *maintenanceRegistry) register(spec maintenanceWorkerSpec) {
	if r == nil || spec.Key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.states[spec.Key]; exists {
		return
	}
	next := r.now().Add(spec.InitialDelay)
	r.order = append(r.order, spec.Key)
	r.states[spec.Key] = MaintenanceWorkerState{
		Key: spec.Key, Name: spec.Name, Status: "waiting", StatusLabel: maintenanceWorkerStatusLabel("waiting"),
		Interval: spec.Interval, Timeout: spec.Timeout, IntervalSeconds: durationSeconds(spec.Interval), TimeoutSeconds: durationSeconds(spec.Timeout), NextRunAt: timePointer(next),
	}
}

func (r *maintenanceRegistry) begin(key string) (time.Time, bool) {
	if r == nil {
		return time.Time{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.states[key]
	if !exists || state.Running {
		return time.Time{}, false
	}
	now := r.now().UTC()
	state.Running = true
	state.Status = "running"
	state.StatusLabel = maintenanceWorkerStatusLabel(state.Status)
	state.LastStartedAt = timePointer(now)
	state.NextRunAt = nil
	r.states[key] = state
	return now, true
}

func (r *maintenanceRegistry) finish(key string, started time.Time, items int64, runErr error, nextRunAt time.Time) int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.states[key]
	if !exists {
		return 0
	}
	now := r.now().UTC()
	state.Running = false
	state.LastFinishedAt = timePointer(now)
	state.LastDurationMillis = max(0, now.Sub(started).Milliseconds())
	state.LastItems = items
	state.NextRunAt = timePointer(nextRunAt.UTC())
	if runErr == nil {
		state.Status = "healthy"
		state.StatusLabel = maintenanceWorkerStatusLabel(state.Status)
		state.LastSuccessAt = timePointer(now)
		state.ConsecutiveFailures = 0
		state.LastError = ""
	} else {
		state.Status = "retrying"
		state.StatusLabel = maintenanceWorkerStatusLabel(state.Status)
		state.ConsecutiveFailures++
		state.LastError = truncate(runErr.Error(), 500)
	}
	r.states[key] = state
	return state.ConsecutiveFailures
}

func (r *maintenanceRegistry) stop(key string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.states[key]
	if !exists {
		return
	}
	state.Running = false
	state.Status = "stopped"
	state.StatusLabel = maintenanceWorkerStatusLabel(state.Status)
	state.NextRunAt = nil
	r.states[key] = state
}

func (r *maintenanceRegistry) interrupt(key string, started time.Time, items int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.states[key]
	if !exists {
		return
	}
	now := r.now().UTC()
	state.Running = false
	state.Status = "stopped"
	state.StatusLabel = maintenanceWorkerStatusLabel(state.Status)
	state.LastFinishedAt = timePointer(now)
	state.LastDurationMillis = max(0, now.Sub(started).Milliseconds())
	state.LastItems = items
	state.NextRunAt = nil
	r.states[key] = state
}

func (r *maintenanceRegistry) schedule(key string, nextRunAt time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.states[key]
	if !exists {
		return
	}
	state.NextRunAt = timePointer(nextRunAt.UTC())
	r.states[key] = state
}

func (r *maintenanceRegistry) snapshot() []MaintenanceWorkerState {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]MaintenanceWorkerState, 0, len(r.order))
	for _, key := range r.order {
		state := r.states[key]
		result = append(result, state)
	}
	return result
}

func maintenanceWorkerStatusLabel(status string) string {
	switch status {
	case "running":
		return "Выполняется"
	case "healthy":
		return "Работает"
	case "retrying":
		return "Повтор после ошибки"
	case "stopped":
		return "Остановлен"
	default:
		return "Ожидает запуска"
	}
}

func durationSeconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	seconds := int64(value / time.Second)
	if seconds == 0 {
		return 1
	}
	return seconds
}

func normalizedMaintenanceWorkerSpec(spec maintenanceWorkerSpec) maintenanceWorkerSpec {
	if spec.Interval <= 0 {
		spec.Interval = maintenanceInterval
	}
	if spec.Timeout <= 0 {
		spec.Timeout = time.Minute
	}
	if spec.RetryDelay <= 0 {
		spec.RetryDelay = 15 * time.Second
	}
	if spec.MaxBackoff < spec.RetryDelay {
		spec.MaxBackoff = 5 * time.Minute
	}
	return spec
}

func maintenanceWorkerBackoff(spec maintenanceWorkerSpec, failures int) time.Duration {
	delay := spec.RetryDelay
	for attempt := 1; attempt < failures && delay < spec.MaxBackoff; attempt++ {
		if delay > spec.MaxBackoff/2 {
			return spec.MaxBackoff
		}
		delay *= 2
	}
	if delay > spec.MaxBackoff {
		return spec.MaxBackoff
	}
	return delay
}

func waitMaintenanceWorker(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func runMaintenanceWorker(ctx context.Context, registry *maintenanceRegistry, rawSpec maintenanceWorkerSpec) {
	spec := normalizedMaintenanceWorkerSpec(rawSpec)
	registry.register(spec)
	delay := spec.InitialDelay
	for waitMaintenanceWorker(ctx, delay) {
		started, claimed := registry.begin(spec.Key)
		if !claimed {
			delay = spec.RetryDelay
			continue
		}
		runCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
		items, runErr := spec.Run(runCtx)
		contextErr := runCtx.Err()
		cancel()
		if runErr == nil && contextErr != nil && ctx.Err() == nil {
			runErr = contextErr
		}
		if ctx.Err() != nil {
			registry.interrupt(spec.Key, started, items)
			break
		}
		delay = spec.Interval
		nextRun := registry.now().UTC().Add(delay)
		failures := registry.finish(spec.Key, started, items, runErr, nextRun)
		if runErr != nil {
			delay = maintenanceWorkerBackoff(spec, failures)
			nextRun = registry.now().UTC().Add(delay)
			registry.schedule(spec.Key, nextRun)
			log.Printf("maintenance worker %s: %v; retry in %s", spec.Key, runErr, delay)
		}
	}
	registry.stop(spec.Key)
}

func runMaintenanceWorkers(ctx context.Context, registry *maintenanceRegistry, specs []maintenanceWorkerSpec) {
	var wg sync.WaitGroup
	for _, rawSpec := range specs {
		spec := normalizedMaintenanceWorkerSpec(rawSpec)
		if spec.Run == nil || spec.Key == "" {
			continue
		}
		registry.register(spec)
		wg.Add(1)
		go func() {
			defer wg.Done()
			runMaintenanceWorker(ctx, registry, spec)
		}()
	}
	<-ctx.Done()
	wg.Wait()
}
