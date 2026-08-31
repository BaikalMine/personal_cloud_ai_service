package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"ai-access-gateway/internal/domain"
)

const (
	minimumCleanupBatchSize = 100
	maximumCleanupBatchSize = 10000
	maximumCleanupPasses    = 100
)

type retentionDeleteTask struct {
	name   string
	before time.Time
	query  string
}

func (s *Store) CleanupDatabaseRetention(ctx context.Context, cutoffs domain.DatabaseRetentionCutoffs, batchSize, maxBatches int) (domain.DatabaseCleanupReport, error) {
	if batchSize < minimumCleanupBatchSize {
		batchSize = minimumCleanupBatchSize
	}
	if batchSize > maximumCleanupBatchSize {
		batchSize = maximumCleanupBatchSize
	}
	if maxBatches < 1 {
		maxBatches = 1
	}
	if maxBatches > maximumCleanupPasses {
		maxBatches = maximumCleanupPasses
	}
	report := domain.DatabaseCleanupReport{
		StartedAt:   time.Now().UTC(),
		Status:      "ok",
		DeletedRows: make(map[string]int64),
		Errors:      make(map[string]string),
	}
	tasks := []retentionDeleteTask{
		{name: "proxy_requests", before: cutoffs.ProxyRequests, query: `
			WITH doomed AS (
				SELECT id FROM proxy_requests WHERE created_at < $1
				ORDER BY created_at,id LIMIT $2::integer FOR UPDATE SKIP LOCKED
			)
			DELETE FROM proxy_requests item USING doomed WHERE item.id=doomed.id`},
		{name: "websocket_sessions", before: cutoffs.WebSocketSessions, query: `
			WITH doomed AS (
				SELECT id FROM websocket_sessions WHERE closed_at IS NOT NULL AND closed_at < $1
				ORDER BY closed_at,id LIMIT $2::integer FOR UPDATE SKIP LOCKED
			)
			DELETE FROM websocket_sessions item USING doomed WHERE item.id=doomed.id`},
		{name: "generation_requests", before: cutoffs.GenerationRequests, query: `
			WITH doomed AS (
				SELECT user_id,request_id FROM generation_requests WHERE updated_at < $1
				ORDER BY updated_at,user_id,request_id LIMIT $2::integer FOR UPDATE SKIP LOCKED
			)
			DELETE FROM generation_requests item USING doomed
			WHERE item.user_id=doomed.user_id AND item.request_id=doomed.request_id`},
		{name: "quick_generation_daily_usage", before: cutoffs.DailyUsage, query: `
			WITH doomed AS (
				SELECT user_id,usage_date FROM quick_generation_daily_usage WHERE usage_date < $1::date
				ORDER BY usage_date,user_id LIMIT $2::integer FOR UPDATE SKIP LOCKED
			)
			DELETE FROM quick_generation_daily_usage item USING doomed
			WHERE item.user_id=doomed.user_id AND item.usage_date=doomed.usage_date`},
		{name: "invites", before: cutoffs.InviteHistory, query: `
			WITH doomed AS (
				SELECT id FROM invites WHERE expires_at < $1
				ORDER BY expires_at,id LIMIT $2::integer FOR UPDATE SKIP LOCKED
			)
			DELETE FROM invites item USING doomed WHERE item.id=doomed.id`},
		{name: "audit_log", before: cutoffs.AuditLog, query: `
			WITH doomed AS (
				SELECT id FROM audit_log WHERE created_at < $1
				ORDER BY created_at,id LIMIT $2::integer FOR UPDATE SKIP LOCKED
			)
			DELETE FROM audit_log item USING doomed WHERE item.id=doomed.id`},
		{name: "host_metrics", before: cutoffs.HostMetrics, query: `
			WITH doomed AS (
				SELECT id FROM host_metrics WHERE recorded_at < $1
				ORDER BY recorded_at,id LIMIT $2::integer FOR UPDATE SKIP LOCKED
			)
			DELETE FROM host_metrics item USING doomed WHERE item.id=doomed.id`},
		{name: "quick_generation_variants", before: cutoffs.GenerationVariants, query: `
			WITH doomed AS (
				SELECT id FROM quick_generation_variants
				WHERE state NOT IN ('queued','running') AND COALESCE(finished_at,created_at) < $1
				ORDER BY COALESCE(finished_at,created_at),id LIMIT $2::integer FOR UPDATE SKIP LOCKED
			)
			DELETE FROM quick_generation_variants item USING doomed WHERE item.id=doomed.id`},
		{name: "comfy_output_ownership", before: cutoffs.OutputOwnerships, query: `
			WITH doomed AS (
				SELECT item.id FROM comfy_output_ownership item
				WHERE item.expires_at < $1
				  AND NOT EXISTS (
					SELECT 1 FROM content_media media
					WHERE media.event_id=item.event_id AND media.original_name=item.filename
					  AND media.subfolder=item.subfolder AND media.storage_type=item.storage_type
				  )
				ORDER BY item.expires_at,item.id LIMIT $2::integer FOR UPDATE SKIP LOCKED
			)
			DELETE FROM comfy_output_ownership item USING doomed WHERE item.id=doomed.id`},
	}

	var cleanupErrors []error
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			report.Errors[task.name] = err.Error()
			cleanupErrors = append(cleanupErrors, fmt.Errorf("%s: %w", task.name, err))
			continue
		}
		deleted, err := s.deleteRetentionBatches(ctx, task, batchSize, maxBatches)
		report.DeletedRows[task.name] = deleted
		if err != nil {
			report.Errors[task.name] = err.Error()
			cleanupErrors = append(cleanupErrors, fmt.Errorf("%s: %w", task.name, err))
		}
	}
	report.FinishedAt = time.Now().UTC()
	report.DurationMS = report.FinishedAt.Sub(report.StartedAt).Milliseconds()
	if len(cleanupErrors) > 0 {
		report.Status = "partial"
		if len(cleanupErrors) == len(tasks) {
			report.Status = "error"
		}
	}
	return report, errors.Join(cleanupErrors...)
}

func (s *Store) deleteRetentionBatches(ctx context.Context, task retentionDeleteTask, batchSize, maxBatches int) (int64, error) {
	var total int64
	for batch := 0; batch < maxBatches; batch++ {
		result, err := s.db.ExecContext(ctx, task.query, task.before.UTC(), batchSize)
		if err != nil {
			return total, err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted < int64(batchSize) {
			break
		}
	}
	return total, nil
}

func (s *Store) SaveDatabaseCleanupState(ctx context.Context, report domain.DatabaseCleanupReport) error {
	deletedRows, err := json.Marshal(report.DeletedRows)
	if err != nil {
		return err
	}
	errorsJSON, err := json.Marshal(report.Errors)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO database_cleanup_state
			(id,last_started_at,last_finished_at,last_success_at,status,deleted_rows,errors,duration_ms,updated_at)
		VALUES (1,$1::timestamptz,$2::timestamptz,CASE WHEN $3='ok' THEN $2::timestamptz ELSE NULL END,$3,$4,$5,$6,now())
		ON CONFLICT (id) DO UPDATE SET
			last_started_at=EXCLUDED.last_started_at,
			last_finished_at=EXCLUDED.last_finished_at,
			last_success_at=CASE WHEN EXCLUDED.status='ok' THEN EXCLUDED.last_finished_at ELSE database_cleanup_state.last_success_at END,
			status=EXCLUDED.status,
			deleted_rows=EXCLUDED.deleted_rows,
			errors=EXCLUDED.errors,
			duration_ms=EXCLUDED.duration_ms,
			updated_at=now()
	`, report.StartedAt.UTC(), report.FinishedAt.UTC(), report.Status, deletedRows, errorsJSON, report.DurationMS)
	return err
}

func (s *Store) DatabaseCleanupState(ctx context.Context) (domain.DatabaseCleanupState, error) {
	var state domain.DatabaseCleanupState
	var startedAt, finishedAt, successAt sql.NullTime
	var deletedRows, errorsJSON []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT last_started_at,last_finished_at,last_success_at,status,deleted_rows,errors,duration_ms
		FROM database_cleanup_state WHERE id=1
	`).Scan(&startedAt, &finishedAt, &successAt, &state.Status, &deletedRows, &errorsJSON, &state.DurationMS)
	if err != nil {
		return domain.DatabaseCleanupState{}, err
	}
	if startedAt.Valid {
		value := startedAt.Time
		state.LastStartedAt = &value
	}
	if finishedAt.Valid {
		value := finishedAt.Time
		state.LastFinishedAt = &value
	}
	if successAt.Valid {
		value := successAt.Time
		state.LastSuccessAt = &value
	}
	if err := json.Unmarshal(deletedRows, &state.DeletedRows); err != nil {
		return domain.DatabaseCleanupState{}, err
	}
	if err := json.Unmarshal(errorsJSON, &state.Errors); err != nil {
		return domain.DatabaseCleanupState{}, err
	}
	return state, nil
}

func (s *Store) DatabaseTableStats(ctx context.Context) ([]domain.DatabaseTableStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT relname,COALESCE(n_live_tup,0)::bigint,pg_total_relation_size(relid)
		FROM pg_stat_user_tables
		WHERE schemaname=current_schema()
		ORDER BY pg_total_relation_size(relid) DESC,relname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := make([]domain.DatabaseTableStat, 0)
	for rows.Next() {
		var item domain.DatabaseTableStat
		if err := rows.Scan(&item.Name, &item.EstimatedRows, &item.TotalBytes); err != nil {
			return nil, err
		}
		stats = append(stats, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	oldestQueries := databaseOldestQueries()
	for index := range stats {
		query, ok := oldestQueries[stats[index].Name]
		if !ok {
			continue
		}
		var oldest sql.NullTime
		if err := s.db.QueryRowContext(ctx, query).Scan(&oldest); err != nil {
			return nil, fmt.Errorf("oldest row for %s: %w", stats[index].Name, err)
		}
		if oldest.Valid {
			value := oldest.Time
			stats[index].OldestAt = &value
		}
	}
	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].TotalBytes == stats[j].TotalBytes {
			return stats[i].Name < stats[j].Name
		}
		return stats[i].TotalBytes > stats[j].TotalBytes
	})
	return stats, nil
}

func databaseOldestQueries() map[string]string {
	return map[string]string{
		"users":                            `SELECT MIN(created_at) FROM users`,
		"sessions":                         `SELECT MIN(created_at) FROM sessions`,
		"invites":                          `SELECT MIN(created_at) FROM invites`,
		"invite_uses":                      `SELECT MIN(used_at) FROM invite_uses`,
		"proxy_requests":                   `SELECT MIN(created_at) FROM proxy_requests`,
		"websocket_sessions":               `SELECT MIN(opened_at) FROM websocket_sessions`,
		"audit_log":                        `SELECT MIN(created_at) FROM audit_log`,
		"content_events":                   `SELECT MIN(created_at) FROM content_events`,
		"content_media":                    `SELECT MIN(created_at) FROM content_media`,
		"comfy_output_ownership":           `SELECT MIN(created_at) FROM comfy_output_ownership`,
		"comfy_settings":                   `SELECT MIN(updated_at) FROM comfy_settings`,
		"comfy_userdata":                   `SELECT MIN(created_at) FROM comfy_userdata`,
		"miners":                           `SELECT MIN(created_at) FROM miners`,
		"quick_generation_daily_usage":     `SELECT MIN(usage_date)::timestamptz FROM quick_generation_daily_usage`,
		"quick_generation_mining_leases":   `SELECT MIN(created_at) FROM quick_generation_mining_leases`,
		"host_metrics":                     `SELECT MIN(recorded_at) FROM host_metrics`,
		"generation_requests":              `SELECT MIN(created_at) FROM generation_requests`,
		"quick_generation_recipes":         `SELECT MIN(created_at) FROM quick_generation_recipes`,
		"quick_generation_variants":        `SELECT MIN(created_at) FROM quick_generation_variants`,
		"quick_generation_access_policies": `SELECT MIN(updated_at) FROM quick_generation_access_policies`,
		"feature_suggestions":              `SELECT MIN(created_at) FROM feature_suggestions`,
		"feature_suggestion_scans":         `SELECT MIN(created_at) FROM feature_suggestion_scans`,
		"comfy_input_assets":               `SELECT MIN(created_at) FROM comfy_input_assets`,
		"comfy_output_cleanup_tombstones":  `SELECT MIN(created_at) FROM comfy_output_cleanup_tombstones`,
		"content_change_revision":          `SELECT MIN(changed_at) FROM content_change_revision`,
		"database_cleanup_state":           `SELECT MIN(updated_at) FROM database_cleanup_state`,
		"schema_migrations":                `SELECT MIN(applied_at) FROM schema_migrations`,
	}
}
