package store

import (
	"ai-access-gateway/internal/domain"
	"context"
)

func (s *Store) ComfyRuntimeBlocked(ctx context.Context) (bool, error) {
	var blocked bool
	err := s.db.QueryRowContext(ctx, `SELECT `+storageAdmissionBlockedSQL+`
 OR EXISTS(SELECT 1 FROM gpu_executor_permits WHERE state IN ('reserved','running'))`, domain.GPUPrimaryResource).Scan(&blocked)
	return blocked, err
}
