package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

func (s *Store) RecordServiceObservation(ctx context.Context, observation domain.ServiceObservationRecord) error {
	observation.Component = strings.TrimSpace(observation.Component)
	observation.Operation = strings.TrimSpace(observation.Operation)
	observation.Outcome = strings.TrimSpace(observation.Outcome)
	if observation.Component == "" || observation.Operation == "" {
		return errors.New("service observation identity is required")
	}
	switch observation.Outcome {
	case "ok", "degraded", "error", "timeout", "misconfigured":
	default:
		return fmt.Errorf("invalid service observation outcome %q", observation.Outcome)
	}
	if observation.LatencyMS < 0 {
		observation.LatencyMS = 0
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO service_observations
			(component,operation,outcome,latency_ms,generation_job_id,correlation_id,error_code,detail,observed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, observation.Component, observation.Operation, observation.Outcome, observation.LatencyMS,
		observation.GenerationJobID, observation.CorrelationID, observation.ErrorCode, observation.Detail, observation.ObservedAt.UTC())
	return err
}

func (s *Store) RecordGatewayObservation(ctx context.Context, observation domain.GatewayObservation) error {
	if observation.RecordedAt.IsZero() {
		observation.RecordedAt = time.Now().UTC()
	}
	if observation.CleanupStatus == "" {
		observation.CleanupStatus = "never"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gateway_observations
			(database_bytes,active_jobs,overdue_jobs,active_leases,content_moderation_backlog,media_moderation_backlog,cleanup_status,cleanup_age_seconds,recorded_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, observation.DatabaseBytes, observation.ActiveJobs, observation.OverdueJobs, observation.ActiveLeases,
		observation.ContentModerationBacklog, observation.MediaModerationBacklog,
		observation.CleanupStatus, observation.CleanupAgeSeconds, observation.RecordedAt.UTC())
	return err
}

func (s *Store) CollectGatewayObservation(ctx context.Context, overdueAfter time.Duration) (domain.GatewayObservation, error) {
	if overdueAfter <= 0 {
		overdueAfter = 45 * time.Minute
	}
	var observation domain.GatewayObservation
	err := s.db.QueryRowContext(ctx, `
		SELECT
			pg_database_size(current_database()),
			(SELECT count(*) FROM generation_jobs WHERE state NOT IN ('completed','failed','cancelled','expired')),
			(SELECT count(*) FROM generation_jobs
			 WHERE state NOT IN ('completed','failed','cancelled','expired')
			   AND state_changed_at < now() - ($1::bigint * interval '1 second')),
			(SELECT count(*) FROM quick_generation_mining_leases),
			(SELECT count(*) FROM content_events WHERE sensitivity_classified_at IS NULL AND expires_at > now()),
			(SELECT count(*) FROM content_media m JOIN content_events e ON e.id=m.event_id
			 WHERE m.media_type='image' AND m.visual_sensitivity_classified_at IS NULL
			   AND m.expires_at > now() AND e.expires_at > now()),
			COALESCE((SELECT status FROM database_cleanup_state WHERE id=1),'never'),
			COALESCE((SELECT GREATEST(0,EXTRACT(EPOCH FROM (now()-COALESCE(last_success_at,last_finished_at,last_started_at)))::bigint)
			 FROM database_cleanup_state WHERE id=1),0),
			now()
	`, int64(overdueAfter/time.Second)).Scan(
		&observation.DatabaseBytes, &observation.ActiveJobs, &observation.OverdueJobs,
		&observation.ActiveLeases, &observation.ContentModerationBacklog, &observation.MediaModerationBacklog,
		&observation.CleanupStatus, &observation.CleanupAgeSeconds, &observation.RecordedAt,
	)
	return observation, err
}

func (s *Store) GatewayObservationSummary(ctx context.Context) (domain.GatewayObservationSummary, error) {
	var summary domain.GatewayObservationSummary
	err := s.db.QueryRowContext(ctx, `
		WITH latest AS (
			SELECT * FROM gateway_observations ORDER BY recorded_at DESC,id DESC LIMIT 1
		), baseline AS (
			SELECT database_bytes FROM gateway_observations
			WHERE recorded_at <= COALESCE((SELECT recorded_at FROM latest),now()) - interval '24 hours'
			ORDER BY recorded_at DESC,id DESC LIMIT 1
		), oldest AS (
			SELECT database_bytes FROM gateway_observations ORDER BY recorded_at,id LIMIT 1
		)
		SELECT latest.id,latest.database_bytes,latest.active_jobs,latest.overdue_jobs,latest.active_leases,
		       latest.content_moderation_backlog,latest.media_moderation_backlog,latest.cleanup_status,
		       latest.cleanup_age_seconds,latest.recorded_at,
		       latest.database_bytes-COALESCE((SELECT database_bytes FROM baseline),(SELECT database_bytes FROM oldest),latest.database_bytes)
		FROM latest
	`).Scan(
		&summary.Latest.ID, &summary.Latest.DatabaseBytes, &summary.Latest.ActiveJobs, &summary.Latest.OverdueJobs,
		&summary.Latest.ActiveLeases, &summary.Latest.ContentModerationBacklog, &summary.Latest.MediaModerationBacklog,
		&summary.Latest.CleanupStatus, &summary.Latest.CleanupAgeSeconds, &summary.Latest.RecordedAt,
		&summary.DatabaseGrowth24Hours,
	)
	return summary, err
}

func (s *Store) ServiceLatencySummaries(ctx context.Context, since time.Time) ([]domain.ServiceLatencySummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH grouped AS (
			SELECT component,operation,count(*)::bigint AS samples,
			       count(*) FILTER (WHERE outcome<>'ok')::bigint AS failures,
			       COALESCE(percentile_disc(0.50) WITHIN GROUP (ORDER BY latency_ms),0)::bigint AS p50_ms,
			       COALESCE(percentile_disc(0.95) WITHIN GROUP (ORDER BY latency_ms),0)::bigint AS p95_ms
			FROM service_observations WHERE observed_at >= $1
			GROUP BY component,operation
		), latest AS (
			SELECT DISTINCT ON (component,operation)
			       component,operation,latency_ms,outcome,error_code,detail,observed_at
			FROM service_observations WHERE observed_at >= $1
			ORDER BY component,operation,observed_at DESC,id DESC
		)
		SELECT g.component,g.operation,g.samples,g.failures,g.p50_ms,g.p95_ms,
		       l.latency_ms,l.outcome,l.error_code,l.detail,l.observed_at
		FROM grouped g JOIN latest l USING(component,operation)
		ORDER BY g.component,g.operation
	`, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ServiceLatencySummary, 0)
	for rows.Next() {
		var item domain.ServiceLatencySummary
		if err := rows.Scan(&item.Component, &item.Operation, &item.Samples, &item.Failures,
			&item.P50MS, &item.P95MS, &item.LastLatencyMS, &item.LastOutcome,
			&item.LastErrorCode, &item.LastDetail, &item.LastObservedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GenerationObservabilitySummary(ctx context.Context, since, overdueBefore time.Time) (domain.GenerationObservabilitySummary, error) {
	var summary domain.GenerationObservabilitySummary
	err := s.db.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE state NOT IN ('completed','failed','cancelled','expired'))::bigint,
			count(*) FILTER (WHERE state NOT IN ('completed','failed','cancelled','expired') AND state_changed_at < $2)::bigint,
			count(*) FILTER (WHERE state='completed' AND finished_at >= $1)::bigint,
			count(*) FILTER (WHERE state='failed' AND finished_at >= $1)::bigint,
			count(*) FILTER (WHERE state='cancelled' AND finished_at >= $1)::bigint,
			count(*) FILTER (WHERE state='expired' AND finished_at >= $1)::bigint,
			COALESCE(percentile_disc(0.50) WITHIN GROUP (ORDER BY GREATEST(0,(EXTRACT(EPOCH FROM (started_at-created_at))*1000)::bigint))
			 FILTER (WHERE started_at IS NOT NULL AND created_at >= $1),0)::bigint,
			COALESCE(percentile_disc(0.95) WITHIN GROUP (ORDER BY GREATEST(0,(EXTRACT(EPOCH FROM (started_at-created_at))*1000)::bigint))
			 FILTER (WHERE started_at IS NOT NULL AND created_at >= $1),0)::bigint,
			COALESCE(percentile_disc(0.50) WITHIN GROUP (ORDER BY GREATEST(0,(EXTRACT(EPOCH FROM (finished_at-started_at))*1000)::bigint))
			 FILTER (WHERE started_at IS NOT NULL AND finished_at IS NOT NULL AND finished_at >= $1),0)::bigint,
			COALESCE(percentile_disc(0.95) WITHIN GROUP (ORDER BY GREATEST(0,(EXTRACT(EPOCH FROM (finished_at-started_at))*1000)::bigint))
			 FILTER (WHERE started_at IS NOT NULL AND finished_at IS NOT NULL AND finished_at >= $1),0)::bigint
		FROM generation_jobs
	`, since.UTC(), overdueBefore.UTC()).Scan(
		&summary.ActiveJobs, &summary.OverdueJobs, &summary.Completed, &summary.Failed,
		&summary.Cancelled, &summary.Expired, &summary.QueueP50MS, &summary.QueueP95MS,
		&summary.ExecutionP50MS, &summary.ExecutionP95MS,
	)
	if err != nil {
		return domain.GenerationObservabilitySummary{}, err
	}
	denominator := summary.Completed + summary.Failed
	if denominator > 0 {
		summary.SuccessRate = int((summary.Completed*100 + denominator/2) / denominator)
	}
	hours := int(time.Since(since).Hours() + 0.5)
	if hours < 1 {
		hours = 1
	}
	summary.ObservationHours = hours
	return summary, nil
}

func (s *Store) GenerationOutcomeGroups(ctx context.Context, since time.Time, limit int) ([]domain.GenerationOutcomeGroup, error) {
	limit = boundedLimit(limit, 1, 100)
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(workflow_id,''),'Без workflow'),COALESCE(NULLIF(model_name,''),'Модель не указана'),
		       count(*)::bigint,
		       count(*) FILTER (WHERE state='completed')::bigint,
		       count(*) FILTER (WHERE state='failed')::bigint,
		       count(*) FILTER (WHERE state IN ('cancelled','expired'))::bigint
		FROM generation_jobs
		WHERE finished_at >= $1 AND state IN ('completed','failed','cancelled','expired')
		GROUP BY workflow_id,model_name
		ORDER BY count(*) DESC,workflow_id,model_name LIMIT $2
	`, since.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.GenerationOutcomeGroup, 0)
	for rows.Next() {
		var item domain.GenerationOutcomeGroup
		if err := rows.Scan(&item.WorkflowID, &item.ModelName, &item.Total, &item.Completed, &item.Failed, &item.Cancelled); err != nil {
			return nil, err
		}
		denominator := item.Completed + item.Failed
		if denominator > 0 {
			item.SuccessRate = int((item.Completed*100 + denominator/2) / denominator)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GenerationFailureSummaries(ctx context.Context, since time.Time, limit int) ([]domain.GenerationFailureSummary, error) {
	limit = boundedLimit(limit, 1, 100)
	rows, err := s.db.QueryContext(ctx, `
		SELECT public_id,correlation_id,username_snapshot,workflow_id,model_name,error_code,error_message,COALESCE(finished_at,updated_at)
		FROM generation_jobs
		WHERE state='failed' AND COALESCE(finished_at,updated_at) >= $1
		ORDER BY COALESCE(finished_at,updated_at) DESC,id DESC LIMIT $2
	`, since.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.GenerationFailureSummary, 0)
	for rows.Next() {
		var item domain.GenerationFailureSummary
		if err := rows.Scan(&item.JobPublicID, &item.CorrelationID, &item.Username, &item.WorkflowID,
			&item.ModelName, &item.ErrorCode, &item.ErrorMessage, &item.FailedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) AdminGenerationJobTrace(ctx context.Context, publicID string) (domain.GenerationJobTrace, error) {
	job, err := scanGenerationJob(s.db.QueryRowContext(ctx, `SELECT `+generationJobColumns+`
		FROM generation_jobs WHERE public_id=$1`, strings.TrimSpace(publicID)))
	if err != nil {
		return domain.GenerationJobTrace{}, err
	}
	trace := domain.GenerationJobTrace{Job: job}
	if trace.Transitions, err = s.adminGenerationJobTransitions(ctx, job.ID); err != nil {
		return domain.GenerationJobTrace{}, err
	}
	if trace.ServiceObservations, err = s.generationJobServiceObservations(ctx, job.ID, job.CorrelationID); err != nil {
		return domain.GenerationJobTrace{}, err
	}
	if trace.ProxyRequests, err = s.generationJobProxyRequests(ctx, job.ID, job.CorrelationID); err != nil {
		return domain.GenerationJobTrace{}, err
	}
	if trace.AuditEvents, err = s.generationJobAuditEvents(ctx, job.ID, job.CorrelationID); err != nil {
		return domain.GenerationJobTrace{}, err
	}
	if trace.ContentEvents, err = s.generationJobContentEvents(ctx, job.ID, job.CorrelationID); err != nil {
		return domain.GenerationJobTrace{}, err
	}
	lease, leaseErr := s.QuickGenerationMiningLeaseByJobID(ctx, job.ID)
	if leaseErr == nil {
		trace.MiningLease = &lease
	} else if !errors.Is(leaseErr, sql.ErrNoRows) {
		return domain.GenerationJobTrace{}, leaseErr
	}
	return trace, nil
}

func (s *Store) adminGenerationJobTransitions(ctx context.Context, jobID int64) ([]domain.GenerationJobTransition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,correlation_id,from_state,to_state,message,error_code,error_message,attempt,duration_ms,created_at
		FROM generation_job_transitions WHERE job_id=$1 ORDER BY created_at,id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.GenerationJobTransition, 0)
	for rows.Next() {
		var item domain.GenerationJobTransition
		var fromState, toState string
		if err := rows.Scan(&item.ID, &item.JobID, &item.CorrelationID, &fromState, &toState,
			&item.Message, &item.ErrorCode, &item.ErrorMessage, &item.Attempt, &item.DurationMS, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.FromState = domain.GenerationJobState(fromState)
		item.ToState = domain.GenerationJobState(toState)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) generationJobServiceObservations(ctx context.Context, jobID int64, correlationID string) ([]domain.ServiceObservationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT component,operation,outcome,latency_ms,generation_job_id,correlation_id,error_code,detail,observed_at
		FROM service_observations WHERE generation_job_id=$1 OR (generation_job_id IS NULL AND $2<>'' AND correlation_id=$2)
		ORDER BY observed_at,id LIMIT 500`, jobID, correlationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ServiceObservationRecord, 0)
	for rows.Next() {
		var item domain.ServiceObservationRecord
		if err := rows.Scan(&item.Component, &item.Operation, &item.Outcome, &item.LatencyMS,
			&item.GenerationJobID, &item.CorrelationID, &item.ErrorCode, &item.Detail, &item.ObservedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) generationJobProxyRequests(ctx context.Context, jobID int64, correlationID string) ([]domain.TraceProxyRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,request_id,correlation_id,service,method,path,status_code,duration_ms,bytes_in,bytes_out,created_at
		FROM proxy_requests WHERE generation_job_id=$1 OR (generation_job_id IS NULL AND $2<>'' AND correlation_id=$2)
		ORDER BY created_at,id LIMIT 500`, jobID, correlationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.TraceProxyRequest, 0)
	for rows.Next() {
		var item domain.TraceProxyRequest
		if err := rows.Scan(&item.ID, &item.RequestID, &item.CorrelationID, &item.Service, &item.Method,
			&item.Path, &item.Status, &item.DurationMS, &item.BytesIn, &item.BytesOut, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) generationJobAuditEvents(ctx context.Context, jobID int64, correlationID string) ([]domain.TraceAuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,request_id,correlation_id,action,target_type,metadata::text,created_at
		FROM audit_log WHERE generation_job_id=$1 OR (generation_job_id IS NULL AND $2<>'' AND correlation_id=$2)
		ORDER BY created_at,id LIMIT 500`, jobID, correlationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.TraceAuditEvent, 0)
	for rows.Next() {
		var item domain.TraceAuditEvent
		if err := rows.Scan(&item.ID, &item.RequestID, &item.CorrelationID, &item.Action,
			&item.TargetType, &item.Metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) generationJobContentEvents(ctx context.Context, jobID int64, correlationID string) ([]domain.TraceContentEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.correlation_id,e.service,e.kind,COALESCE(e.external_id,''),e.model,e.generation_state,
		       count(m.id)::bigint,e.created_at
		FROM content_events e LEFT JOIN content_media m ON m.event_id=e.id
		WHERE e.generation_job_id=$1 OR (e.generation_job_id IS NULL AND $2<>'' AND e.correlation_id=$2)
		GROUP BY e.id ORDER BY e.created_at,e.id LIMIT 500`, jobID, correlationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.TraceContentEvent, 0)
	for rows.Next() {
		var item domain.TraceContentEvent
		if err := rows.Scan(&item.ID, &item.CorrelationID, &item.Service, &item.Kind, &item.ExternalID,
			&item.Model, &item.GenerationState, &item.MediaCount, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
