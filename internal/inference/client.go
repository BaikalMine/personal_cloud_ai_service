package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

var (
	ErrUnconfirmed   = errors.New("inference completion is unconfirmed")
	ErrNotDispatched = errors.New("inference request was not dispatched")
	ErrResultExpired = errors.New("inference result has expired")
	ErrStopped       = errors.New("owned model execution stopped without a usable result")
)

type Client struct {
	store  *store.Store
	cipher *contentcrypto.Cipher
}

func NewClient(repository *store.Store, cipher *contentcrypto.Cipher) *Client {
	return &Client{store: repository, cipher: cipher}
}

func requestAAD(id string) []byte  { return []byte("gpu-inference/request/v1/" + id) }
func responseAAD(id string) []byte { return []byte("gpu-inference/response/v1/" + id) }

func (c *Client) Submit(ctx context.Context, work domain.GPUWork, body []byte, timeout time.Duration) (domain.GPUInference, error) {
	input, err := c.prepare(work, body, timeout)
	if err != nil {
		return domain.GPUInference{}, err
	}
	defer clear(input.RequestCipher)
	return c.store.SubmitGPUInference(ctx, input)
}

func (c *Client) SubmitCaption(ctx context.Context, work domain.GPUWork, body []byte, timeout time.Duration, captionID, captionToken string, metadata []byte) (domain.GPUInference, error) {
	input, err := c.prepare(work, body, timeout)
	if err != nil {
		return domain.GPUInference{}, err
	}
	defer clear(input.RequestCipher)
	return c.store.SubmitCaptionGPUInference(ctx, input, captionID, captionToken, metadata)
}

func (c *Client) SubmitAssistant(ctx context.Context, work domain.GPUWork, body []byte, timeout time.Duration, id, token string, metadata []byte) (domain.GPUInference, error) {
	input, err := c.prepare(work, body, timeout)
	if err != nil {
		return domain.GPUInference{}, err
	}
	defer clear(input.RequestCipher)
	return c.store.SubmitAssistantGPUInference(ctx, input, id, token, metadata)
}

func (c *Client) prepare(work domain.GPUWork, body []byte, timeout time.Duration) (domain.GPUInference, error) {
	if len(body) == 0 || len(body) > domain.MaxGPUInferenceRequestBytes || timeout < time.Second || timeout > time.Hour {
		return domain.GPUInference{}, store.ErrGPUWorkInput
	}
	cipher, err := c.cipher.EncryptBytesWithAAD(body, requestAAD(work.ID))
	if err != nil {
		return domain.GPUInference{}, err
	}
	hash := sha256.Sum256(body)
	return domain.GPUInference{ID: work.ID, LeaseToken: work.LeaseToken, RequestHash: hex.EncodeToString(hash[:]), RequestCipher: cipher, TimeoutMS: timeout.Milliseconds()}, nil
}

// Cancellation of the caller stops waiting, not execution. Reconnect with the
// same request ID to retrieve the saved response without repeating inference.
func (c *Client) Wait(ctx context.Context, id, token string) (domain.GPUInference, []byte, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := c.store.GPUInference(ctx, id)
		if err != nil {
			return job, nil, err
		}
		if token == "" || job.LeaseToken != token {
			return domain.GPUInference{}, nil, store.ErrGPUWorkConflict
		}
		switch job.State {
		case "stopped":
			return job, nil, ErrStopped
		case "not_dispatched":
			return job, nil, ErrNotDispatched
		case "uncertain":
			return job, nil, ErrUnconfirmed
		case "completed":
			if len(job.ResponseCipher) == 0 || job.ResultExpiresAt == nil || !job.ResultExpiresAt.After(time.Now()) {
				return job, nil, ErrResultExpired
			}
			body, err := c.cipher.DecryptBytesWithAAD(job.ResponseCipher, responseAAD(id))
			if err != nil {
				return job, nil, fmt.Errorf("decode persisted inference response: %w", err)
			}
			return job, body, nil
		}
		select {
		case <-ctx.Done():
			return job, nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
