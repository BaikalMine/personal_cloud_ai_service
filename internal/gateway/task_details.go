package gateway

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
)

type taskDetailView struct {
	Kind                string                    `json:"kind"`
	ID                  string                    `json:"id"`
	GPU                 *domain.TaskGPUStatus     `json:"gpu,omitempty"`
	Generation          *generationJobView        `json:"generation,omitempty"`
	Training            *loraTrainingJobJSON      `json:"training,omitempty"`
	Caption             *loraCaptionJobView       `json:"caption,omitempty"`
	Batch               *generationBatchView      `json:"batch,omitempty"`
	CaptionSeries       *domain.LoraCaptionSeries `json:"caption_series,omitempty"`
	SeriesURL           string                    `json:"series_url,omitempty"`
	CancelURL           string                    `json:"cancel_url,omitempty"`
	RetryURL            string                    `json:"retry_url,omitempty"`
	RetryReason         string                    `json:"retry_reason,omitempty"`
	OpenURL             string                    `json:"open_url,omitempty"`
	EditURL             string                    `json:"edit_url,omitempty"`
	OpenLabel           string                    `json:"open_label,omitempty"`
	OpenReason          string                    `json:"open_reason,omitempty"`
	PreviewURL          string                    `json:"preview_url,omitempty"`
	CancellationPending bool                      `json:"cancellation_pending"`
}

func (a *App) loadTaskDetail(ctx context.Context, scope domain.TaskScope, kind, id string) (taskDetailView, error) {
	view := taskDetailView{Kind: kind, ID: id}
	switch kind {
	case "generation":
		if !scope.Generation {
			return view, sql.ErrNoRows
		}
		job, err := a.store.GenerationJobByPublicID(ctx, scope.UserID, id)
		if err != nil {
			return view, err
		}
		jobs, err := a.generationJobViews(ctx, []domain.GenerationJob{job}, scope.UserID)
		if err != nil {
			return view, err
		}
		view.Generation = &jobs[0]
		view.OpenURL = "/generate?job=" + url.QueryEscape(id)
		if isPostprocessJob(job) {
			view.OpenURL, view.OpenLabel = "", "Открыть результат"
			if len(jobs[0].Media) > 0 {
				view.OpenURL = "/gallery?media=" + strconv.FormatInt(jobs[0].Media[0].ID, 10)
			}
			if jobs[0].Postprocess != nil {
				if err := a.resolvePostprocessViewSources(ctx, scope.UserID, jobs[0].Postprocess); err != nil {
					return view, err
				}
			}
		}
		if jobs[0].Retryable {
			view.EditURL = "/generate?edit_job=" + url.QueryEscape(id)
		}
		if jobs[0].Cancellable {
			view.CancelURL = "/generate/jobs/cancel"
		}
	case "lora_training":
		if !scope.Training {
			return view, sql.ErrNoRows
		}
		job, err := a.store.LoraTrainingJobByPublicID(ctx, id, scope.UserID, false)
		if err != nil {
			return view, err
		}
		data := loraTrainingJSON(job, nil, false)
		if job.AgentJobID != "" && !job.State.Terminal() && a.loraTraining != nil {
			statusCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			status, statusErr := a.loraTraining.Status(statusCtx, job.AgentJobID)
			cancel()
			if statusErr == nil && status.ID == job.AgentJobID && status.GatewayJobID == job.PublicID && status.State == "running" {
				if step, ok := trainingStepFromLog(status.LogTail, job.MaxTrainSteps); ok {
					total := job.MaxTrainSteps
					data.TrainingStep, data.TrainingTotalSteps = &step, &total
				}
			}
		}
		data.CanCancel = job.State.Cancellable() && job.CancellationRequestedAt == nil
		data.CancelURL = ""
		if data.CanCancel {
			view.CancelURL = "/train-lora/" + url.PathEscape(id) + "/cancel"
			data.CancelURL = view.CancelURL
		}
		view.Training = &data
		view.CancellationPending = job.CancellationRequestedAt != nil && !job.State.Terminal()
		view.OpenReason = "Исходная версия набора больше недоступна."
		if job.DatasetSnapshotID != "" {
			_, err := a.store.LoraDatasetSnapshot(ctx, scope.UserID, job.DatasetSnapshotID)
			if err == nil {
				view.OpenURL = "/train-lora?version=" + url.QueryEscape(job.DatasetSnapshotID)
				view.OpenLabel, view.OpenReason = "Открыть исходную версию", ""
			} else if !errors.Is(err, sql.ErrNoRows) {
				return view, err
			}
		}
	case "lora_caption":
		if !scope.Training {
			return view, sql.ErrNoRows
		}
		job, err := a.store.LoraCaptionJob(ctx, scope.UserID, id)
		if err != nil {
			return view, err
		}
		data, err := a.loraCaptionJobView(job)
		if err != nil {
			return view, err
		}
		view.Caption = &data
		view.OpenURL, view.OpenReason, err = a.taskDatasetEditorURL(ctx, scope.UserID, job.DatasetID, job.ImageID, job.AssetID)
		if err != nil {
			return view, err
		}
		view.OpenLabel = "Открыть кадр в редакторе"
		seriesID, err := a.store.LatestLoraCaptionSeries(ctx, scope.UserID, id, job.RunVersion)
		if err != nil {
			return view, err
		}
		if seriesID != "" {
			view.SeriesURL = "/tasks?task=lora_caption_series:" + url.QueryEscape(seriesID)
		}
		view.PreviewURL = "/api/lora-datasets/assets/" + url.PathEscape(job.AssetID)
		if (job.State == "queued" || job.State == "running") && !job.CancelRequested {
			view.CancelURL = "/api/lora-training/caption/" + url.PathEscape(id) + "/cancel"
		}
		if job.State == "failed" || job.State == "cancelled" {
			settled := true
			if job.InferenceWorkID != "" {
				settled, err = a.store.GPUInferenceSettled(ctx, job.InferenceWorkID)
				if err != nil {
					return view, err
				}
			}
			if settled {
				view.RetryURL = "/api/lora-training/caption/" + url.PathEscape(id) + "/retry"
			} else {
				view.RetryReason = "Завершение предыдущего описания ещё не подтверждено."
			}
		}
	case "lora_caption_series":
		if !scope.Training {
			return view, sql.ErrNoRows
		}
		series, err := a.store.LoraCaptionSeries(ctx, scope.UserID, id)
		if err != nil {
			return view, err
		}
		view.CaptionSeries = &series
		view.OpenURL, view.OpenReason, err = a.taskDatasetEditorURL(ctx, scope.UserID, series.DatasetID, "", "")
		if err != nil {
			return view, err
		}
		view.OpenLabel = "Открыть рабочий набор"
	case "generation_batch":
		if !scope.Generation {
			return view, sql.ErrNoRows
		}
		batch, err := a.store.GenerationBatchByPublicID(ctx, scope.UserID, id)
		if err != nil {
			return view, err
		}
		jobs, err := a.store.GenerationBatchJobs(ctx, scope.UserID, batch.ID)
		if err != nil {
			return view, err
		}
		children, err := a.generationJobViews(ctx, jobs, scope.UserID)
		if err != nil {
			return view, err
		}
		for i := range children {
			children[i].BatchID = batch.PublicID
		}
		data := makeGenerationBatchView(batch, children)
		view.Batch = &data
		view.OpenURL = "/generate?batch=" + url.QueryEscape(batch.PublicID)
		view.OpenLabel = "Открыть пакет в студии"
		if data.Cancellable {
			view.CancelURL = "/generate/batches/cancel"
		}
	default:
		return view, sql.ErrNoRows
	}
	if kind == "generation" || kind == "lora_training" || kind == "lora_caption" {
		var err error
		view.GPU, err = a.store.TaskGPUStatus(ctx, scope, kind, id)
		if err != nil {
			return view, err
		}
	}
	return view, nil
}

func (a *App) taskDatasetEditorURL(ctx context.Context, userID int64, datasetID, imageID, assetID string) (string, string, error) {
	if datasetID == "" {
		return "", "Рабочий набор больше недоступен.", nil
	}
	dataset, err := a.store.LoraDataset(ctx, userID, datasetID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "Рабочий набор больше недоступен.", nil
	}
	if err != nil {
		return "", "", err
	}
	query := url.Values{"dataset": {dataset.ID}}
	if imageID != "" {
		manifest, err := a.decodeLoraDatasetManifest(dataset.ManifestCipher)
		if err != nil {
			return "", "", err
		}
		found := false
		for _, image := range manifest.Images {
			if image.ID == imageID && image.AssetID == assetID {
				found = true
				break
			}
		}
		if !found {
			return "", "Кадр удалён или заменён в рабочем наборе.", nil
		}
		query.Set("image", imageID)
		query.Set("asset", assetID)
	}
	return "/train-lora?" + query.Encode(), "", nil
}

func taskDetailIdentity(r *http.Request) (kind, id string, valid bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 2 || len(query["kind"]) != 1 || len(query["id"]) != 1 || len(query.Get("id")) > 200 || query.Get("id") == "" {
		return "", "", false
	}
	if !utf8.ValidString(query.Get("id")) || strings.ContainsRune(query.Get("id"), 0) {
		return "", "", false
	}
	return query.Get("kind"), query.Get("id"), true
}

func (a *App) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeGenerationError(w, http.StatusMethodNotAllowed, "Метод не поддерживается.")
		return
	}
	scope := taskAccessScope(a.currentUser(r))
	if scope.UserID <= 0 {
		writeGenerationError(w, http.StatusUnauthorized, "Требуется вход.")
		return
	}
	kind, id, valid := taskDetailIdentity(r)
	if !valid {
		writeGenerationError(w, http.StatusBadRequest, "Некорректный адрес задачи.")
		return
	}
	view, err := a.loadTaskDetail(r.Context(), scope, kind, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeGenerationError(w, http.StatusNotFound, "Задача больше не доступна.")
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "Не удалось загрузить задачу. Попробуйте ещё раз.")
		return
	}
	writeJSON(w, http.StatusOK, view)
}
