package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"time"
)

// Presentation fixtures only: no claim of a real postprocessing/GPU execution.
func postprocessPreviewView(video bool) map[string]any {
	mediaType, multiplier, numerator, frames := "image", 1, 0, 1
	if video {
		mediaType, multiplier, numerator, frames = "video", 2, 60000, 60
	}
	denominator := 0
	if video {
		denominator = 1001
	}
	return map[string]any{"version": 1, "plan": map[string]any{"version": 1, "media_type": mediaType,
		"options":       map[string]any{"width": 2560, "height": 1664, "interpolate": multiplier, "rtx_quality": "HIGH"},
		"fps_numerator": numerator, "fps_denominator": denominator, "output_frames": frames},
		"sources": []map[string]any{{"job_id": "gallery-preview-job-000", "media_id": "1", "availability": "available", "measurement": map[string]any{"width": 1280, "height": 832, "frames": 1}}}}
}

func postprocessPreviewPassport(w http.ResponseWriter, r *http.Request) bool {
	id := r.URL.Query().Get("job")
	if !strings.HasPrefix(id, "postprocess-preview-") {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	mediaID := "601"
	if strings.Contains(id, "video") {
		mediaID = "602"
	}
	json.NewEncoder(w).Encode(map[string]any{"version": 1, "job_id": id, "operation": "postprocess", "evidence_state": "submission_recorded", "submission_confirmed": true,
		"postprocess": postprocessPreviewView(strings.Contains(id, "video")), "execution": map[string]any{"version": 2, "postprocess": map[string]any{"output_node": "6"}},
		"parent_job_id": "gallery-preview-job-000", "parent_available": true, "outputs": []any{map[string]any{"media_id": mediaID}}})
	return true
}

func registerPostprocessPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, originals []map[string]any, now time.Time) {
	items := []map[string]any{originals[0]}
	for index, name := range []string{"image", "video", "retained", "failed"} {
		video := name == "video"
		view := postprocessPreviewView(video)
		id, template, label := "postprocess-preview-"+name, "postprocess-image-v1", "Постобработка изображения"
		if video {
			template, label = "postprocess-video-v1", "Постобработка видео"
		}
		if name == "retained" {
			id = ""
			source := view["sources"].([]map[string]any)[0]
			source["availability"], source["media_id"] = "not_checked", "9007199254740993"
		}
		encoded, _ := json.Marshal(view)
		item := map[string]any{"VariantID": 601 + index, "JobID": id, "TemplateID": template, "WorkflowID": "postprocess-v1", "WorkflowName": "Обработка готового файла", "Scenario": label,
			"ModelName": label, "Prompt": "", "State": "completed", "CreatedAt": now.Add(-time.Duration(index+1) * time.Minute), "HasMedia": name != "failed", "CompareCount": 1, "Postprocess": view, "PostprocessJSON": string(encoded)}
		media := maps.Clone(originals[0]["Media"].(map[string]any))
		if video {
			media = maps.Clone(originals[4]["Media"].(map[string]any))
		}
		media["ID"], media["Filename"], media["Pinned"], media["Favorite"] = 601+index, "postprocess-"+name+map[bool]string{true: ".mp4", false: ".jpg"}[video], false, false
		media["ReferenceUses"] = nil
		item["Media"] = media
		if name == "failed" {
			item["State"], item["ErrorMessage"] = "failed", "Исходный файл больше недоступен."
		}
		items = append(items, item)
	}
	registerPostprocessDialogPreview(mux, originals, items)
	mux.HandleFunc("/preview/gallery-postprocess", render("gallery", "Результаты", map[string]any{"Items": items, "Collections": []any{}, "ImageCount": 3, "VideoCount": 1, "PinnedCount": 1, "FavoriteCount": 1, "ErrorCount": 1, "CanReuseImages": true}))
	mux.HandleFunc("/preview/postprocess/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		job := map[string]any{"job_id": "postprocess-preview-image", "request_id": "postprocess-preview-request", "operation": "postprocess", "template_id": "postprocess-image-v1", "workflow_id": "postprocess-v1",
			"state": "completed", "job_state": "completed", "message": "Файл обработан и сохранён.", "created_at": now, "updated_at": now, "duration_seconds": 12, "attempt": 1, "retryable": false, "cancellable": false,
			"postprocess": postprocessPreviewView(false), "media": []map[string]any{{"id": 601, "url": "/preview/studio-result.jpg", "media_type": "image", "filename": "postprocess-image.jpg", "expires_unix": now.Add(24 * time.Hour).UnixMilli()}}}
		json.NewEncoder(w).Encode(map[string]any{"job": job, "variant": map[string]any{"id": 601, "operation": "postprocess", "postprocess": postprocessPreviewView(false)},
			"detail": map[string]any{"kind": "generation", "id": job["job_id"], "generation": job, "open_url": "/gallery?media=601", "open_label": "Открыть результат"},
			"task":   map[string]any{"kind": "generation", "id": job["job_id"], "name": "Постобработка изображения", "state": "completed", "created_at": now, "updated_at": now, "next_action": "inspect"}})
	})
}
