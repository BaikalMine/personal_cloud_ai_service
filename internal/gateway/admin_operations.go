package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const adminOperationsActiveJobLimit = 100

type adminOperationsView struct {
	GeneratedAt       time.Time
	OverallState      string
	OverallLabel      string
	OverallDetail     string
	CriticalCount     int
	WarningCount      int
	Attention         []adminAttentionItem
	ActiveJobs        []adminActiveJobView
	Queue             adminQueueStatusView
	Generation        domain.GenerationObservabilitySummary
	Gateway           domain.GatewayObservationSummary
	Leases            []domain.QuickGenerationMiningLease
	Compatibility     adminCompatibilitySummaryView
	Storage           adminStorageSummaryView
	Failures          []domain.GenerationFailureSummary
	ProblemWorkers    []MaintenanceWorkerState
	BackgroundWorkers []MaintenanceWorkerState
}

type adminAttentionItem struct {
	Key      string
	Severity string
	Title    string
	Detail   string
	Count    int
	Href     string
	Action   string
}

type adminActiveJobView struct {
	PublicID       string
	Username       string
	DeletedUser    bool
	Workflow       string
	Model          string
	State          domain.GenerationJobState
	StateLabel     string
	StateClass     string
	StatusMessage  string
	CreatedAt      time.Time
	StateChangedAt time.Time
	Age            time.Duration
	StateAge       time.Duration
	Overdue        bool
	HasLease       bool
}

type adminQueueStatusView struct {
	Available            bool
	State                string
	StateLabel           string
	Detail               string
	Error                string
	Running              int
	Pending              int
	EstimatedWaitSeconds int64
	AverageTaskSeconds   int64
}

type adminCompatibilitySummaryView struct {
	State       string
	StateLabel  string
	Detail      string
	Compatible  int
	Failed      int
	Unavailable int
	Stale       bool
}

type adminStorageSummaryView struct {
	DatabaseBytes  int64
	DatabaseGrowth int64
	MediaBytes     int64
	EstimatedRows  int64
	UnmappedCount  int
	Cleanup        databaseCleanupView
	TopTables      []databaseTableView
}

func (a *App) loadAdminOperations(ctx context.Context, system SystemOverview) (adminOperationsView, error) {
	observability, err := a.loadAdminObservability(ctx)
	if err != nil {
		return adminOperationsView{}, err
	}
	activeJobs, err := a.store.ListActiveGenerationJobs(ctx, adminOperationsActiveJobLimit)
	if err != nil {
		return adminOperationsView{}, err
	}
	storage, err := a.databaseStorageView(ctx)
	if err != nil {
		return adminOperationsView{}, err
	}

	comfy := dependencyStatusByKey(system.Dependencies, dependencyComfyUI)
	queue := adminQueueStatusView{State: "offline", StateLabel: "Недоступна", Detail: comfy.Detail, Error: comfy.LastError}
	if comfy.State == DependencyOnline || comfy.State == DependencyStale {
		queueCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		queueOverview, queueErr := a.generationQueueOverview(queueCtx)
		cancel()
		queue = newAdminQueueStatusView(queueOverview, queueErr)
	}

	compatibility := workflowCompatibilityReport{GeneratedAt: time.Now().UTC()}
	if comfy.State == DependencyOnline || comfy.State == DependencyStale {
		compatibilityCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		compatibility = a.currentWorkflowCompatibility(compatibilityCtx, false)
		cancel()
	} else {
		compatibility.Error = firstNonEmpty(comfy.LastError, comfy.Detail, "ComfyUI недоступен")
	}

	observability.Dependencies = system.Dependencies
	observability.Workers = system.Workers
	observability.WorkerIssues = observability.WorkerIssues[:0]
	observability.HealthyWorkers = 0
	for _, worker := range system.Workers {
		if maintenanceWorkerIsForeground(worker) {
			observability.WorkerIssues = append(observability.WorkerIssues, worker)
			continue
		}
		if worker.Status == "healthy" {
			observability.HealthyWorkers++
		}
	}

	return newAdminOperationsView(time.Now().UTC(), observability, activeJobs, queue, compatibility, storage, system), nil
}

func newAdminOperationsView(now time.Time, observability adminObservabilityView, jobs []domain.GenerationJob, queue adminQueueStatusView, compatibility workflowCompatibilityReport, storage databaseStorageView, system SystemOverview) adminOperationsView {
	view := adminOperationsView{
		GeneratedAt:   now,
		Queue:         queue,
		Generation:    observability.Generation,
		Gateway:       observability.Gateway,
		Leases:        observability.Leases,
		Compatibility: newAdminCompatibilitySummaryView(compatibility),
		Storage:       newAdminStorageSummaryView(storage, observability.Gateway.DatabaseGrowth24Hours),
	}
	for _, worker := range system.Workers {
		if maintenanceWorkerIsForeground(worker) {
			view.ProblemWorkers = append(view.ProblemWorkers, worker)
		} else {
			view.BackgroundWorkers = append(view.BackgroundWorkers, worker)
		}
	}
	for _, job := range jobs {
		view.ActiveJobs = append(view.ActiveJobs, newAdminActiveJobView(now, observability.OverdueAfter, job, observability.Leases))
	}
	if len(observability.Failures) > 6 {
		view.Failures = append(view.Failures, observability.Failures[:6]...)
	} else {
		view.Failures = append(view.Failures, observability.Failures...)
	}
	view.Attention = buildAdminAttention(view, system.Dependencies)
	for _, item := range view.Attention {
		switch item.Severity {
		case "critical":
			view.CriticalCount++
		case "warning":
			view.WarningCount++
		}
	}
	switch {
	case view.CriticalCount > 0:
		view.OverallState = "critical"
		view.OverallLabel = "Нужно вмешательство"
		view.OverallDetail = fmt.Sprintf("Критичных состояний: %d; предупреждений: %d", view.CriticalCount, view.WarningCount)
	case view.WarningCount > 0:
		view.OverallState = "warning"
		view.OverallLabel = "Есть предупреждения"
		view.OverallDetail = fmt.Sprintf("Проверьте %d состояния", view.WarningCount)
	default:
		view.OverallState = "healthy"
		view.OverallLabel = "Работает штатно"
		view.OverallDetail = "Очереди, зависимости и фоновые задачи без отклонений"
	}
	return view
}

func newAdminQueueStatusView(queue generationQueueOverview, err error) adminQueueStatusView {
	if err != nil {
		return adminQueueStatusView{State: "offline", StateLabel: "Недоступна", Detail: "Не удалось получить очередь ComfyUI", Error: err.Error()}
	}
	view := adminQueueStatusView{
		Available:            true,
		Running:              queue.Running,
		Pending:              queue.Pending,
		Detail:               queue.CurrentTask,
		EstimatedWaitSeconds: int64(queue.EstimatedWaitSeconds),
		AverageTaskSeconds:   int64(queue.AverageTaskSeconds),
	}
	switch {
	case queue.Running > 0:
		view.State, view.StateLabel = "running", "Выполняется"
		view.Detail = firstNonEmpty(view.Detail, "ComfyUI выполняет текущее задание")
	case queue.Pending > 0:
		view.State, view.StateLabel = "waiting", "Ожидание"
		view.Detail = firstNonEmpty(view.Detail, "Задания ожидают запуска")
	default:
		view.State, view.StateLabel = "idle", "Свободна"
		view.Detail = firstNonEmpty(view.Detail, "Очередь готова к следующему запуску")
	}
	return view
}

func newAdminCompatibilitySummaryView(report workflowCompatibilityReport) adminCompatibilitySummaryView {
	view := adminCompatibilitySummaryView{
		Compatible:  report.Compatible,
		Failed:      report.Failed,
		Unavailable: report.Unavailable,
		Stale:       report.Stale,
	}
	switch {
	case report.Error != "":
		view.State, view.StateLabel = "critical", "Проверка недоступна"
		view.Detail = trimOperationsDetail(report.Error, 220)
	case report.Failed > 0:
		view.State, view.StateLabel = "critical", "Есть несовместимые сценарии"
		view.Detail = fmt.Sprintf("Ошибок: %d; совместимо: %d", report.Failed, report.Compatible)
	case report.Unavailable > 0:
		view.State, view.StateLabel = "warning", "Не все модели доступны"
		view.Detail = fmt.Sprintf("Недоступно: %d; совместимо: %d", report.Unavailable, report.Compatible)
	case report.Stale:
		view.State, view.StateLabel = "warning", "Используется прошлый снимок"
		view.Detail = fmt.Sprintf("Совместимых сценариев: %d", report.Compatible)
	default:
		view.State, view.StateLabel = "healthy", "Workflow совместимы"
		view.Detail = fmt.Sprintf("Проверено сценариев: %d", report.Compatible)
	}
	return view
}

func newAdminStorageSummaryView(storage databaseStorageView, growth int64) adminStorageSummaryView {
	view := adminStorageSummaryView{
		DatabaseBytes:  storage.DatabaseBytes,
		DatabaseGrowth: growth,
		EstimatedRows:  storage.EstimatedRows,
		UnmappedCount:  storage.UnmappedCount,
		Cleanup:        storage.Cleanup,
	}
	tables := append([]databaseTableView{}, storage.ManagedTables...)
	tables = append(tables, storage.LifecycleTables...)
	for _, table := range tables {
		switch table.Name {
		case "content_media", "content_media_chunks", "comfy_input_assets", "comfy_output_ownership", "comfy_output_cleanup_tombstones":
			view.MediaBytes += table.TotalBytes
		}
	}
	sort.SliceStable(tables, func(left, right int) bool {
		if tables[left].TotalBytes != tables[right].TotalBytes {
			return tables[left].TotalBytes > tables[right].TotalBytes
		}
		return tables[left].Label < tables[right].Label
	})
	if len(tables) > 5 {
		tables = tables[:5]
	}
	view.TopTables = tables
	return view
}

func newAdminActiveJobView(now time.Time, overdueAfter time.Duration, job domain.GenerationJob, leases []domain.QuickGenerationMiningLease) adminActiveJobView {
	changedAt := job.StateChangedAt
	if changedAt.IsZero() {
		changedAt = job.CreatedAt
	}
	username := strings.TrimSpace(job.UsernameSnapshot)
	if username == "" {
		username = "Удалённый пользователь"
	}
	workflow := firstNonEmpty(strings.TrimSpace(job.WorkflowID), strings.TrimSpace(job.TemplateID), "Быстрая генерация")
	model := firstNonEmpty(strings.TrimSpace(job.ModelName), "Модель не указана")
	stateAge := now.Sub(changedAt)
	if stateAge < 0 {
		stateAge = 0
	}
	age := now.Sub(job.CreatedAt)
	if age < 0 {
		age = 0
	}
	view := adminActiveJobView{
		PublicID:       job.PublicID,
		Username:       username,
		DeletedUser:    job.UserID == nil,
		Workflow:       workflow,
		Model:          model,
		State:          job.State,
		StateLabel:     generationJobStateLabel(job.State),
		StatusMessage:  firstNonEmpty(strings.TrimSpace(job.StatusMessage), "Ожидаем следующий этап"),
		CreatedAt:      job.CreatedAt,
		StateChangedAt: changedAt,
		Age:            age,
		StateAge:       stateAge,
	}
	view.Overdue = overdueAfter > 0 && stateAge >= overdueAfter
	switch {
	case view.Overdue:
		view.StateClass = "overdue"
	case job.State == domain.GenerationJobRunning || job.State == domain.GenerationJobPostprocessing || job.State == domain.GenerationJobArchiving:
		view.StateClass = "running"
	default:
		view.StateClass = "waiting"
	}
	for _, lease := range leases {
		if lease.GenerationJobID == job.ID {
			view.HasLease = true
			break
		}
	}
	return view
}

func buildAdminAttention(view adminOperationsView, dependencies []DependencyStatus) []adminAttentionItem {
	items := make([]adminAttentionItem, 0, 8)
	if view.Generation.OverdueJobs > 0 {
		items = append(items, adminAttentionItem{
			Key: "overdue-jobs", Severity: "critical", Title: "Задания не меняют состояние",
			Detail: "Откройте трассу и проверьте последний завершённый этап.", Count: int(view.Generation.OverdueJobs), Href: "#active-jobs", Action: "К заданиям",
		})
	}
	criticalDependencies, staleDependencies := dependencyProblems(dependencies)
	if len(criticalDependencies) > 0 {
		items = append(items, adminAttentionItem{
			Key: "dependencies", Severity: "critical", Title: "Нет связи с зависимостями",
			Detail: strings.Join(criticalDependencies, ", "), Count: len(criticalDependencies), Href: "#dependencies", Action: "Проверить связь",
		})
	}
	if len(staleDependencies) > 0 {
		items = append(items, adminAttentionItem{
			Key: "stale-dependencies", Severity: "warning", Title: "Данные зависимостей устарели",
			Detail: strings.Join(staleDependencies, ", "), Count: len(staleDependencies), Href: "#dependencies", Action: "Посмотреть",
		})
	}
	comfyUnavailable := dependencyUnavailable(dependencies, dependencyComfyUI)
	if view.Queue.Error != "" && !comfyUnavailable {
		items = append(items, adminAttentionItem{
			Key: "queue", Severity: "critical", Title: "Очередь ComfyUI не читается",
			Detail: trimOperationsDetail(view.Queue.Error, 200), Count: 1, Href: "#active-jobs", Action: "К очереди",
		})
	}
	if view.Compatibility.State == "critical" && !comfyUnavailable {
		items = append(items, adminAttentionItem{
			Key: "workflow-compatibility", Severity: "critical", Title: "Workflow требуют проверки",
			Detail: view.Compatibility.Detail, Count: maxInt(1, view.Compatibility.Failed), Href: "/admin/workflows", Action: "Открыть матрицу",
		})
	} else if view.Compatibility.State == "warning" {
		items = append(items, adminAttentionItem{
			Key: "workflow-compatibility", Severity: "warning", Title: view.Compatibility.StateLabel,
			Detail: view.Compatibility.Detail, Count: maxInt(1, view.Compatibility.Unavailable), Href: "/admin/workflows", Action: "Открыть матрицу",
		})
	}
	workerIssues := make([]MaintenanceWorkerState, 0, len(view.ProblemWorkers))
	for _, worker := range view.ProblemWorkers {
		if maintenanceWorkerHasProblem(worker) {
			workerIssues = append(workerIssues, worker)
		}
	}
	if len(workerIssues) > 0 {
		detail := workerIssues[0].Name
		if workerIssues[0].LastError != "" {
			detail += ": " + trimOperationsDetail(workerIssues[0].LastError, 170)
		}
		items = append(items, adminAttentionItem{
			Key: "workers", Severity: "warning", Title: "Фоновые задачи повторяются после ошибки",
			Detail: detail, Count: len(workerIssues), Href: "#maintenance", Action: "К процессам",
		})
	}
	moderationBacklog := view.Gateway.Latest.ContentModerationBacklog + view.Gateway.Latest.MediaModerationBacklog
	if moderationBacklog > 0 {
		items = append(items, adminAttentionItem{
			Key: "moderation", Severity: "warning", Title: "Контент ожидает проверки",
			Detail: fmt.Sprintf("Текст: %d; медиа: %d", view.Gateway.Latest.ContentModerationBacklog, view.Gateway.Latest.MediaModerationBacklog),
			Count:  int(moderationBacklog), Href: "/admin/content", Action: "Открыть контент",
		})
	}
	if view.Storage.Cleanup.Status == "error" || view.Storage.Cleanup.Status == "partial" {
		severity := "warning"
		if view.Storage.Cleanup.Status == "error" {
			severity = "critical"
		}
		items = append(items, adminAttentionItem{
			Key: "cleanup", Severity: severity, Title: view.Storage.Cleanup.StatusLabel,
			Detail: "Проверьте таблицы, которые не удалось очистить.", Count: maxInt(1, len(view.Storage.Cleanup.Items)), Href: "/admin/storage", Action: "К хранилищу",
		})
	}
	if view.Storage.UnmappedCount > 0 {
		items = append(items, adminAttentionItem{
			Key: "storage-policy", Severity: "warning", Title: "Не для всех таблиц задан срок хранения",
			Detail: "Назначьте владельца и политику хранения новым таблицам.", Count: view.Storage.UnmappedCount, Href: "/admin/storage", Action: "Проверить таблицы",
		})
	}
	sort.SliceStable(items, func(left, right int) bool {
		leftRank, rightRank := attentionSeverityRank(items[left].Severity), attentionSeverityRank(items[right].Severity)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return items[left].Key < items[right].Key
	})
	return items
}

func dependencyProblems(dependencies []DependencyStatus) (critical, stale []string) {
	for _, dependency := range dependencies {
		switch dependency.State {
		case DependencyOffline, DependencyMisconfigured:
			critical = append(critical, dependency.Name)
		case DependencyStale:
			stale = append(stale, dependency.Name)
		}
	}
	return critical, stale
}

func dependencyUnavailable(dependencies []DependencyStatus, key string) bool {
	status := dependencyStatusByKey(dependencies, key)
	return status.State == DependencyOffline || status.State == DependencyMisconfigured
}

func dependencyStatusByKey(dependencies []DependencyStatus, key string) DependencyStatus {
	for _, dependency := range dependencies {
		if dependency.Key == key {
			return dependency
		}
	}
	return DependencyStatus{Key: key, Name: observabilityComponentLabel(key), State: DependencyMisconfigured, StateLabel: "Не настроено", Detail: "Состояние ещё не получено."}
}

func maintenanceWorkerHasProblem(worker MaintenanceWorkerState) bool {
	return worker.ConsecutiveFailures > 0 || worker.Status == "retrying" || worker.Status == "stopped"
}

func maintenanceWorkerIsForeground(worker MaintenanceWorkerState) bool {
	return worker.Running || maintenanceWorkerHasProblem(worker)
}

func attentionSeverityRank(severity string) int {
	if severity == "critical" {
		return 0
	}
	return 1
}

func trimOperationsDetail(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
