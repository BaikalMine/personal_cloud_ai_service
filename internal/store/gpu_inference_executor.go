package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
)

func validInferenceExecutor(executor domain.GPUInferenceExecutor) bool {
	id, idErr := hex.DecodeString(executor.ID)
	token, tokenErr := hex.DecodeString(executor.Token)
	return idErr == nil && tokenErr == nil && len(id) == 32 && len(token) == 32 && executor.Generation > 0
}

// Called only after the native owner has drained its previous process tree.
// Local generations are durable and monotonic: a late registration cannot
// replace a newer owner, and repeating a lost commit response is idempotent.
func (s *Store) RegisterOwnedInferenceExecutor(ctx context.Context, executor domain.GPUInferenceExecutor) error {
	if !validInferenceExecutor(executor) {
		return ErrGPUWorkInput
	}
	var id string
	err := s.db.QueryRowContext(ctx, `INSERT INTO gpu_inference_executors(id,generation,session_token) VALUES($1,$2,$3)
		ON CONFLICT(id) DO UPDATE SET generation=EXCLUDED.generation,session_token=EXCLUDED.session_token,updated_at=now()
		WHERE gpu_inference_executors.generation<EXCLUDED.generation OR
		(gpu_inference_executors.generation=EXCLUDED.generation AND gpu_inference_executors.session_token=EXCLUDED.session_token)
		RETURNING id`, executor.ID, executor.Generation, executor.Token).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGPUWorkConflict
	}
	return err
}

func checkInferenceExecutor(ctx context.Context, tx *sql.Tx, executor domain.GPUInferenceExecutor) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM gpu_inference_executors WHERE id=$1 AND generation=$2 AND session_token=$3 FOR SHARE`,
		executor.ID, executor.Generation, executor.Token).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGPUWorkConflict
	}
	return err
}

// Process shutdown must already have been confirmed by this same native owner.
// Never select direct/unbound calls or requests from the current generation.
func (s *Store) RecoverOwnedGPUInference(ctx context.Context, executor domain.GPUInferenceExecutor) (int, error) {
	if !validInferenceExecutor(executor) {
		return 0, ErrGPUWorkInput
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_token FROM gpu_inference_requests
		WHERE executor_id=$1 AND executor_generation<$2 AND state IN ('running','uncertain') ORDER BY id LIMIT 100`, executor.ID, executor.Generation)
	if err != nil {
		return 0, err
	}
	type identity struct{ id, token string }
	var requests []identity
	for rows.Next() {
		var item identity
		if err = rows.Scan(&item.id, &item.token); err != nil {
			rows.Close()
			return 0, err
		}
		requests = append(requests, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for index, item := range requests {
		if err = s.finishGPUInference(ctx, item.id, item.token, "executor_stopped", 0, nil, "", "", time.Time{}, &executor); err != nil {
			return index, err
		}
	}
	return len(requests), nil
}
