package store

import (
	"context"
	"time"

	"ai-access-gateway/internal/domain"
)

type GPUInferenceProgress struct {
	Cipher     []byte
	RunToken   string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

func (s *Store) IsAssistantInference(ctx context.Context, id string) (bool, error) {
	var bound bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM prompt_assistant_jobs WHERE inference_work_id=$1 AND state IN ('queued','running') AND expires_at>now())`, id).Scan(&bound)
	return bound, err
}

func (s *Store) SaveGPUInferenceProgress(ctx context.Context, id, token string, cipher []byte) error {
	if id == "" || token == "" || len(cipher) == 0 || len(cipher) > domain.MaxGPUInferenceResponseBytes+64 {
		return ErrGPUWorkInput
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO gpu_inference_progress(id,run_token,cipher,expires_at)
	 SELECT r.id,r.run_token,$3,LEAST(j.expires_at,r.created_at+interval '24 hours')
	 FROM gpu_inference_requests r JOIN prompt_assistant_jobs j ON j.inference_work_id=r.id
	 WHERE r.id=$1 AND r.run_token=$2 AND r.state='running' AND NOT r.cancel_requested AND j.state IN ('queued','running') AND j.expires_at>now()
	 ON CONFLICT(id) DO UPDATE SET run_token=EXCLUDED.run_token,cipher=EXCLUDED.cipher,updated_at=now(),expires_at=EXCLUDED.expires_at`, id, token, cipher)
	return err
}

func (s *Store) GPUInferenceProgress(ctx context.Context, id string) (GPUInferenceProgress, error) {
	var progress GPUInferenceProgress
	err := s.db.QueryRowContext(ctx, `SELECT p.cipher,COALESCE(p.run_token,''),r.started_at,r.finished_at
	 FROM gpu_inference_requests r LEFT JOIN gpu_inference_progress p ON p.id=r.id AND p.run_token=r.run_token AND p.expires_at>now()
	 AND r.state IN ('running','completed') AND NOT r.cancel_requested
	 WHERE r.id=$1`, id).Scan(&progress.Cipher, &progress.RunToken, &progress.StartedAt, &progress.FinishedAt)
	return progress, err
}
