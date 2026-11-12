package store

import (
	"ai-access-gateway/internal/domain"
	"context"
)

// The compare-and-swap is the last durable boundary before HTTP submission.
// A lost response must never cause a second dispatch of the same job.
func (s *Store) BeginHarnessUltraDispatch(ctx context.Context, id, token, promptID string) (domain.GPUWork, error) {
	return scanGPUWork(s.db.QueryRowContext(ctx, `UPDATE gpu_work SET phase='dispatching',external_id=$3,updated_at=now()
	 WHERE id=$1 AND lease_token=$2 AND kind='harness_ultra' AND phase='resources_ready' AND state='held'
	 AND lease_until>clock_timestamp() AND NOT cancellation_requested RETURNING `+gpuWorkColumns, id, token, promptID))
}

func (s *Store) HarnessUltraPermitSettled(ctx context.Context, work domain.GPUWork) (bool, error) {
	var settled bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gpu_executor_permits WHERE work_id=$1 AND state='closed')
	 AND NOT EXISTS(SELECT 1 FROM gpu_executor_permits WHERE work_id=$1 AND state IN ('reserved','running'))`, work.ID).Scan(&settled)
	return settled, err
}

// A response acknowledgement is evidence, not permission to execute; retain it
// even if cancellation arrived while ComfyUI was accepting the HTTP request.
func (s *Store) AcknowledgeHarnessUltraDispatch(ctx context.Context, id, token, promptID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE gpu_work SET phase='running',updated_at=now() WHERE id=$1 AND lease_token=$2
	 AND kind='harness_ultra' AND phase='dispatching' AND external_id=$3 AND state IN ('held','uncertain')`, id, token, promptID)
	return err
}
