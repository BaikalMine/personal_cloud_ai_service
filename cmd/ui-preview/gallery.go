package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"ai-access-gateway/internal/mediaprobe"
)

var galleryFixturePaths = []string{"docs/frontend/prototype/assets/portrait.jpg", "docs/frontend/prototype/assets/portrait-2.jpg", "docs/frontend/prototype/assets/portrait-2.jpg", "internal/promptassistant/testdata/visual-v1/images/03.jpg", "cmd/ui-preview/testdata/task-preview.mp4"}
var galleryFixtureURLs = []string{"/preview/studio-result.jpg", "/preview/dataset-media/portrait-2.jpg", "/preview/dataset-media/portrait-2.jpg", "/preview/task-photo.jpg", "/preview/task-video.mp4"}

func galleryPreviewExactImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.URL.Query().Get("media"))
	if err != nil || id < 1 || id > 4 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"media_id": strconv.Itoa(id), "images": []map[string]any{{"id": id, "url": galleryFixtureURLs[id-1], "filename": fmt.Sprintf("result-%d.jpg", id), "model_name": "Krea2", "expires_unix": time.Now().Add(24 * time.Hour).UnixMilli(), "sensitive": id == 3}}})
}

func registerGalleryPreview(mux *http.ServeMux, items []map[string]any) {
	for index, item := range items {
		if media, ok := item["Media"].(map[string]any); ok && index < len(galleryFixturePaths) {
			media["URL"] = galleryFixtureURLs[index]
			media["ExpiresUnix"] = time.Now().Add(30 * 24 * time.Hour).UnixMilli()
			if info, err := os.Stat(galleryFixturePaths[index]); err == nil {
				media["SizeBytes"] = info.Size()
			}
		}
	}
	mux.HandleFunc("/generate/library/destinations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"media_id": r.URL.Query().Get("media"), "can_dataset": true, "catalog_online": true, "presets": []map[string]any{
			{"id": "photoflow-krea2-edit", "key": "photoflow-krea2-edit:krea2:test", "model_id": "krea2:test", "template_id": "image-to-image", "name": "Krea2", "maximum": 2, "frames": true},
			{"id": "photoflow-flux2-edit", "key": "photoflow-flux2-edit:flux2:test", "model_id": "flux2:test", "template_id": "image-to-image", "name": "Flux2", "maximum": 4, "frames": true},
			{"id": "minimax-h3-video", "key": "minimax-h3-video:minimax:h3", "model_id": "minimax:h3", "template_id": "minimax-h3-video", "name": "MiniMax H3", "maximum": 4, "frames": true},
			{"id": "minimax-h3-video", "key": "minimax-h3-video:minimax:eros", "model_id": "minimax:eros", "template_id": "minimax-h3-video", "name": "MiniMax Eros", "maximum": 4, "frames": false},
		}})
	})
	find := func(jobID string) (int, map[string]any) {
		for index, item := range items {
			if item["JobID"] == jobID && index < len(galleryFixturePaths) {
				return index, item
			}
		}
		return -1, nil
	}
	mux.HandleFunc("/generate/jobs/passport", func(w http.ResponseWriter, r *http.Request) {
		if postprocessPreviewPassport(w, r) {
			return
		}
		index, item := find(r.URL.Query().Get("job"))
		if item == nil {
			http.NotFound(w, r)
			return
		}
		media := item["Media"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"version": 1, "job_id": item["JobID"], "seed": fmt.Sprint(item["Seed"]), "evidence_state": "submission_recorded", "submission_confirmed": true,
			"execution": map[string]any{"version": 1, "seed": fmt.Sprint(item["Seed"]), "workflow": map[string]any{"id": item["WorkflowID"]}, "resolved_input": map[string]any{"Positive": item["Prompt"], "ModelName": item["ModelName"], "Steps": 8, "CFG": 1, "Width": 1024, "Height": 1024}, "components": []any{}, "graph": map[string]any{"1": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"seed": item["Seed"]}}}},
			"outputs":   []map[string]any{{"media_id": strconv.Itoa(index + 1), "media_type": media["MediaType"], "measurement_state": "not_recorded"}}, "outputs_truncated": false})
	})
	mux.HandleFunc("/generate/jobs/output-measurement", func(w http.ResponseWriter, r *http.Request) {
		index, item := find(r.URL.Query().Get("job"))
		postprocess := r.URL.Query().Get("job") == "postprocess-preview-image" && r.URL.Query().Get("media") == "601" || r.URL.Query().Get("job") == "postprocess-preview-video" && r.URL.Query().Get("media") == "602"
		if postprocess {
			index = 0
			if r.URL.Query().Get("media") == "602" {
				index = 4
			}
		} else if item == nil || r.URL.Query().Get("media") != strconv.Itoa(index+1) {
			http.NotFound(w, r)
			return
		}
		mime := "image/jpeg"
		if index == 4 {
			mime = "video/mp4"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		measurement, err := mediaprobe.Probe(ctx, galleryFixturePaths[index], mime)
		if err != nil {
			http.Error(w, "measurement unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"version": 1, "job_id": r.URL.Query().Get("job"), "media_id": r.URL.Query().Get("media"), "measurement_state": "measured", "measurement": measurement})
	})
}
