package gateway

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestAdminDashboardRendersMaintenanceWorkerState(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	worker := MaintenanceWorkerState{
		Key: "generation_jobs", Name: "Задания генераций", Status: "retrying", StatusLabel: "Повтор после ошибки",
		Interval: 30 * time.Second, Timeout: 20 * time.Second, LastSuccessAt: &now, NextRunAt: &now,
		LastDurationMillis: 842, LastItems: 3, ConsecutiveFailures: 1, LastError: "ComfyUI временно недоступен",
	}
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "admin_dashboard", map[string]any{
		"Title": "Обзор системы", "Stats": domain.AdminStats{}, "System": SystemOverview{Workers: []MaintenanceWorkerState{worker}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-worker-key="generation_jobs"`, "Обслуживание Gateway", "Каждые 30 сек. · таймаут 20 сек.",
		"Повтор после ошибки", "ComfyUI временно недоступен",
	} {
		if !strings.Contains(rendered.String(), expected) {
			t.Fatalf("admin dashboard does not contain %q", expected)
		}
	}
}
