package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestAdminStorageRendersRetentionAndCleanupState(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	view := databaseStorageView{
		DatabaseBytes:     12 << 20,
		VisibleTableBytes: 9 << 20,
		EstimatedRows:     321,
		UnmappedCount:     1,
		ManagedTables: []databaseTableView{
			{Name: "proxy_requests", Label: "Запросы к сервисам", Owner: "Телеметрия Gateway", Retention: "90 дней", Configuration: "PROXY_REQUEST_RETENTION", Managed: true, EstimatedRows: 300, TotalBytes: 8 << 20, OldestAt: &now},
			{Name: "future_table", Label: "future_table", Owner: "Не назначен", Retention: "Политика не задана", Unmapped: true, EstimatedRows: 21, TotalBytes: 1 << 20},
		},
		LifecycleTables: []databaseTableView{{Name: "users", Label: "Учётные записи", Owner: "Управление доступом", Retention: "До удаления; временные — до срока"}},
		Cleanup: databaseCleanupView{
			Status: "partial", StatusLabel: "Очистка завершена частично", LastStartedAt: &now,
			LastSuccessAt: &now, DurationMS: 140, TotalDeleted: 7,
			Items: []databaseCleanupItemView{{Table: "proxy_requests", Label: "Запросы к сервисам", Deleted: 7}, {Table: "audit_log", Label: "Журнал аудита", Error: "timeout"}},
		},
	}
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "admin_storage", map[string]any{"Title": "Хранилище базы данных", "Storage": view}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Хранилище", "Очистка завершена частично", "PROXY_REQUEST_RETENTION", "future_table",
		"Политика не задана", "Без политики: 1", "Выгрузить аудит CSV", "Постоянные и служебные данные",
	} {
		if !strings.Contains(rendered.String(), expected) {
			t.Fatalf("storage page does not contain %q", expected)
		}
	}
}

func TestDatabaseCleanupViewSortsErrorsFirst(t *testing.T) {
	view := newDatabaseCleanupView(domain.DatabaseCleanupState{
		Status:      "partial",
		DeletedRows: map[string]int64{"audit_log": 3, "proxy_requests": 4},
		Errors:      map[string]string{"proxy_requests": "database timeout"},
	}, databaseTablePolicies())
	if view.TotalDeleted != 7 || len(view.Items) != 2 {
		t.Fatalf("cleanup view = %+v", view)
	}
	if view.Items[0].Table != "proxy_requests" || view.Items[0].Error == "" {
		t.Fatalf("cleanup errors should be first: %+v", view.Items)
	}
}

func TestGenerationJobTablesHaveLifecyclePolicies(t *testing.T) {
	policies := databaseTablePolicies()
	for _, table := range []string{"generation_jobs", "generation_job_transitions", "generation_job_revision"} {
		policy, ok := policies[table]
		if !ok || policy.Owner == "" || policy.Retention == nil {
			t.Fatalf("generation job table %q has no lifecycle policy: %+v", table, policy)
		}
	}
	if policy := policies["generation_jobs"]; !policy.Managed || policy.Configuration != "GENERATION_REQUEST_RETENTION" {
		t.Fatalf("generation_jobs retention policy = %+v", policy)
	}
}

func TestAdminAuditExportHeadAndInvalidBoundary(t *testing.T) {
	app := &App{}
	head := httptest.NewRecorder()
	app.handleAdminAuditExport(head, httptest.NewRequest(http.MethodHead, "/admin/audit/export?before=2026-08-31T12:00:00Z", nil))
	if head.Code != http.StatusOK || head.Header().Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Fatalf("audit export HEAD = status:%d headers:%v", head.Code, head.Header())
	}
	if disposition := head.Header().Get("Content-Disposition"); !strings.Contains(disposition, "ai-gateway-audit-20260831-120000.csv") {
		t.Fatalf("audit export filename = %q", disposition)
	}

	invalid := httptest.NewRecorder()
	app.handleAdminAuditExport(invalid, httptest.NewRequest(http.MethodGet, "/admin/audit/export?before=invalid", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid audit boundary status = %d", invalid.Code)
	}
}
