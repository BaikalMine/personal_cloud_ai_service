package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/promptassistant"
)

func (a *App) refreshLoraCaptionJobs(ctx context.Context) (int64, error) {
	if a.promptAssistant == nil || !a.promptAssistant.VisionConfigured() {
		return 0, nil
	}
	var count int64
	_, err := a.store.WithLoraCaptionWorker(ctx, func(conn *sql.Conn) error {
		job, err := a.store.ClaimLoraCaptionJob(ctx, newRequestID())
		if err != nil || job.ID == "" {
			return err
		}
		count = 1
		return a.runDurableLoraCaptionJob(ctx, conn, job)
	})
	return count, err
}

func (a *App) runDurableLoraCaptionJob(parent context.Context, conn *sql.Conn, job domain.LoraCaptionJob) error {
	policy := a.promptAssistant.PolicyForRequest(promptassistant.ModeImageToImage, promptassistant.ProfileWorkflowDefault, false, true)
	ctx, cancel := context.WithTimeout(parent, policy.Timeout+2*time.Minute)
	defer cancel()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				probe, release := context.WithTimeout(ctx, 3*time.Second)
				err := conn.PingContext(probe)
				active := false
				if err == nil {
					active, err = a.store.LoraCaptionRunActive(probe, job.ID, job.RunToken)
				}
				release()
				if err != nil || !active {
					cancel()
					return
				}
			}
		}
	}()
	result, runErr := a.executeLoraCaption(ctx, job)
	close(stop)
	<-done
	state, message := "completed", "Описание готово"
	var cipher []byte
	var retryAfter time.Duration
	if runErr == nil {
		plain, err := json.Marshal(result)
		if err != nil {
			return err
		}
		defer clear(plain)
		cipher, runErr = a.contentCipher.EncryptBytes(plain)
	}
	if runErr != nil {
		state = "failed"
		message = "Не удалось подготовить описание. Повторите этот кадр."
		if job.Attempts < domain.LoraCaptionMaxAttempts && (parent.Err() != nil || retryableLoraCaptionError(runErr) || errors.Is(runErr, context.Canceled)) {
			state = "queued"
			retryAfter = time.Duration(job.Attempts*5) * time.Second
			message = "Связь с ассистентом прервана. Задание будет повторено."
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finishCancel()
	_, err := a.store.FinishLoraCaptionJob(finishCtx, job.ID, job.RunToken, state, message, cipher, retryAfter)
	return err
}

func retryableLoraCaptionError(err error) bool {
	var upstream *promptassistant.CaptionHTTPError
	if errors.As(err, &upstream) {
		return upstream.StatusCode == 408 || upstream.StatusCode == 429 || upstream.StatusCode >= 500
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errMediaMemoryBudget)
}

func (a *App) executeLoraCaption(ctx context.Context, job domain.LoraCaptionJob) (domain.LoraCaptionResult, error) {
	var output domain.LoraCaptionResult
	input, err := a.decodeLoraCaptionInput(job)
	if err != nil {
		return output, err
	}
	user, err := a.store.UserByID(ctx, job.UserID)
	if err != nil {
		return output, err
	}
	if user.Disabled || (user.AccountExpiresAt.Valid && !user.AccountExpiresAt.Time.After(time.Now())) || (user.Role != "admin" && !user.CanTrainImageLora) {
		return output, errors.New("caption permission revoked")
	}
	releaseAssistant, acquired := acquireBoundedSlot(ctx, a.promptAssistantSlots, 20*time.Minute)
	if !acquired {
		return output, context.DeadlineExceeded
	}
	defer releaseAssistant()
	releaseMedia, acquired := a.mediaByteLimiter().tryAcquire(promptAssistantMemoryReservation)
	if !acquired {
		return output, errMediaMemoryBudget
	}
	defer releaseMedia()
	asset, err := a.store.LoraDatasetAsset(ctx, job.UserID, job.AssetID)
	if err != nil {
		return output, err
	}
	if asset.Hash != input.AssetHash {
		return output, errors.New("caption image version mismatch")
	}
	file, err := a.materializeLoraDatasetAsset(ctx, asset)
	if err != nil {
		return output, err
	}
	defer file.Close()
	image, err := io.ReadAll(io.LimitReader(file, maxLoraTrainingImageBytes+1))
	if err != nil {
		return output, err
	}
	defer clear(image)
	prepared, mime, _, err := prepareVisionReference(image, asset.MIMEType)
	if err != nil {
		return output, err
	}
	defer clear(prepared)
	lease, warning, err := a.pauseMiningForQuickGeneration(ctx, &user, 0)
	if err != nil {
		return output, fmt.Errorf("prepare caption resources: %w", err)
	}
	if lease != nil {
		defer func() {
			release, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			a.releaseMiningPause(release, lease.ID)
		}()
	}
	started := time.Now()
	result, err := a.promptAssistant.CaptionImageWithInstruction(ctx, input.TriggerWord, input.ConceptType, prepared, mime, input.Instruction)
	a.observeServiceCall(ctx, dependencyOllama, "caption_lora_image", started, err, false, "assistant_request_failed", "")
	if err != nil {
		return output, err
	}
	if strings.TrimSpace(result.Caption) == "" {
		return output, errors.New("empty caption result")
	}
	output = domain.LoraCaptionResult{Caption: truncateLoraText(ensureLoraCaptionTrigger(input.TriggerWord, result.Caption), promptassistant.MaxLoraCaptionCharacters), Model: result.Model, Warning: warning}
	a.audit(ctx, &user.ID, "lora_training_caption_generated", "lora_training_dataset", nil, "", "", map[string]any{"job_id": job.ID, "dataset_id": job.DatasetID, "image_id": job.ImageID, "model": result.Model, "instruction_version": job.InstructionVersion, "usage": result.Usage})
	return output, nil
}
