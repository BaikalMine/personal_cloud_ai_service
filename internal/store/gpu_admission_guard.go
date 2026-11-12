package store

import (
	"context"
	"database/sql/driver"
	"time"
)

// Serialize admission with the final mining resume check across Gateway
// instances. No transaction or row lock is held during executor network I/O.
func (s *Store) WithGPUAdmissionGuard(ctx context.Context, work func() error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var acquired bool
	const key = "gateway_gpu_admission_and_mining_resume"
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return ErrGPUWorkConflict
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	return work()
}

func (s *Store) UncoordinatedGPUWork(ctx context.Context) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM generation_jobs j WHERE resources_released_at IS NULL AND dispatch_closed_at IS NULL
		 AND (prompt_id IS NOT NULL OR submission_started_at IS NOT NULL) AND submission_rejected_at IS NULL
		 AND state IN ('preparing','uploading','waiting_for_resources','queued','running','postprocessing','archiving'))
		OR EXISTS(SELECT 1 FROM lora_training_jobs WHERE state IN ('uploading','preparing','caching','running','installing')
		 AND (agent_submission_started_at IS NOT NULL OR agent_job_id<>''))`).Scan(&active)
	return active, err
}
