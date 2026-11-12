package store

import (
	"context"

	"ai-access-gateway/internal/domain"
)

func (s *Store) ManualMiningResumeBlocked(ctx context.Context) (bool, error) {
	var blocked bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM gpu_work WHERE resource_id=$1 AND
		(state IN ('held','uncertain') OR (state='waiting' AND NOT cancellation_requested AND ready_until>clock_timestamp()))
	) OR EXISTS (
		SELECT 1 FROM gpu_executor_permits WHERE resource_id=$1 AND state IN ('reserved','running')
	) OR EXISTS (SELECT 1 FROM quick_generation_mining_leases)`, domain.GPUPrimaryResource).Scan(&blocked)
	return blocked, err
}
