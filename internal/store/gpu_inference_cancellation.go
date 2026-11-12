package store

import "context"

// Match the immutable executor claim; a stale worker cannot observe cancellation
// for another execution. This read does not acquire the GPU resource lock.
func (s *Store) GPUInferenceCancellation(ctx context.Context, id, runToken string) (bool, error) {
	var cancelled bool
	err := s.db.QueryRowContext(ctx, `SELECT cancel_requested FROM gpu_inference_requests WHERE id=$1 AND run_token=$2 AND state='running'`, id, runToken).Scan(&cancelled)
	return cancelled, err
}
