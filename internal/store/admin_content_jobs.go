package store

import (
	"context"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

func (s *Store) ListAdminGenerationJobs(ctx context.Context, limit int, username string, createdAfter time.Time) ([]domain.GenerationJob, error) {
	if createdAfter.IsZero() {
		return nil, errors.New("generation job content boundary is required")
	}
	limit = boundedLimit(limit, 1, 500)
	rows, err := s.db.QueryContext(ctx, `SELECT `+generationJobColumns+` FROM generation_jobs
		WHERE created_at >= $3
		  AND ($2='' OR COALESCE((SELECT u.username FROM users u WHERE u.id=generation_jobs.user_id),NULLIF(username_snapshot,''),'Удалённый пользователь') ILIKE '%' || $2 || '%')
		ORDER BY created_at DESC,id DESC LIMIT $1`, limit, username, createdAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]domain.GenerationJob, 0)
	for rows.Next() {
		job, err := scanGenerationJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) GenerationJobTransitionsForAdmin(ctx context.Context, jobIDs []int64) (map[int64][]domain.GenerationJobTransition, error) {
	result := make(map[int64][]domain.GenerationJobTransition)
	if len(jobIDs) == 0 {
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,correlation_id,from_state,to_state,
		message,error_code,error_message,attempt,duration_ms,created_at
		FROM generation_job_transitions
		WHERE job_id=ANY($1)
		ORDER BY job_id,created_at,id`, pq.Array(jobIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.GenerationJobTransition
		var fromState, toState string
		if err := rows.Scan(&item.ID, &item.JobID, &item.CorrelationID, &fromState, &toState,
			&item.Message, &item.ErrorCode, &item.ErrorMessage, &item.Attempt, &item.DurationMS, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.FromState = domain.GenerationJobState(fromState)
		item.ToState = domain.GenerationJobState(toState)
		result[item.JobID] = append(result[item.JobID], item)
	}
	return result, rows.Err()
}

func (s *Store) LinkGenerationJobAssistantEvents(ctx context.Context, jobID, userID int64, correlationID string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE content_events e
		SET generation_job_id=$1
		FROM generation_jobs j
		WHERE j.id=$1 AND e.user_id=$2 AND e.kind='prompt_assistant' AND e.correlation_id=$3 AND j.correlation_id=$3
		  AND (e.generation_job_id IS NULL OR e.generation_job_id=$1)`, jobID, userID, correlationID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
