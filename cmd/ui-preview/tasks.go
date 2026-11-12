package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/thumbnail"
)

// These read-only fixtures never connect to a database, model or work queue.
func registerTaskPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, now time.Time, generationJobs, generationBatches []map[string]any) {
	page := render("tasks", "Задачи", map[string]any{"TaskGenerationAccess": true, "TaskTrainingAccess": true})
	mux.HandleFunc("/preview/tasks", page)
	mux.HandleFunc("/tasks", page)
	mux.HandleFunc("/preview/task-photo.jpg", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "internal/promptassistant/testdata/visual-v1/images/03.jpg")
	})
	mux.HandleFunc("/preview/task-video.mp4", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "cmd/ui-preview/testdata/task-preview.mp4")
	})
	mux.HandleFunc("/preview/task-lora.safetensors", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="fixture.safetensors"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("fixture-only"))
	})
	const preview = "/preview/task-photo.jpg"
	rows := []domain.Task{
		{Kind: "generation", ID: "task-video-queued", Name: "MiniMax H3 · Движение камеры", State: "waiting_for_resources", Message: "Видеозадание ожидает доступ к GPU.", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-10 * time.Second)},
		{Kind: "lora_training", ID: "task-training-running", Name: "Свет предметной съёмки", State: "running", Stage: "Обучение адаптера", Message: "Шаг 976 из 1600.", CreatedAt: now.Add(-3 * time.Minute), UpdatedAt: now.Add(-8 * time.Second)},
		{Kind: "lora_caption", ID: "task-caption-failed", Name: "Предметы в студии", State: "failed", Message: "Не удалось получить описание кадра.", DatasetID: "task-dataset", ImageID: "task-frame-2", CreatedAt: now.Add(-4 * time.Minute), UpdatedAt: now.Add(-2 * time.Minute)},
		{Kind: "generation", ID: "task-image-completed", Name: "Krea2 · Предметная съёмка", State: "completed", Message: "Изображение сохранено.", CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-8 * time.Minute)},
		{Kind: "lora_caption", ID: "task-caption-completed", Name: "Предметы в студии", State: "completed", Message: "Описание готово.", DatasetID: "task-dataset", ImageID: "task-frame-1", CreatedAt: now.Add(-12 * time.Minute), UpdatedAt: now.Add(-11 * time.Minute)},
		{Kind: "generation", ID: "task-batch-variant-1", Name: "Krea2 · Варианты света", State: "completed", Message: "Вариант готов.", BatchID: "task-batch-preview", BatchPosition: 1, CreatedAt: now.Add(-15 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute)},
		{Kind: "generation", ID: "task-batch-variant-2", Name: "Krea2 · Варианты света", State: "running", Message: "Основная генерация.", BatchID: "task-batch-preview", BatchPosition: 2, CreatedAt: now.Add(-15 * time.Minute), UpdatedAt: now.Add(-3 * time.Second)},
		{Kind: "lora_training", ID: "task-training-completed", Name: "Коллекция керамики", State: "completed", Stage: "Готово", Message: "LoRA сохранена.", CreatedAt: now.Add(-35 * time.Minute), UpdatedAt: now.Add(-25 * time.Minute)},
		{Kind: "generation", ID: "task-cancellation-pending", Name: "Flux2 · Редактирование изображения", State: "running", Message: "Ожидаем подтверждения отмены.", CancellationPending: true, CreatedAt: now.Add(-40 * time.Minute), UpdatedAt: now.Add(-5 * time.Second)},
		{Kind: "generation", ID: "task-generation-expired", Name: "Krea2 · Сохранённые параметры", State: "expired", Message: "Срок хранения результата истёк.", CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-time.Minute)},
		{Kind: "generation", ID: "task-video-completed", Name: "MiniMax H3 · Керамика", State: "completed", Message: "Видео сохранено.", CreatedAt: now.Add(-20 * time.Minute), UpdatedAt: now.Add(-18 * time.Minute)},
	}
	progress := 61
	rows[1].Progress = &progress
	expires := now.Add(23 * time.Hour)
	rows[3].ExpiresAt = &expires
	type thumbnailSource struct{ path, mime, label, sourceType string }
	thumbnailSources := make(map[string]thumbnailSource)
	thumbnailURL := func(task domain.Task) string {
		return "/api/tasks/thumbnail?" + url.Values{"kind": {task.Kind}, "id": {task.ID}, "ref": {"fixture-v1"}, "sensitive": {"0"}}.Encode()
	}
	for _, task := range rows {
		source := thumbnailSource{path: "internal/promptassistant/testdata/visual-v1/images/03.jpg", mime: "image/jpeg", sourceType: "image"}
		switch {
		case task.Kind == "lora_training":
			source.label = "Кадр исходного набора обучения"
			source.sourceType = "dataset"
		case task.Kind == "lora_caption":
			source.label = "Исходный кадр описания"
		case task.ID == "task-video-completed":
			source.path, source.mime, source.label = "cmd/ui-preview/testdata/task-preview.mp4", "video/mp4", "Первый кадр видео"
			source.sourceType = "video"
		case task.Kind == "generation" && task.State == "completed":
			source.label = "Результат генерации"
		default:
			continue
		}
		thumbnailSources[task.Kind+":"+task.ID] = source
	}
	for _, job := range generationJobs {
		media, _ := job["media"].([]map[string]any)
		id, _ := job["job_id"].(string)
		if len(media) == 0 || media[0]["media_type"] != "image" {
			continue
		}
		media[0]["url"] = "/preview/studio-result.jpg"
		thumbnailSources["generation:"+id] = thumbnailSource{path: "docs/frontend/prototype/assets/portrait.jpg", mime: "image/jpeg", sourceType: "image"}
		job["preview"] = map[string]any{"url": thumbnailURL(domain.Task{Kind: "generation", ID: id}), "source_type": "image", "label": "Результат генерации", "sensitive": false, "expires_unix": now.Add(24 * time.Hour).UnixMilli()}
	}
	thumbnailCache := thumbnail.NewCache()
	mux.HandleFunc("/api/tasks/thumbnail", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		key := r.URL.Query().Get("kind") + ":" + r.URL.Query().Get("id")
		source, ok := thumbnailSources[key]
		if !ok || r.Method != http.MethodGet || r.URL.Query().Get("ref") != "fixture-v1" || r.URL.Query().Get("sensitive") != "0" {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		data, err := thumbnailCache.Get(ctx, key, time.Now().Add(time.Hour), func(ctx context.Context) ([]byte, error) { return thumbnail.Render(ctx, source.path, source.mime) })
		if err != nil {
			http.Error(w, "thumbnail fixture unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	details := make(map[string]map[string]any)
	for _, task := range rows {
		data := map[string]any{"kind": task.Kind, "id": task.ID, "cancellation_pending": task.CancellationPending}
		terminal := task.State == "completed" || task.State == "failed" || task.State == "expired"
		switch task.Kind {
		case "generation":
			media := []map[string]any{}
			if task.State == "completed" {
				if task.ID == "task-video-completed" {
					media = append(media, map[string]any{"url": "/preview/task-video.mp4", "filename": "ceramic-study.mp4", "media_type": "video", "sensitive": false})
				} else {
					media = append(media, map[string]any{"url": preview, "filename": "ceramic-study.jpg", "media_type": "image", "sensitive": false})
				}
			}
			clientState := task.State
			if task.CancellationPending {
				clientState = "cancelling"
			}
			data["generation"] = map[string]any{"job_id": task.ID, "model_name": task.Name, "job_state": task.State, "state": clientState, "message": task.Message,
				"created_at": task.CreatedAt, "updated_at": task.UpdatedAt, "expires_at": task.ExpiresAt, "seed": 847123, "attempt": 1, "prompt": "A ceramic cup viewed from above in soft daylight.",
				"batch_id": task.BatchID, "batch_position": task.BatchPosition, "media": media, "cancellable": !terminal && !task.CancellationPending, "retryable": terminal}
			data["open_url"] = "/preview/generate?job=" + task.ID
			if terminal {
				data["edit_url"] = "/preview/generate?edit_job=" + task.ID
			}
			if !terminal && !task.CancellationPending {
				data["cancel_url"] = "/generate/jobs/cancel"
			}
		case "lora_training":
			job := map[string]any{"id": task.ID, "name": task.Name, "state": task.State, "stage": task.Stage, "message": task.Message,
				"created_at": task.CreatedAt.UnixMilli(), "updated_at": task.UpdatedAt.UnixMilli(), "sample_count": 24, "base_model": "Krea2 Raw", "resolution": 768, "max_train_steps": 1600, "progress": 61, "can_cancel": !terminal}
			if terminal {
				job["progress"] = 100
				job["can_download"] = true
				job["download_url"] = "/preview/task-lora.safetensors"
				job["artifact_name"] = "ceramic-study.safetensors"
			}
			data["training"] = job
			if !terminal {
				data["cancel_url"] = "/train-lora/" + task.ID + "/cancel"
			}
		case "lora_caption":
			job := map[string]any{"job_id": task.ID, "state": task.State, "status": task.Message, "created_at": task.CreatedAt, "updated_at": task.UpdatedAt, "expires_at": expires,
				"dataset_id": task.DatasetID, "image_id": task.ImageID, "source": map[string]any{"trigger_word": "ceramicset", "image": map[string]any{"name": "Керамическая чашка"}}, "model": "Vision preview"}
			if task.State == "completed" {
				job["caption"] = "ceramicset, a cup viewed from above, circular rim and dark liquid, soft natural light"
			}
			if task.State == "failed" {
				job["error"] = "Исполнитель остановлен. Можно повторить описание этого кадра."
				data["retry_url"] = "/api/lora-training/caption/" + task.ID + "/retry"
			}
			data["caption"] = job
			data["preview_url"] = preview
		}
		details[task.Kind+":"+task.ID] = data
	}
	details["generation_batch:task-batch-preview"] = map[string]any{"kind": "generation_batch", "id": "task-batch-preview", "cancel_url": "/generate/batches/cancel",
		"batch": map[string]any{"batch_id": "task-batch-preview", "model_name": "Krea2 · Варианты света", "state": "running", "total_count": 2, "completed_count": 1, "failed_count": 0, "cancelled_count": 0, "parameter_label": "Шаги", "created_at": now.Add(-15 * time.Minute), "updated_at": now.Add(-3 * time.Second),
			"jobs": []any{details["generation:task-batch-variant-1"]["generation"], details["generation:task-batch-variant-2"]["generation"]}}}
	details["lora_caption_series:task-caption-series-preview"] = map[string]any{"kind": "lora_caption_series", "id": "task-caption-series-preview",
		"caption_series": map[string]any{"id": "task-caption-series-preview", "dataset_id": "task-dataset", "name": "Предметы в студии", "state": "partial", "total_count": 3, "completed_count": 1, "failed_count": 1, "cancelled_count": 1, "unavailable_count": 0,
			"created_at": now.Add(-15 * time.Minute), "updated_at": now.Add(-2 * time.Minute), "expires_at": now.Add(30 * 24 * time.Hour),
			"members": []map[string]any{{"job_id": "task-caption-completed", "image_id": "task-frame-1", "position": 1, "state": "completed", "available": true}, {"job_id": "task-caption-failed", "image_id": "task-frame-2", "position": 2, "state": "failed", "available": true}, {"job_id": "old-caption-attempt", "image_id": "task-frame-3", "position": 3, "state": "cancelled", "available": false}}}}
	for _, id := range []string{"task-caption-completed", "task-caption-failed"} {
		details["lora_caption:"+id]["series_url"] = "/tasks?task=lora_caption_series:task-caption-series-preview"
	}
	rows = append(rows, domain.Task{Kind: "lora_caption_series", ID: "task-caption-series-preview", Name: "Предметы в студии", State: "partial", DatasetID: "task-dataset",
		CreatedAt: now.Add(-15 * time.Minute), UpdatedAt: now.Add(-2 * time.Minute), Series: &domain.TaskSeriesSummary{Total: 3, Completed: 1, Failed: 1, Cancelled: 1}})
	for index := 1; index <= 42; index++ {
		id, name := fmt.Sprintf("task-caption-series-history-%02d", index), fmt.Sprintf("Съёмка керамики · серия %02d", index)
		created, expires := now.Add(-time.Duration(index)*time.Hour), now.Add(20*24*time.Hour)
		status := "completed"
		summary := &domain.TaskSeriesSummary{Total: 3, Completed: 3}
		members := []map[string]any{{"job_id": "retained-attempt-1", "image_id": "prior-frame-1", "position": 1, "state": "completed", "available": false}, {"job_id": "retained-attempt-2", "image_id": "prior-frame-2", "position": 2, "state": "completed", "available": false}, {"job_id": "retained-attempt-3", "image_id": "prior-frame-3", "position": 3, "state": "completed", "available": false}}
		if index%2 == 0 {
			status = "partial"
			summary.Completed, summary.Unavailable = 2, 1
			members[2]["state"] = "unavailable"
		}
		rows = append(rows, domain.Task{Kind: "lora_caption_series", ID: id, Name: name, State: status, CreatedAt: created, UpdatedAt: created, FinishedAt: &created, ExpiresAt: &expires, Series: summary})
		details["lora_caption_series:"+id] = map[string]any{"kind": "lora_caption_series", "id": id, "open_reason": "Исходный набор больше недоступен.",
			"caption_series": map[string]any{"id": id, "name": name, "state": status, "total_count": summary.Total, "completed_count": summary.Completed, "failed_count": 0, "cancelled_count": 0, "unavailable_count": summary.Unavailable,
				"created_at": created, "updated_at": created, "finished_at": created, "expires_at": expires, "members": members}}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return rows[i].Kind+":"+rows[i].ID > rows[j].Kind+":"+rows[j].ID
	})
	write := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	gpuSnapshot := func(kind, id string) *domain.TaskGPUStatus {
		status := &domain.TaskGPUStatus{SnapshotAt: time.Now().UTC()}
		switch kind + ":" + id {
		case "generation:task-video-queued":
			status.Code, status.Position = "gpu_in_use", 2
		case "lora_training:task-training-running":
			status.Code = "gpu_running"
		case "generation:task-batch-variant-2":
			status.Code = "gpu_reserved"
		case "generation:task-cancellation-pending":
			status.Code = "executor_unconfirmed"
		default:
			return nil
		}
		return status
	}
	mux.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		matches := []domain.Task{}
		for _, task := range rows {
			if task.Kind == "lora_caption_series" && query.Get("kind") != "lora_caption_series" {
				continue
			}
			if kind := query.Get("kind"); kind != "" && task.Kind != kind {
				continue
			}
			if state := query.Get("state"); state != "" && task.State != state {
				continue
			}
			if search := strings.ToLower(query.Get("search")); search != "" && !strings.Contains(strings.ToLower(task.Name+task.ID), search) {
				continue
			}
			if from, err := time.Parse(time.RFC3339, query.Get("from")); err == nil && task.CreatedAt.Before(from) {
				continue
			}
			if before, err := time.Parse(time.RFC3339, query.Get("before")); err == nil && !task.CreatedAt.Before(before) {
				continue
			}
			matches = append(matches, task)
		}
		offset, _ := strconv.Atoi(query.Get("cursor"))
		if offset < 0 || offset > len(matches) {
			offset = 0
		}
		end := offset + 40
		if end > len(matches) {
			end = len(matches)
		}
		next := ""
		if end < len(matches) {
			next = strconv.Itoa(end)
		}
		items := make([]any, 0, end-offset)
		for _, task := range matches[offset:end] {
			var image any
			nextAction := "inspect"
			data := details[task.Kind+":"+task.ID]
			if data["cancel_url"] != nil {
				nextAction = "cancel"
			} else if task.Kind == "generation" && (task.State == "completed" || task.State == "failed" || task.State == "expired") {
				nextAction = "edit_generation"
			} else if task.Kind == "lora_training" && task.State == "completed" {
				nextAction = "download_training"
			}
			if source, ok := thumbnailSources[task.Kind+":"+task.ID]; ok {
				image = map[string]any{"url": thumbnailURL(task), "label": source.label, "source_type": source.sourceType, "sensitive": false, "expires_unix": time.Now().Add(time.Hour).UnixMilli()}
			}
			items = append(items, struct {
				domain.Task
				Preview    any    `json:"preview,omitempty"`
				NextAction string `json:"next_action"`
			}{task, image, nextAction})
		}
		write(w, map[string]any{"schema_version": 1, "tasks": items, "total": len(matches), "revision": 71, "next_cursor": next, "access": map[string]bool{"generation": true, "training": true}})
	})
	mux.HandleFunc("/api/tasks/detail", func(w http.ResponseWriter, r *http.Request) {
		data, ok := details[r.URL.Query().Get("kind")+":"+r.URL.Query().Get("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		response := make(map[string]any, len(data)+1)
		for key, value := range data {
			response[key] = value
		}
		if gpu := gpuSnapshot(r.URL.Query().Get("kind"), r.URL.Query().Get("id")); gpu != nil {
			response["gpu"] = gpu
		}
		write(w, response)
	})
	for _, job := range generationJobs {
		id, _ := job["job_id"].(string)
		details["generation:"+id] = map[string]any{"kind": "generation", "id": id, "job": job, "open_url": "/preview/generate?job=" + url.QueryEscape(id)}
	}
	for _, batch := range generationBatches {
		id, _ := batch["batch_id"].(string)
		details["generation_batch:"+id] = map[string]any{"kind": "generation_batch", "id": id, "batch": batch, "open_url": "/preview/generate?batch=" + url.QueryEscape(id), "open_label": "Открыть пакет в студии"}
	}
	details["generation_batch:task-batch-preview"]["open_url"] = "/preview/generate?batch=task-batch-preview"
	details["generation_batch:task-batch-preview"]["open_label"] = "Открыть пакет в студии"
	mux.HandleFunc("/api/tasks/gpu", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		kind, id := r.URL.Query().Get("kind"), r.URL.Query().Get("id")
		if _, ok := details[kind+":"+id]; !ok || (kind != "generation" && kind != "lora_training" && kind != "lora_caption") {
			w.WriteHeader(http.StatusNotFound)
			write(w, map[string]any{"error": "Задача больше не доступна."})
			return
		}
		write(w, map[string]any{"kind": kind, "id": id, "gpu": gpuSnapshot(kind, id)})
	})
	mux.HandleFunc("/api/tasks/gpu-snapshots", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		keys := r.URL.Query()["task"]
		if len(keys) == 0 || len(keys) > domain.TaskGPUSnapshotLimit {
			w.WriteHeader(http.StatusBadRequest)
			write(w, map[string]string{"error": "invalid_task_query"})
			return
		}
		batch := domain.TaskGPUSnapshots{SnapshotAt: time.Now().UTC(), Tasks: make([]domain.TaskGPUSnapshotEntry, 0, len(keys))}
		for _, key := range keys {
			kind, id, _ := strings.Cut(key, ":")
			_, available := details[key]
			available = available && (kind == "generation" || kind == "lora_training" || kind == "lora_caption")
			gpu := gpuSnapshot(kind, id)
			if gpu != nil {
				gpu.SnapshotAt = batch.SnapshotAt
			}
			batch.Tasks = append(batch.Tasks, domain.TaskGPUSnapshotEntry{TaskGPUKey: domain.TaskGPUKey{Kind: kind, ID: id}, Available: available, GPU: gpu})
		}
		write(w, batch)
	})
	mux.HandleFunc("/api/tasks/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = w.Write([]byte("event: ready\ndata: 71\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
}
