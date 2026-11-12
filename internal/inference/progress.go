package inference

import (
	"context"
	"encoding/json"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/promptassistant"
)

type AssistantProgress struct {
	Phase      string                `json:"phase"`
	Prompt     string                `json:"prompt,omitempty"`
	Usage      promptassistant.Usage `json:"usage"`
	StartedAt  *time.Time            `json:"started_at,omitempty"`
	FinishedAt *time.Time            `json:"finished_at,omitempty"`
}

func progressAAD(id, token string) []byte {
	return []byte("gpu-inference/progress/v1/" + id + "/" + token)
}

func (c *Client) Progress(ctx context.Context, id string) (*AssistantProgress, error) {
	saved, err := c.store.GPUInferenceProgress(ctx, id)
	if err != nil {
		return nil, err
	}
	progress := AssistantProgress{Phase: "loading"}
	if len(saved.Cipher) > 0 {
		plain, err := c.cipher.DecryptBytesWithAAD(saved.Cipher, progressAAD(id, saved.RunToken))
		if err != nil {
			return nil, err
		}
		defer clear(plain)
		if err = json.Unmarshal(plain, &progress); err != nil {
			return nil, err
		}
	}
	progress.StartedAt, progress.FinishedAt = saved.StartedAt, saved.FinishedAt
	return &progress, nil
}

func (w *Worker) progressWriter(ctx context.Context, job domain.GPUInference) func(AssistantProgress) {
	var last time.Time
	return func(progress AssistantProgress) {
		if progress.Phase != "finishing" && time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		plain, err := json.Marshal(progress)
		if err != nil || len(plain) > domain.MaxGPUInferenceResponseBytes {
			clear(plain)
			return
		}
		defer clear(plain)
		cipher, err := w.cipher.EncryptBytesWithAAD(plain, progressAAD(job.ID, job.RunToken))
		if err != nil {
			return
		}
		defer clear(cipher)
		writeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		// Presentation failures must never interrupt execution or its terminal receipt.
		_ = w.store.SaveGPUInferenceProgress(writeCtx, job.ID, job.RunToken, cipher)
	}
}
