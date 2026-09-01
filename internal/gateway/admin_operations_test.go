package gateway

import (
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestAdminOperationsPrioritizesActionableFailures(t *testing.T) {
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	userID := int64(7)
	worker := MaintenanceWorkerState{Key: "host_metrics", Name: "Метрики Windows", Status: "retrying", StatusLabel: "Повтор после ошибки", ConsecutiveFailures: 2, LastError: "agent unavailable"}
	dependencies := []DependencyStatus{
		{Key: dependencyComfyUI, Name: "ComfyUI", State: DependencyOffline, StateLabel: "Нет связи"},
		{Key: dependencySystemMonitor, Name: "Мониторинг Windows", State: DependencyStale, StateLabel: "Данные устарели"},
	}
	job := domain.GenerationJob{
		ID: 41, PublicID: "job-active", UserID: &userID, UsernameSnapshot: "rayka", WorkflowID: "minimax-h3-video-v4", ModelName: "MiniMax H3",
		State: domain.GenerationJobRunning, StatusMessage: "ComfyUI выполняет workflow", CreatedAt: now.Add(-90 * time.Minute), StateChangedAt: now.Add(-60 * time.Minute),
	}
	observability := adminObservabilityView{
		Generation:   domain.GenerationObservabilitySummary{ActiveJobs: 1, OverdueJobs: 1},
		Gateway:      domain.GatewayObservationSummary{Latest: domain.GatewayObservation{MediaModerationBacklog: 2}},
		Leases:       []domain.QuickGenerationMiningLease{{GenerationJobID: 41}},
		OverdueAfter: 45 * time.Minute,
	}
	storage := databaseStorageView{
		UnmappedCount: 1,
		Cleanup:       databaseCleanupView{Status: "error", StatusLabel: "Очистка не выполнена", Items: []databaseCleanupItemView{{Table: "content_media", Error: "locked"}}},
	}
	view := newAdminOperationsView(
		now, observability, []domain.GenerationJob{job},
		adminQueueStatusView{State: "offline", StateLabel: "Недоступна", Error: "connection refused"},
		workflowCompatibilityReport{Error: "object_info unavailable"}, storage,
		SystemOverview{Dependencies: dependencies, Workers: []MaintenanceWorkerState{worker}},
	)

	if view.OverallState != "critical" || view.CriticalCount < 2 {
		t.Fatalf("unexpected overall state: %#v", view)
	}
	keys := make(map[string]bool, len(view.Attention))
	for _, item := range view.Attention {
		keys[item.Key] = true
	}
	for _, key := range []string{"overdue-jobs", "dependencies", "stale-dependencies", "workers", "moderation", "cleanup", "storage-policy"} {
		if !keys[key] {
			t.Fatalf("missing attention item %q in %#v", key, view.Attention)
		}
	}
	if keys["queue"] || keys["workflow-compatibility"] {
		t.Fatalf("ComfyUI outage must not create duplicate queue or workflow alerts: %#v", view.Attention)
	}
	if len(view.ActiveJobs) != 1 || !view.ActiveJobs[0].Overdue || !view.ActiveJobs[0].HasLease {
		t.Fatalf("active job context was not preserved: %#v", view.ActiveJobs)
	}
}

func TestAdminOperationsHealthyStateHasNoAttentionQueue(t *testing.T) {
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	view := newAdminOperationsView(
		now,
		adminObservabilityView{OverdueAfter: 45 * time.Minute},
		nil,
		adminQueueStatusView{Available: true, State: "idle", StateLabel: "Свободна"},
		workflowCompatibilityReport{Compatible: 4},
		databaseStorageView{Cleanup: databaseCleanupView{Status: "ok", StatusLabel: "Очистка завершена"}},
		SystemOverview{Dependencies: []DependencyStatus{{Key: dependencyComfyUI, Name: "ComfyUI", State: DependencyOnline}}},
	)
	if view.OverallState != "healthy" || len(view.Attention) != 0 {
		t.Fatalf("expected a clean healthy state, got %#v", view)
	}
}

func TestAdminOperationsShowsRunningWorkerWithoutFalseWarning(t *testing.T) {
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	worker := MaintenanceWorkerState{Key: "cleanup", Name: "Очистка", Running: true, Status: "healthy", StatusLabel: "Выполняется"}
	view := newAdminOperationsView(
		now,
		adminObservabilityView{OverdueAfter: 45 * time.Minute},
		nil,
		adminQueueStatusView{Available: true, State: "idle", StateLabel: "Свободна"},
		workflowCompatibilityReport{Compatible: 4},
		databaseStorageView{Cleanup: databaseCleanupView{Status: "ok", StatusLabel: "Очистка завершена"}},
		SystemOverview{Dependencies: []DependencyStatus{{Key: dependencyComfyUI, Name: "ComfyUI", State: DependencyOnline}}, Workers: []MaintenanceWorkerState{worker}},
	)
	if len(view.ProblemWorkers) != 1 || view.ProblemWorkers[0].Key != worker.Key {
		t.Fatalf("running worker must stay visible: %#v", view.ProblemWorkers)
	}
	for _, item := range view.Attention {
		if item.Key == "workers" {
			t.Fatalf("running healthy worker must not create an alert: %#v", view.Attention)
		}
	}
}
