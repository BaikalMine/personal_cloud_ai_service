package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/inference"
	"ai-access-gateway/internal/promptassistant"
)

type loraCaptionInferenceMetadata struct {
	Request promptassistant.PreparedCaption `json:"request"`
	Warning string                          `json:"warning,omitempty"`
}

type loraCaptionInferencePending struct {
	message string
	cause   error
}

func (e *loraCaptionInferencePending) Error() string { return e.message }
func (e *loraCaptionInferencePending) Unwrap() error { return e.cause }

func (a *App) submitLoraCaptionInference(ctx context.Context, job domain.LoraCaptionJob, input domain.LoraCaptionInput, image []byte, mime string, work domain.GPUWork, warning string) (domain.LoraCaptionResult, promptassistant.ExecutionOutcome, error) {
	var output domain.LoraCaptionResult
	prepared, err := a.promptAssistant.PrepareCaptionImageWithInstruction(input.TriggerWord, input.ConceptType, image, mime, input.Instruction)
	if err != nil {
		return output, promptassistant.ExecutionNotDispatched, err
	}
	defer clear(prepared.Body)
	metadata, err := json.Marshal(loraCaptionInferenceMetadata{Request: prepared, Warning: warning})
	if err != nil {
		return output, promptassistant.ExecutionNotDispatched, err
	}
	defer clear(metadata)
	encrypted, err := a.contentCipher.EncryptBytes(metadata)
	if err != nil {
		return output, promptassistant.ExecutionNotDispatched, err
	}
	defer clear(encrypted)
	client := inference.NewClient(a.store, a.contentCipher)
	_, err = client.SubmitCaption(ctx, work, prepared.Body, prepared.Policy.Timeout, job.ID, job.RunToken, encrypted)
	if err != nil {
		return output, promptassistant.ExecutionUnconfirmed, &loraCaptionInferencePending{message: "Проверяем отправку описания. Повторного запуска не будет.", cause: err}
	}
	// Release the Gateway worker/decoded image buffers immediately. The separate
	// executor owns the model call; a later pass reads this same persisted ID.
	return output, promptassistant.ExecutionUnconfirmed, &loraCaptionInferencePending{message: "Описание передано модели. Ожидаем результат."}
}

func (a *App) recoverLoraCaptionInference(ctx context.Context, job domain.LoraCaptionJob, input domain.LoraCaptionInput) (domain.LoraCaptionResult, error) {
	var output domain.LoraCaptionResult
	request, err := a.store.GPUInference(ctx, job.InferenceWorkID)
	if err != nil {
		return output, &loraCaptionInferencePending{message: "Проверяем состояние ранее отправленного описания.", cause: err}
	}
	work, err := a.store.GPUWork(ctx, request.ID)
	if err != nil {
		return output, &loraCaptionInferencePending{message: "Проверяем состояние ранее отправленного описания.", cause: err}
	}
	execution := promptassistant.ExecutionUnconfirmed
	if request.CompletionCode == "executor_terminal" {
		execution = promptassistant.ExecutionCompleted
	}
	if request.CompletionCode == "request_not_dispatched" {
		execution = promptassistant.ExecutionNotDispatched
	} else if request.CompletionCode == "executor_stopped" {
		execution = promptassistant.ExecutionStopped
	}
	defer func() { a.gpuCoordinator.finishInference(domain.GPUWorkCaption, work.JobKey, execution) }()
	if request.State == "stopped" {
		return output, errors.New("Исполнитель модели остановился без готового описания. Повторите этот кадр.")
	}
	if !request.Settled() {
		message := "Модель готовит описание. Проверяем результат."
		if request.State == "uncertain" {
			message = "Ответ модели пока не подтверждён. Проверяем прежний запрос."
		}
		return output, &loraCaptionInferencePending{message: message}
	}
	metadataBytes, err := a.contentCipher.DecryptBytes(job.InferenceMetadataCipher)
	if err != nil {
		return output, err
	}
	defer clear(metadataBytes)
	var metadata loraCaptionInferenceMetadata
	if err = json.Unmarshal(metadataBytes, &metadata); err != nil {
		return output, err
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	saved, body, err := inference.NewClient(a.store, a.contentCipher).Wait(readCtx, request.ID, request.LeaseToken)
	if err != nil {
		if errors.Is(err, inference.ErrNotDispatched) {
			return output, errors.New("Описание не запускалось. Повторите этот кадр.")
		}
		if errors.Is(err, inference.ErrResultExpired) {
			return output, errors.New("Срок хранения ответа истёк. Повторите описание кадра.")
		}
		return output, &loraCaptionInferencePending{message: "Получаем сохранённое описание.", cause: err}
	}
	defer clear(body)
	result, err := promptassistant.ParseCaptionResponse(metadata.Request, saved.ResponseStatus, body)
	if err != nil {
		return output, err
	}
	caption, err := validatedLoraCaption(input.TriggerWord, result.Caption, promptassistant.MaxLoraCaptionCharacters)
	if err != nil {
		return output, err
	}
	output = domain.LoraCaptionResult{Caption: caption, Model: result.Model, Warning: metadata.Warning}
	a.audit(ctx, &job.UserID, "lora_training_caption_generated", "lora_training_dataset", nil, "", "", map[string]any{"job_id": job.ID, "dataset_id": job.DatasetID, "image_id": job.ImageID, "model": result.Model, "instruction_version": job.InstructionVersion, "usage": result.Usage, "inference_id": request.ID})
	return output, nil
}
