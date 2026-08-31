package gateway

import (
	"context"
	"encoding/csv"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/config"
	"ai-access-gateway/internal/domain"
)

type databaseTablePolicy struct {
	Label         string
	Owner         string
	Retention     func(config.RetentionPolicy) string
	Configuration string
	Managed       bool
}

type databaseTableView struct {
	Name          string
	Label         string
	Owner         string
	Retention     string
	Configuration string
	Managed       bool
	Unmapped      bool
	EstimatedRows int64
	TotalBytes    int64
	OldestAt      *time.Time
}

type databaseCleanupItemView struct {
	Table   string
	Label   string
	Deleted int64
	Error   string
}

type databaseCleanupView struct {
	Status         string
	StatusLabel    string
	LastStartedAt  *time.Time
	LastFinishedAt *time.Time
	LastSuccessAt  *time.Time
	DurationMS     int64
	TotalDeleted   int64
	Items          []databaseCleanupItemView
}

type databaseStorageView struct {
	DatabaseBytes     int64
	VisibleTableBytes int64
	EstimatedRows     int64
	ManagedTables     []databaseTableView
	LifecycleTables   []databaseTableView
	UnmappedCount     int
	Cleanup           databaseCleanupView
}

func (a *App) handleAdminStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	view, err := a.databaseStorageView(r.Context())
	if err != nil {
		http.Error(w, "не удалось получить состояние хранилища", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "admin_storage", map[string]any{
		"Title":   "Хранилище базы данных",
		"Storage": view,
	})
}

func (a *App) databaseStorageView(ctx context.Context) (databaseStorageView, error) {
	databaseBytes, err := a.store.DatabaseSize(ctx)
	if err != nil {
		return databaseStorageView{}, err
	}
	stats, err := a.store.DatabaseTableStats(ctx)
	if err != nil {
		return databaseStorageView{}, err
	}
	cleanupState, err := a.store.DatabaseCleanupState(ctx)
	if err != nil {
		return databaseStorageView{}, err
	}
	policy := a.retentionPolicy()
	policies := databaseTablePolicies()
	view := databaseStorageView{DatabaseBytes: databaseBytes}
	for _, stat := range stats {
		row := databaseTableView{
			Name: stat.Name, Label: stat.Name, Owner: "Не назначен", Retention: "Политика не задана",
			Unmapped: true, EstimatedRows: stat.EstimatedRows, TotalBytes: stat.TotalBytes, OldestAt: stat.OldestAt,
		}
		if tablePolicy, ok := policies[stat.Name]; ok {
			row.Label = tablePolicy.Label
			row.Owner = tablePolicy.Owner
			row.Configuration = tablePolicy.Configuration
			row.Managed = tablePolicy.Managed
			row.Unmapped = false
			row.Retention = tablePolicy.Retention(policy)
		}
		view.VisibleTableBytes += row.TotalBytes
		view.EstimatedRows += row.EstimatedRows
		if row.Unmapped {
			view.UnmappedCount++
		}
		if row.Managed || row.Unmapped {
			view.ManagedTables = append(view.ManagedTables, row)
		} else {
			view.LifecycleTables = append(view.LifecycleTables, row)
		}
	}
	view.Cleanup = newDatabaseCleanupView(cleanupState, policies)
	return view, nil
}

func newDatabaseCleanupView(state domain.DatabaseCleanupState, policies map[string]databaseTablePolicy) databaseCleanupView {
	view := databaseCleanupView{
		Status: state.Status, LastStartedAt: state.LastStartedAt, LastFinishedAt: state.LastFinishedAt,
		LastSuccessAt: state.LastSuccessAt, DurationMS: state.DurationMS,
	}
	switch state.Status {
	case "ok":
		view.StatusLabel = "Очистка завершена"
	case "partial":
		view.StatusLabel = "Очистка завершена частично"
	case "error":
		view.StatusLabel = "Очистка не выполнена"
	default:
		view.Status = "never"
		view.StatusLabel = "Очистка ещё не запускалась"
	}
	seen := make(map[string]struct{}, len(state.DeletedRows)+len(state.Errors))
	for table, deleted := range state.DeletedRows {
		seen[table] = struct{}{}
		view.TotalDeleted += deleted
		if deleted == 0 && state.Errors[table] == "" {
			continue
		}
		view.Items = append(view.Items, cleanupItemView(table, deleted, state.Errors[table], policies))
	}
	for table, message := range state.Errors {
		if _, ok := seen[table]; ok {
			continue
		}
		view.Items = append(view.Items, cleanupItemView(table, 0, message, policies))
	}
	sortCleanupItems(view.Items)
	return view
}

func cleanupItemView(table string, deleted int64, message string, policies map[string]databaseTablePolicy) databaseCleanupItemView {
	label := table
	if policy, ok := policies[table]; ok {
		label = policy.Label
	}
	return databaseCleanupItemView{Table: table, Label: label, Deleted: deleted, Error: message}
}

func sortCleanupItems(items []databaseCleanupItemView) {
	sort.SliceStable(items, func(i, j int) bool {
		leftError := items[i].Error != ""
		rightError := items[j].Error != ""
		if leftError != rightError {
			return leftError
		}
		return items[i].Label < items[j].Label
	})
}

func databaseTablePolicies() map[string]databaseTablePolicy {
	duration := func(value func(config.RetentionPolicy) time.Duration) func(config.RetentionPolicy) string {
		return func(policy config.RetentionPolicy) string { return retentionDurationLabel(value(policy)) }
	}
	fixed := func(label string) func(config.RetentionPolicy) string {
		return func(config.RetentionPolicy) string { return label }
	}
	return map[string]databaseTablePolicy{
		"proxy_requests":                   {Label: "Запросы к сервисам", Owner: "Телеметрия Gateway", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.ProxyRequests }), Configuration: "PROXY_REQUEST_RETENTION", Managed: true},
		"websocket_sessions":               {Label: "История WebSocket", Owner: "Телеметрия Gateway", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.WebSocketSessions }), Configuration: "WEBSOCKET_SESSION_RETENTION", Managed: true},
		"generation_requests":              {Label: "Восстановление запусков", Owner: "Быстрая генерация", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.GenerationRequests }), Configuration: "GENERATION_REQUEST_RETENTION", Managed: true},
		"quick_generation_daily_usage":     {Label: "Суточные лимиты", Owner: "Квоты генерации", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.DailyUsage }), Configuration: "DAILY_USAGE_RETENTION", Managed: true},
		"invites":                          {Label: "Завершённые приглашения", Owner: "Управление доступом", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.InviteHistory }), Configuration: "INVITE_HISTORY_RETENTION", Managed: true},
		"invite_uses":                      {Label: "Активации приглашений", Owner: "Управление доступом", Retention: fixed("Вместе с приглашением"), Managed: true},
		"audit_log":                        {Label: "Журнал аудита", Owner: "Безопасность", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.AuditLog }), Configuration: "AUDIT_LOG_RETENTION", Managed: true},
		"host_metrics":                     {Label: "Метрики компьютера", Owner: "Мониторинг", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.HostMetrics }), Configuration: "HOST_METRIC_RETENTION", Managed: true},
		"quick_generation_variants":        {Label: "История генераций", Owner: "Быстрая генерация", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.GenerationHistory }), Configuration: "GENERATION_RETENTION", Managed: true},
		"content_events":                   {Label: "AI-контент", Owner: "Контроль контента", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.AIContent }), Configuration: "AI_CONTENT_RETENTION", Managed: true},
		"content_media":                    {Label: "Медиа генераций", Owner: "Контроль контента", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.GenerationMedia }), Configuration: "GENERATION_RETENTION", Managed: true},
		"comfy_output_ownership":           {Label: "Связи результатов ComfyUI", Owner: "Изоляция файлов", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.GenerationMedia }), Configuration: "GENERATION_RETENTION", Managed: true},
		"comfy_input_assets":               {Label: "Входные референсы", Owner: "Изоляция файлов", Retention: duration(func(p config.RetentionPolicy) time.Duration { return p.ComfyInputs }), Configuration: "COMFY_INPUT_RETENTION", Managed: true},
		"sessions":                         {Label: "Сессии входа", Owner: "Аутентификация", Retention: fixed("До истечения или простоя"), Managed: true},
		"users":                            {Label: "Учётные записи", Owner: "Управление доступом", Retention: fixed("До удаления; временные — до срока")},
		"comfy_settings":                   {Label: "Настройки ComfyUI", Owner: "Профиль пользователя", Retention: fixed("До удаления аккаунта")},
		"comfy_userdata":                   {Label: "Пользовательские данные ComfyUI", Owner: "Профиль пользователя", Retention: fixed("До удаления аккаунта")},
		"miners":                           {Label: "Профили майнеров", Owner: "Администратор", Retention: fixed("До ручного удаления")},
		"quick_generation_mining_leases":   {Label: "Аренды ресурсов", Owner: "Координатор майнинга", Retention: fixed("До завершения или восстановления")},
		"quick_generation_recipes":         {Label: "Сохранённые рецепты", Owner: "Пользователь", Retention: fixed("До ручного удаления")},
		"quick_generation_access_policies": {Label: "Ограничения моделей", Owner: "Управление доступом", Retention: fixed("До удаления аккаунта")},
		"feature_suggestions":              {Label: "Предложения пользователей", Owner: "Администратор", Retention: fixed("До ручного решения")},
		"feature_suggestion_scans":         {Label: "Проверки предложений", Owner: "VirusTotal", Retention: fixed("Вместе с предложением")},
		"comfy_output_cleanup_tombstones":  {Label: "Очередь удаления результатов", Owner: "Очистка файлов", Retention: fixed("До подтверждения агента")},
		"content_change_revision":          {Label: "Ревизия AI-контента", Owner: "Система", Retention: fixed("Одна служебная запись")},
		"database_cleanup_state":           {Label: "Состояние очистки БД", Owner: "Система", Retention: fixed("Одна служебная запись")},
		"schema_migrations":                {Label: "История схемы", Owner: "Система", Retention: fixed("Постоянно")},
	}
}

func (a *App) handleAdminAuditExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	before := time.Now().UTC()
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "некорректная граница выгрузки", http.StatusBadRequest)
			return
		}
		before = parsed.UTC()
	}
	filename := "ai-gateway-audit-" + before.Format("20060102-150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte{0xef, 0xbb, 0xbf})
	writer := csv.NewWriter(w)
	if err := writer.Write([]string{"ID", "Время", "Инициатор", "Действие", "Тип объекта", "ID объекта", "IP", "User-Agent", "Метаданные"}); err != nil {
		return
	}
	err := a.store.VisitAuditBefore(r.Context(), before, func(row domain.AuditRow) error {
		targetID := ""
		if row.TargetID.Valid {
			targetID = strconv.FormatInt(row.TargetID.Int64, 10)
		}
		actor := row.Actor
		if actor == "" {
			actor = "система"
		}
		return writer.Write([]string{
			strconv.FormatInt(row.ID, 10), row.CreatedAt.UTC().Format(time.RFC3339), actor,
			row.Action, row.TargetType, targetID, row.IP, row.UserAgent, row.Metadata,
		})
	})
	writer.Flush()
	if err == nil {
		err = writer.Error()
	}
	if err != nil {
		log.Printf("export audit CSV: %v", err)
	}
}
