package store

import (
	"context"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
)

type MetricsGeneration struct {
	domain.GenerationObservabilitySummary
	QueueSamples     int64
	ExecutionSamples int64
}

type MetricsBucket struct {
	From, Until     time.Time
	Samples         int64
	ActivePeak      int64
	OverduePeak     int64
	FirstAt, LastAt *time.Time
}

func validMetricsWindow(since, until time.Time) bool {
	return !since.IsZero() && until.After(since) && until.Sub(since) <= 7*24*time.Hour
}

func (s *Store) MetricsGeneration(ctx context.Context, since, until time.Time, overdueAfter time.Duration) (MetricsGeneration, error) {
	if !validMetricsWindow(since, until) || overdueAfter <= 0 {
		return MetricsGeneration{}, errors.New("invalid metrics window")
	}
	var out MetricsGeneration
	err := s.db.QueryRowContext(ctx, `
		WITH jobs AS (
			SELECT *,
				finished_at >= $1 AND finished_at < $2 AS finished_in_period,
				started_at >= $1 AND started_at < $2 AS started_in_period,
				GREATEST(0,EXTRACT(EPOCH FROM (started_at-created_at))*1000)::bigint AS queue_ms,
				GREATEST(0,EXTRACT(EPOCH FROM (finished_at-started_at))*1000)::bigint AS execution_ms
			FROM generation_jobs
		)
		SELECT
			count(*) FILTER (WHERE state NOT IN ('completed','failed','cancelled','expired')),
			count(*) FILTER (WHERE state NOT IN ('completed','failed','cancelled','expired') AND state_changed_at < $3),
			count(*) FILTER (WHERE state='completed' AND finished_in_period),
			count(*) FILTER (WHERE state='failed' AND finished_in_period),
			count(*) FILTER (WHERE state='cancelled' AND finished_in_period),
			count(*) FILTER (WHERE state='expired' AND finished_in_period),
			COALESCE(percentile_disc(0.50) WITHIN GROUP (ORDER BY queue_ms) FILTER (WHERE started_in_period),0),
			COALESCE(percentile_disc(0.95) WITHIN GROUP (ORDER BY queue_ms) FILTER (WHERE started_in_period),0),
			COALESCE(percentile_disc(0.50) WITHIN GROUP (ORDER BY execution_ms) FILTER (WHERE started_at IS NOT NULL AND finished_in_period),0),
			COALESCE(percentile_disc(0.95) WITHIN GROUP (ORDER BY execution_ms) FILTER (WHERE started_at IS NOT NULL AND finished_in_period),0),
			count(*) FILTER (WHERE started_in_period),
			count(*) FILTER (WHERE started_at IS NOT NULL AND finished_in_period)
		FROM jobs
	`, since.UTC(), until.UTC(), until.Add(-overdueAfter).UTC()).Scan(
		&out.ActiveJobs, &out.OverdueJobs, &out.Completed, &out.Failed, &out.Cancelled, &out.Expired,
		&out.QueueP50MS, &out.QueueP95MS, &out.ExecutionP50MS, &out.ExecutionP95MS,
		&out.QueueSamples, &out.ExecutionSamples,
	)
	if n := out.Completed + out.Failed; n > 0 {
		out.SuccessRate = int((out.Completed*100 + n/2) / n)
	}
	return out, err
}

func (s *Store) MetricsGatewayHistory(ctx context.Context, since, until time.Time, step time.Duration) ([]MetricsBucket, error) {
	if !validMetricsWindow(since, until) || step < 5*time.Minute || step > until.Sub(since) || until.Sub(since)%step != 0 || until.Sub(since)/step > 168 {
		return nil, errors.New("invalid metrics buckets")
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH buckets AS (
			SELECT d AS start_at,d + $3::bigint * interval '1 second' AS end_at
			FROM generate_series($1::timestamptz,$2::timestamptz - $3::bigint * interval '1 second',$3::bigint * interval '1 second') d
		)
		SELECT b.start_at,b.end_at,count(g.id),COALESCE(max(g.active_jobs),0),COALESCE(max(g.overdue_jobs),0),min(g.recorded_at),max(g.recorded_at)
		FROM buckets b LEFT JOIN gateway_observations g ON g.recorded_at >= b.start_at AND g.recorded_at < b.end_at
		GROUP BY b.start_at,b.end_at ORDER BY b.start_at
	`, since.UTC(), until.UTC(), int64(step/time.Second))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]MetricsBucket, 0, int(until.Sub(since)/step))
	for rows.Next() {
		var item MetricsBucket
		if err := rows.Scan(&item.From, &item.Until, &item.Samples, &item.ActivePeak, &item.OverduePeak, &item.FirstAt, &item.LastAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) MetricsLatestHost(ctx context.Context, until time.Time) (domain.HostMetric, error) {
	var out domain.HostMetric
	err := s.db.QueryRowContext(ctx, `
		SELECT recorded_at,cpu_percent,memory_used_bytes,memory_total_bytes,gpu_available,gpu_name,gpu_percent,gpu_memory_used_bytes,gpu_memory_total_bytes
		FROM host_metrics WHERE recorded_at <= $1 ORDER BY recorded_at DESC LIMIT 1
	`, until.UTC()).Scan(&out.RecordedAt, &out.CPUPercent, &out.MemoryUsedBytes, &out.MemoryTotalBytes,
		&out.GPUAvailable, &out.GPUName, &out.GPUPercent, &out.GPUMemoryUsedBytes, &out.GPUMemoryTotalBytes)
	return out, err
}
