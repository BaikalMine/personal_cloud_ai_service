package store

import (
	"context"
	"database/sql"
	"errors"

	"ai-access-gateway/internal/domain"
)

func (s *Store) SetGPUInferencePreparation(ctx context.Context, id, token string, ready bool) error {
	previous, next := "reserved", "preparing_resources"
	if ready {
		previous, next = "preparing_resources", "resources_ready"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE gpu_work SET phase=$4,updated_at=now()
		WHERE id=$1 AND lease_token=$2 AND phase=$3 AND state='held' AND lease_until>clock_timestamp()
		AND NOT cancellation_requested AND kind IN ('lora_caption','prompt_assistant','harness_ultra')`, id, token, previous, next)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrGPUWorkConflict
	}
	return err
}

// The expired token and the pre-dispatch phase jointly prove that the old
// sender cannot submit. Resource preparation in flight is explicitly excluded.
func (s *Store) FenceExpiredGPUInferenceReservation(ctx context.Context, id string) (domain.GPUWork, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUWork{}, false, err
	}
	defer tx.Rollback()
	resource, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return work, false, err
	}
	if (work.Kind != domain.GPUWorkCaption && work.Kind != domain.GPUWorkAssistant && work.Kind != domain.GPUWorkHarnessUltra) ||
		(work.State != domain.GPUWorkHeld && work.State != domain.GPUWorkUncertain) ||
		(work.Phase != "reserved" && work.Phase != "resources_ready") || work.InferenceCompletion != "" || work.ExternalID != "" {
		return work, false, tx.Commit()
	}
	work, err = scanGPUWork(tx.QueryRowContext(ctx, `UPDATE gpu_work SET state='uncertain',
		inference_completion='request_not_dispatched',updated_at=now()
		WHERE id=$1 AND lease_until<=clock_timestamp() RETURNING `+gpuWorkColumns, id))
	if errors.Is(err, sql.ErrNoRows) {
		return work, false, tx.Commit()
	}
	if err != nil {
		return work, false, err
	}
	if err = gpuEvent(ctx, tx, work.ID, work.State, "pre_dispatch_fenced", "Expired invocation cannot enter model dispatch"); err != nil {
		return work, false, err
	}
	if err = invalidateGPUObservation(ctx, tx, resource.ID); err != nil {
		return work, false, err
	}
	return work, true, tx.Commit()
}
