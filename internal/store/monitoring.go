package store

import (
	"context"
	"time"

	"ai-access-gateway/internal/domain"
)

func (s *Store) DatabaseSize(ctx context.Context) (int64, error) {
	var size int64
	err := s.db.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&size)
	return size, err
}

func (s *Store) OnlineUsers(ctx context.Context, limit int) ([]domain.OnlineUser, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.username, u.role, MAX(s.last_seen_at), MAX(s.ip)
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.expires_at > now() AND s.last_seen_at >= now() - interval '5 minutes'
		GROUP BY u.id, u.username, u.role
		ORDER BY MAX(s.last_seen_at) DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []domain.OnlineUser
	for rows.Next() {
		var user domain.OnlineUser
		if err := rows.Scan(&user.Username, &user.Role, &user.LastSeenAt, &user.IP); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) RecordHostMetric(ctx context.Context, metric domain.HostMetric) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO host_metrics (recorded_at, cpu_percent, memory_used_bytes, memory_total_bytes, gpu_available, gpu_name, gpu_percent, gpu_memory_used_bytes, gpu_memory_total_bytes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, metric.RecordedAt, metric.CPUPercent, metric.MemoryUsedBytes, metric.MemoryTotalBytes, metric.GPUAvailable, metric.GPUName, metric.GPUPercent, metric.GPUMemoryUsedBytes, metric.GPUMemoryTotalBytes)
	return err
}

func (s *Store) HostMetrics(ctx context.Context, since time.Time) ([]domain.HostMetric, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT recorded_at, cpu_percent, memory_used_bytes, memory_total_bytes, gpu_available, gpu_name, gpu_percent, gpu_memory_used_bytes, gpu_memory_total_bytes
		FROM host_metrics WHERE recorded_at >= $1 ORDER BY recorded_at ASC
	`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var metrics []domain.HostMetric
	for rows.Next() {
		var metric domain.HostMetric
		if err := rows.Scan(&metric.RecordedAt, &metric.CPUPercent, &metric.MemoryUsedBytes, &metric.MemoryTotalBytes, &metric.GPUAvailable, &metric.GPUName, &metric.GPUPercent, &metric.GPUMemoryUsedBytes, &metric.GPUMemoryTotalBytes); err != nil {
			return nil, err
		}
		metrics = append(metrics, metric)
	}
	return metrics, rows.Err()
}

func (s *Store) DeleteHostMetricsBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM host_metrics WHERE recorded_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
