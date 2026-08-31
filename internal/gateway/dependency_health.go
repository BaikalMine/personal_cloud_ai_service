package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/mining"
)

const (
	dependencyComfyUI       = "comfyui"
	dependencyOpenWebUI     = "openwebui"
	dependencyOllama        = "ollama"
	dependencyModerator     = "moderator"
	dependencyMiningAgent   = "mining-agent"
	dependencySystemMonitor = "system-monitor"

	defaultDependencyCheckInterval = 10 * time.Second
	defaultDependencyStaleAfter    = 45 * time.Second
	defaultDependencyOfflineAfter  = 3 * time.Minute
)

type DependencyState string

const (
	DependencyOnline        DependencyState = "online"
	DependencyStale         DependencyState = "stale"
	DependencyOffline       DependencyState = "offline"
	DependencyMisconfigured DependencyState = "misconfigured"
)

type DependencyStatus struct {
	Key               string          `json:"key"`
	Name              string          `json:"name"`
	State             DependencyState `json:"state"`
	StateLabel        string          `json:"state_label"`
	Detail            string          `json:"detail"`
	LastCheckedAt     *time.Time      `json:"last_checked_at,omitempty"`
	LastSuccessAt     *time.Time      `json:"last_success_at,omitempty"`
	LastDataAt        *time.Time      `json:"last_data_at,omitempty"`
	LastErrorAt       *time.Time      `json:"last_error_at,omitempty"`
	LastError         string          `json:"last_error,omitempty"`
	NextCheckAt       *time.Time      `json:"next_check_at,omitempty"`
	RetryInSeconds    int             `json:"retry_in_seconds"`
	LatencyMillis     int64           `json:"latency_ms"`
	RequiresFreshData bool            `json:"requires_fresh_data"`
}

type dependencyRecord struct {
	LastCheckedAt time.Time
	LastSuccessAt time.Time
	LastDataAt    time.Time
	LastErrorAt   time.Time
	NextCheckAt   time.Time
	LastError     string
	Detail        string
	Latency       time.Duration
	ProbeOK       bool
	Misconfigured bool
	Probing       bool
}

type dependencyMonitor struct {
	mu           sync.RWMutex
	records      map[string]dependencyRecord
	now          func() time.Time
	checkEvery   time.Duration
	staleAfter   time.Duration
	offlineAfter time.Duration
}

type dependencySpec struct {
	Key          string
	Name         string
	Configured   bool
	RequiresData bool
	Probe        func(context.Context) (string, *time.Time, bool, error)
}

func newDependencyMonitor(checkEvery, staleAfter, offlineAfter time.Duration, now func() time.Time) *dependencyMonitor {
	if checkEvery <= 0 {
		checkEvery = defaultDependencyCheckInterval
	}
	if staleAfter < 2*checkEvery {
		staleAfter = defaultDependencyStaleAfter
		if staleAfter < 2*checkEvery {
			staleAfter = 2 * checkEvery
		}
	}
	if offlineAfter <= staleAfter {
		offlineAfter = defaultDependencyOfflineAfter
		if offlineAfter <= staleAfter {
			offlineAfter = staleAfter + 2*checkEvery
		}
	}
	if now == nil {
		now = time.Now
	}
	return &dependencyMonitor{
		records: make(map[string]dependencyRecord), now: now,
		checkEvery: checkEvery, staleAfter: staleAfter, offlineAfter: offlineAfter,
	}
}

func (m *dependencyMonitor) claim(key string) bool {
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.records[key]
	if record.Probing || (!record.NextCheckAt.IsZero() && now.Before(record.NextCheckAt)) {
		return false
	}
	record.Probing = true
	record.NextCheckAt = now.Add(m.checkEvery)
	m.records[key] = record
	return true
}

func (m *dependencyMonitor) success(key, detail string, dataAt *time.Time, latency time.Duration) {
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.records[key]
	record.LastCheckedAt = now
	record.LastSuccessAt = now
	record.NextCheckAt = now.Add(m.checkEvery)
	record.Detail = strings.TrimSpace(detail)
	record.Latency = latency
	record.ProbeOK = true
	record.Misconfigured = false
	record.Probing = false
	if dataAt != nil && !dataAt.IsZero() {
		collectedAt := dataAt.UTC()
		if collectedAt.After(now) {
			collectedAt = now
		}
		if collectedAt.After(record.LastDataAt) {
			record.LastDataAt = collectedAt
		}
	}
	m.records[key] = record
}

func (m *dependencyMonitor) failure(key, message string, misconfigured bool, latency time.Duration) {
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.records[key]
	record.LastCheckedAt = now
	record.LastErrorAt = now
	record.NextCheckAt = now.Add(m.checkEvery)
	record.LastError = strings.TrimSpace(message)
	record.Detail = record.LastError
	record.Latency = latency
	record.ProbeOK = false
	record.Misconfigured = misconfigured
	record.Probing = false
	m.records[key] = record
}

func (m *dependencyMonitor) observeData(key string, collectedAt time.Time) {
	if collectedAt.IsZero() {
		return
	}
	now := m.now().UTC()
	collectedAt = collectedAt.UTC()
	if collectedAt.After(now) {
		collectedAt = now
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.records[key]
	if collectedAt.After(record.LastDataAt) {
		record.LastDataAt = collectedAt
		m.records[key] = record
	}
}

func (m *dependencyMonitor) status(spec dependencySpec) DependencyStatus {
	now := m.now().UTC()
	m.mu.RLock()
	record := m.records[spec.Key]
	m.mu.RUnlock()
	status := DependencyStatus{
		Key: spec.Key, Name: spec.Name, LastError: record.LastError,
		LatencyMillis: record.Latency.Milliseconds(), RequiresFreshData: spec.RequiresData,
	}
	status.LastCheckedAt = timePointer(record.LastCheckedAt)
	status.LastSuccessAt = timePointer(record.LastSuccessAt)
	status.LastDataAt = timePointer(record.LastDataAt)
	status.LastErrorAt = timePointer(record.LastErrorAt)
	status.NextCheckAt = timePointer(record.NextCheckAt)
	if !record.NextCheckAt.IsZero() && record.NextCheckAt.After(now) {
		status.RetryInSeconds = int((record.NextCheckAt.Sub(now) + time.Second - 1) / time.Second)
	}

	switch {
	case !spec.Configured || record.Misconfigured:
		status.State = DependencyMisconfigured
		status.Detail = record.Detail
		if status.Detail == "" {
			status.Detail = "Проверьте адрес и параметры подключения."
		}
	case record.LastSuccessAt.IsZero():
		status.State = DependencyOffline
		status.Detail = record.Detail
		if status.Detail == "" {
			status.Detail = "Успешных проверок пока не было."
		}
	default:
		freshAt := record.LastSuccessAt
		missingData := spec.RequiresData && record.LastDataAt.IsZero()
		if spec.RequiresData && !missingData && record.LastDataAt.Before(freshAt) {
			freshAt = record.LastDataAt
		}
		age := now.Sub(freshAt)
		if age < 0 {
			age = 0
		}
		switch {
		case !missingData && age <= m.staleAfter:
			status.State = DependencyOnline
			status.Detail = record.Detail
			if !record.ProbeOK {
				status.Detail = "Последний ответ ещё свежий; повторная проверка не удалась."
			} else if status.Detail == "" {
				status.Detail = "Соединение подтверждено."
			}
		case age <= m.offlineAfter || missingData:
			status.State = DependencyStale
			status.Detail = "Последние данные устарели; ждём следующую успешную проверку."
		default:
			status.State = DependencyOffline
			status.Detail = record.Detail
			if status.Detail == "" || record.ProbeOK {
				status.Detail = "Свежего ответа нет."
			}
		}
	}
	status.StateLabel = dependencyStateLabel(status.State)
	return status
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

func dependencyStateLabel(state DependencyState) string {
	switch state {
	case DependencyOnline:
		return "В сети"
	case DependencyStale:
		return "Данные устарели"
	case DependencyMisconfigured:
		return "Не настроен"
	default:
		return "Нет связи"
	}
}

func (a *App) dependencyMonitor() *dependencyMonitor {
	a.dependencyOnce.Do(func() {
		a.dependencyHealth = newDependencyMonitor(
			a.cfg.DependencyCheckInterval,
			a.cfg.DependencyStaleAfter,
			a.cfg.DependencyOfflineAfter,
			time.Now,
		)
	})
	return a.dependencyHealth
}

func (a *App) dependencySpecs() []dependencySpec {
	return []dependencySpec{
		{Key: dependencyComfyUI, Name: "ComfyUI", Configured: a.cfg.ComfyUIUpstream != nil, Probe: a.httpDependencyProbe(a.cfg.ComfyUIUpstream, "", a.cfg.ComfyUIUpstreamAuthHeader)},
		{Key: dependencyOpenWebUI, Name: "OpenWebUI", Configured: a.cfg.OpenWebUIUpstream != nil, Probe: a.httpDependencyProbe(a.cfg.OpenWebUIUpstream, "", a.cfg.OpenWebUIUpstreamAuth)},
		{Key: dependencyOllama, Name: "Промт-ассистент", Configured: a.cfg.OllamaUpstream != nil && strings.TrimSpace(a.cfg.PromptAssistantModel) != "", Probe: a.httpDependencyProbe(a.cfg.OllamaUpstream, "", "")},
		{Key: dependencyModerator, Name: "Проверка 18+", Configured: a.cfg.ContentModeratorUpstream != nil, Probe: a.httpDependencyProbe(a.cfg.ContentModeratorUpstream, "/healthz", "")},
		{Key: dependencyMiningAgent, Name: "Управление майнингом", Configured: a.mining != nil && a.mining.Configured(), Probe: a.agentDependencyProbe(a.mining)},
		{Key: dependencySystemMonitor, Name: "Мониторинг Windows", Configured: a.systemMonitor != nil && a.systemMonitor.Configured(), RequiresData: true, Probe: a.agentDependencyProbe(a.systemMonitor)},
	}
}

func (a *App) refreshDependencyStatuses(ctx context.Context) {
	monitor := a.dependencyMonitor()
	var wg sync.WaitGroup
	for _, spec := range a.dependencySpecs() {
		if !spec.Configured || spec.Probe == nil || !monitor.claim(spec.Key) {
			continue
		}
		wg.Add(1)
		go func(spec dependencySpec) {
			defer wg.Done()
			started := time.Now()
			detail, dataAt, misconfigured, err := spec.Probe(ctx)
			latency := time.Since(started)
			if err != nil {
				monitor.failure(spec.Key, err.Error(), misconfigured, latency)
				return
			}
			monitor.success(spec.Key, detail, dataAt, latency)
		}(spec)
	}
	wg.Wait()
}

func (a *App) dependencyStatuses() []DependencyStatus {
	specs := a.dependencySpecs()
	statuses := make([]DependencyStatus, 0, len(specs))
	for _, spec := range specs {
		statuses = append(statuses, a.dependencyMonitor().status(spec))
	}
	return statuses
}

func (a *App) dependencyStatus(key string) DependencyStatus {
	for _, spec := range a.dependencySpecs() {
		if spec.Key == key {
			return a.dependencyMonitor().status(spec)
		}
	}
	return DependencyStatus{Key: key, State: DependencyMisconfigured, StateLabel: dependencyStateLabel(DependencyMisconfigured), Detail: "Зависимость не зарегистрирована."}
}

func (a *App) httpDependencyProbe(base *url.URL, endpoint, authHeader string) func(context.Context) (string, *time.Time, bool, error) {
	if base == nil {
		return nil
	}
	target := *base
	if endpoint != "" {
		target.Path = strings.TrimRight(target.Path, "/") + endpoint
	}
	target.RawQuery = ""
	target.Fragment = ""
	return func(parent context.Context) (string, *time.Time, bool, error) {
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return "", nil, true, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "ai-access-gateway/dependency-health")
		if authHeader != "" {
			request.Header.Set("Authorization", authHeader)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return "", nil, false, err
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return "", nil, true, fmt.Errorf("проверка авторизации вернула HTTP %d", response.StatusCode)
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return "", nil, false, fmt.Errorf("проверка доступности вернула HTTP %d", response.StatusCode)
		}
		now := time.Now().UTC()
		return "Соединение подтверждено.", &now, false, nil
	}
}

func (a *App) agentDependencyProbe(client *mining.Client) func(context.Context) (string, *time.Time, bool, error) {
	if client == nil {
		return nil
	}
	return func(ctx context.Context) (string, *time.Time, bool, error) {
		if err := client.Health(ctx); err != nil {
			return "", nil, errors.Is(err, mining.ErrUnavailable) && !client.Configured(), err
		}
		return "Heartbeat получен.", nil, false, nil
	}
}
