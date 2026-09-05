package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/store"
)

func (a *App) recoverLoraTrainingSubmission(ctx context.Context, job domain.LoraTrainingJob) (domain.LoraTrainingJob, bool, error) {
	status, err := a.loraTraining.StatusByGatewayID(ctx, job.PublicID)
	if err == nil {
		if !loraTrainingStateFromAgent(status.State).Valid() || status.ExecutionUnconfirmed {
			return job, false, fmt.Errorf("LoRA training %s executor state is unconfirmed", job.PublicID)
		}
		if err := a.store.AttachLoraTrainingAgentJob(ctx, job.ID, status.ID, truncateLoraText(status.Stage, 120), "Восстановлена связь с принятым заданием.", clampLoraProgress(status.Progress)); err != nil {
			return job, false, err
		}
		recovered, err := a.store.LoraTrainingJobByID(ctx, job.ID)
		return recovered, false, err
	}
	var httpErr *loratraining.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound || httpErr.Code != "job_not_found" {
		return job, false, err
	}
	// A missing record alone is not proof: fence late uploads before releasing.
	confirmation, err := a.loraTraining.FenceSubmission(ctx, job.PublicID)
	if err != nil {
		return job, false, err
	}
	if !confirmation.Settled {
		return job, false, fmt.Errorf("LoRA training %s is stopping after an uncertain handoff", job.PublicID)
	}
	if confirmation.Job != nil {
		if err := a.store.AttachLoraTrainingAgentJob(ctx, job.ID, confirmation.Job.ID, "Проверка завершена", "Агент подтвердил исход передачи.", 100); err != nil {
			return job, false, err
		}
	}
	state := domain.LoraTrainingFailed
	message := "Передача агенту не завершилась. Поздний запуск заблокирован; обучение можно запустить заново из сохранённого датасета."
	if job.CancellationRequestedAt != nil {
		state = domain.LoraTrainingCancelled
		message = "Отмена подтверждена агентом; поздний запуск заблокирован."
	}
	params := store.UpdateLoraTrainingJobParams{State: state, Stage: loraTrainingStateLabel(state), Progress: 100, Message: message}
	if confirmation.Job != nil && confirmation.Job.State == "completed" {
		params.State, params.Stage = domain.LoraTrainingCompleted, "Готово"
		params.Message = truncateLoraText(confirmation.Job.Message, 1000)
		params.ArtifactName, params.ArtifactBytes = truncateLoraText(confirmation.Job.ArtifactName, 255), confirmation.Job.ArtifactBytes
	}
	if err := a.store.UpdateLoraTrainingJob(ctx, job.ID, params); err != nil {
		return job, false, err
	}
	a.removeLoraTrainingDataset(job)
	if err := a.store.ClearLoraTrainingDatasetPath(ctx, job.ID); err != nil {
		return job, true, err
	}
	a.releaseMiningPauseForLoraTraining(ctx, job.ID)
	return job, true, nil
}
