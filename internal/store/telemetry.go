package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ai-access-gateway/internal/domain"
)

var ErrUnknownService = errors.New("unknown service")

func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

func (s *Store) RecordProxyRequest(ctx context.Context, record domain.ProxyRequestRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO proxy_requests
			(user_id, request_id, correlation_id, generation_job_id, service, method, path, status_code, duration_ms, bytes_in, bytes_out, is_websocket, client_ip, user_agent)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`, record.UserID, record.RequestID, record.CorrelationID, record.GenerationJobID, record.Service, record.Method, record.Path, record.Status, record.DurationMS,
		record.BytesIn, record.BytesOut, record.WebSocket, record.ClientIP, record.UserAgent)
	return err
}

func (s *Store) OpenWebSocketSession(ctx context.Context, userID int64, service, ip, userAgent string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO websocket_sessions (user_id, service, client_ip, user_agent)
		VALUES ($1,$2,$3,$4) RETURNING id
	`, userID, service, ip, userAgent).Scan(&id)
	return id, err
}

func (s *Store) CloseWebSocketSession(ctx context.Context, id, durationMS int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE websocket_sessions
		SET closed_at = now(), duration_ms = $2
		WHERE id = $1 AND closed_at IS NULL
	`, id, durationMS)
	return err
}

func (s *Store) RecordAudit(ctx context.Context, event domain.AuditEvent) error {
	metadata := []byte(`{}`)
	if event.Metadata != nil {
		var err error
		metadata, err = json.Marshal(event.Metadata)
		if err != nil {
			return fmt.Errorf("encode audit metadata: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log (actor_user_id, request_id, correlation_id, generation_job_id, action, target_type, target_id, ip, user_agent, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)
	`, event.ActorUserID, event.RequestID, event.CorrelationID, event.GenerationJobID, event.Action, event.TargetType, event.TargetID, event.IP, event.UserAgent, string(metadata))
	return err
}

func (s *Store) UserStats(ctx context.Context, userID int64, days int) (domain.UserStats, error) {
	days = boundedDays(days)
	var out domain.UserStats
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(bytes_out),0), COALESCE(AVG(duration_ms),0)::bigint,
		       COUNT(*) FILTER (WHERE created_at >= date_trunc('day', now())),
		       COUNT(*) FILTER (WHERE created_at >= now() - interval '7 days'),
		       COALESCE((SELECT service FROM proxy_requests WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1),'')
		FROM proxy_requests WHERE user_id = $1
	`, userID).Scan(&out.TotalRequests, &out.TotalBytesOut, &out.AvgDuration,
		&out.TodayRequests, &out.WeekRequests, &out.LastService); err != nil {
		return domain.UserStats{}, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT service, COUNT(*), COUNT(DISTINCT user_id), COALESCE(SUM(bytes_out),0),
		       COUNT(*) FILTER (WHERE status_code >= 400)
		FROM proxy_requests WHERE user_id = $1 GROUP BY service ORDER BY service
	`, userID)
	if err != nil {
		return domain.UserStats{}, err
	}
	for rows.Next() {
		var usage domain.ServiceUsage
		if err := rows.Scan(&usage.Service, &usage.Requests, &usage.Users, &usage.Bytes, &usage.Errors); err != nil {
			rows.Close()
			return domain.UserStats{}, err
		}
		out.ByService = append(out.ByService, usage)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.UserStats{}, err
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
		SELECT to_char(d::date, 'DD.MM.YY'), COUNT(pr.id)
		FROM generate_series(date_trunc('day', now()) - ($2::int - 1) * interval '1 day', date_trunc('day', now()), interval '1 day') d
		LEFT JOIN proxy_requests pr ON pr.user_id = $1 AND pr.created_at >= d AND pr.created_at < d + interval '1 day'
		GROUP BY d ORDER BY d DESC
	`, userID, days)
	if err != nil {
		return domain.UserStats{}, err
	}
	defer rows.Close()
	maxCount := int64(1)
	for rows.Next() {
		var point domain.ChartPoint
		if err := rows.Scan(&point.Label, &point.Count); err != nil {
			return domain.UserStats{}, err
		}
		if point.Count > maxCount {
			maxCount = point.Count
		}
		out.Chart = append(out.Chart, point)
	}
	if err := rows.Err(); err != nil {
		return domain.UserStats{}, err
	}
	for i := range out.Chart {
		out.Chart[i].Percent = int(out.Chart[i].Count * 100 / maxCount)
	}
	return out, nil
}

func (s *Store) LatestActivity(ctx context.Context, userID int64, limit int) ([]domain.Activity, error) {
	limit = boundedLimit(limit, 1, 200)
	rows, err := s.db.QueryContext(ctx, `
		SELECT service, method, path, status_code, duration_ms, bytes_out, is_websocket, created_at
		FROM proxy_requests WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var activities []domain.Activity
	for rows.Next() {
		var activity domain.Activity
		if err := rows.Scan(&activity.Service, &activity.Method, &activity.Path, &activity.Status,
			&activity.Duration, &activity.Bytes, &activity.WebSocket, &activity.CreatedAt); err != nil {
			return nil, err
		}
		activities = append(activities, activity)
	}
	return activities, rows.Err()
}

func (s *Store) AdminStats(ctx context.Context) (domain.AdminStats, error) {
	var out domain.AdminStats
	var total, failures int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(DISTINCT user_id) FROM sessions WHERE last_seen_at >= now() - interval '5 minutes' AND expires_at > now()),
			(SELECT COUNT(*) FROM proxy_requests WHERE created_at >= date_trunc('day', now())),
			(SELECT COUNT(*) FROM proxy_requests WHERE created_at >= now() - interval '7 days'),
			(SELECT COUNT(*) FROM websocket_sessions WHERE closed_at IS NULL),
			(SELECT COALESCE(AVG(duration_ms),0)::bigint FROM proxy_requests WHERE created_at >= now() - interval '7 days'),
			(SELECT COUNT(*) FROM proxy_requests WHERE created_at >= now() - interval '7 days'),
			(SELECT COUNT(*) FROM proxy_requests WHERE status_code >= 400 AND created_at >= now() - interval '7 days')
	`).Scan(&out.ActiveUsers, &out.RequestsToday, &out.Requests7Days, &out.ActiveWebSockets,
		&out.AverageDuration, &total, &failures); err != nil {
		return domain.AdminStats{}, err
	}
	out.ErrorRate = "0%"
	if total > 0 {
		out.ErrorRate = fmt.Sprintf("%.1f%%", float64(failures)*100/float64(total))
	}

	var err error
	out.TopUsersRequests, err = s.topUsers(ctx, topUsersByRequests, 5)
	if err != nil {
		return domain.AdminStats{}, err
	}
	out.TopUsersTraffic, err = s.topUsers(ctx, topUsersByTraffic, 5)
	if err != nil {
		return domain.AdminStats{}, err
	}
	out.UsageByService, err = s.ServiceStats(ctx)
	if err != nil {
		return domain.AdminStats{}, err
	}
	out.Trend, err = s.AdminRequestTrend(ctx, 14)
	if err != nil {
		return domain.AdminStats{}, err
	}
	return out, nil
}

func (s *Store) AdminRequestTrend(ctx context.Context, days int) ([]domain.ChartPoint, error) {
	days = boundedDays(days)
	rows, err := s.db.QueryContext(ctx, `
		SELECT to_char(d::date, 'DD.MM.YY'), COUNT(pr.id)
		FROM generate_series(
			date_trunc('day', now()) - ($1::int - 1) * interval '1 day',
			date_trunc('day', now()), interval '1 day'
		) AS d
		LEFT JOIN proxy_requests pr ON pr.created_at >= d AND pr.created_at < d + interval '1 day'
		GROUP BY d ORDER BY d DESC
	`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	maxCount := int64(1)
	var points []domain.ChartPoint
	for rows.Next() {
		var point domain.ChartPoint
		if err := rows.Scan(&point.Label, &point.Count); err != nil {
			return nil, err
		}
		if point.Count > maxCount {
			maxCount = point.Count
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range points {
		points[i].Percent = int(points[i].Count * 100 / maxCount)
	}
	return points, nil
}

type topUsersMetric uint8

const (
	topUsersByRequests topUsersMetric = iota
	topUsersByTraffic
)

func (s *Store) topUsers(ctx context.Context, metric topUsersMetric, limit int) ([]domain.TopUser, error) {
	metricSQL := "COUNT(*)"
	if metric == topUsersByTraffic {
		metricSQL = "COALESCE(SUM(bytes_out),0)"
	}
	query := fmt.Sprintf(`
		SELECT u.username, %s AS metric
		FROM proxy_requests pr JOIN users u ON u.id = pr.user_id
		WHERE pr.created_at >= now() - interval '7 days'
		GROUP BY u.username ORDER BY metric DESC LIMIT $1
	`, metricSQL)
	rows, err := s.db.QueryContext(ctx, query, boundedLimit(limit, 1, 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []domain.TopUser
	for rows.Next() {
		var user domain.TopUser
		if err := rows.Scan(&user.Username, &user.Value); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) ServiceStats(ctx context.Context) ([]domain.ServiceUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT service, COUNT(*), COUNT(DISTINCT user_id), COALESCE(SUM(bytes_out),0),
		       COUNT(*) FILTER (WHERE status_code >= 400)
		FROM proxy_requests
		WHERE created_at >= now() - interval '30 days'
		GROUP BY service ORDER BY service
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var usage []domain.ServiceUsage
	for rows.Next() {
		var item domain.ServiceUsage
		if err := rows.Scan(&item.Service, &item.Requests, &item.Users, &item.Bytes, &item.Errors); err != nil {
			return nil, err
		}
		usage = append(usage, item)
	}
	return usage, rows.Err()
}

func (s *Store) ServiceAnalytics(ctx context.Context, service string, days int) (domain.ServiceAnalytics, error) {
	if service != "comfyui" && service != "openwebui" {
		return domain.ServiceAnalytics{}, ErrUnknownService
	}
	days = boundedDays(days)
	analytics := domain.ServiceAnalytics{Service: service}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT user_id), COALESCE(SUM(bytes_out),0),
		       COUNT(*) FILTER (WHERE status_code >= 400), COALESCE(AVG(duration_ms),0)::bigint
		FROM proxy_requests
		WHERE service = $1 AND created_at >= now() - ($2::int * interval '1 day')
	`, service, days).Scan(&analytics.Requests, &analytics.Users, &analytics.Bytes,
		&analytics.Errors, &analytics.AverageDuration); err != nil {
		return domain.ServiceAnalytics{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM websocket_sessions WHERE service = $1 AND closed_at IS NULL
	`, service).Scan(&analytics.ActiveWebSockets); err != nil {
		return domain.ServiceAnalytics{}, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT to_char(d, 'DD.MM.YY'), COUNT(pr.id), COUNT(DISTINCT pr.user_id),
		       COUNT(pr.id) FILTER (WHERE pr.status_code >= 400), COALESCE(SUM(pr.bytes_out),0)
		FROM generate_series(
			date_trunc('day', now()) - ($2::int - 1) * interval '1 day',
			date_trunc('day', now()), interval '1 day'
		) AS d
		LEFT JOIN proxy_requests pr
		  ON pr.service = $1 AND pr.created_at >= d AND pr.created_at < d + interval '1 day'
		GROUP BY d ORDER BY d DESC
	`, service, days)
	if err != nil {
		return domain.ServiceAnalytics{}, err
	}
	defer rows.Close()
	maxRequests := int64(1)
	for rows.Next() {
		var point domain.ServiceTrendPoint
		if err := rows.Scan(&point.Label, &point.Requests, &point.Users, &point.Errors, &point.Bytes); err != nil {
			return domain.ServiceAnalytics{}, err
		}
		if point.Requests > maxRequests {
			maxRequests = point.Requests
		}
		analytics.Trend = append(analytics.Trend, point)
	}
	if err := rows.Err(); err != nil {
		return domain.ServiceAnalytics{}, err
	}
	for i := range analytics.Trend {
		analytics.Trend[i].RequestPercent = int(analytics.Trend[i].Requests * 100 / maxRequests)
	}
	return analytics, nil
}

func boundedDays(days int) int {
	if days < 1 {
		return 30
	}
	if days > 366 {
		return 366
	}
	return days
}

func boundedLimit(limit, min, max int) int {
	if limit < min {
		return min
	}
	if limit > max {
		return max
	}
	return limit
}
