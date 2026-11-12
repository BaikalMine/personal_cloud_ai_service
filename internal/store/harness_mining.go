package store

import (
	"ai-access-gateway/internal/domain"
	"context"
)

// Caller holds the GPU admission guard before recording intent and stopping mining.
func (s *Store) CreateHarnessMiningLease(ctx context.Context, lease domain.QuickGenerationMiningLease, input domain.GPUExecutorPermitRequest) error {
	if input.ExecutorKind() != domain.GPUExecutorOllama || !validGPUExecutorPermitRequest(input) || input.WorkID != "" || lease.ExecutorPermitID != input.ID || lease.ExecutorOwnerID != input.OwnerID || lease.ExecutorGeneration != input.OwnerGeneration {
		return ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkInferenceExecutor(ctx, tx, input.OwnedExecutor()); err != nil {
		return err
	}
	if err = insertMiningLease(ctx, tx, lease); err != nil {
		return err
	}
	return tx.Commit()
}

// An HTTP error/timeout is not stop evidence. Only a closed permit or a newer
// generation of the exact native owner can make the durable reservation ready.
func (s *Store) HarnessMiningLeaseSettled(ctx context.Context, lease domain.QuickGenerationMiningLease) (bool, error) {
	var settled bool
	err := s.db.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM gpu_executor_permits WHERE id=$1 AND executor='ollama' AND owner_id=$2 AND owner_generation=$3 AND state='closed') OR
 EXISTS(SELECT 1 FROM gpu_inference_executors WHERE id=$2 AND generation>$3)`, lease.ExecutorPermitID, lease.ExecutorOwnerID, lease.ExecutorGeneration).Scan(&settled)
	return settled, err
}
