package main

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

type captionPreviewSource struct {
	Image   domain.LoraDatasetImage `json:"image"`
	Trigger string                  `json:"trigger_word"`
	Concept string                  `json:"concept_type"`
}
type captionPreviewJob struct {
	domain.LoraCaptionJob
	domain.LoraCaptionResult
	Source captionPreviewSource `json:"source"`
}

// Deterministic HTTP fixture, not a second implementation of the production worker.
func (p *datasetPreview) serveCaptions(w http.ResponseWriter, r *http.Request, state *datasetPreviewSession, view datasetPreviewView) {
	write := func(code int, value any) { w.WriteHeader(code); json.NewEncoder(w).Encode(value) }
	if r.Method == "POST" {
		var input struct {
			Revision  int64    `json:"revision"`
			IDs       []string `json:"image_ids"`
			OnlyEmpty bool     `json:"only_empty"`
			Cancel    bool     `json:"cancel"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		if input.Cancel {
			for i := range state.captions {
				job := &state.captions[i]
				if job.DatasetID == view.Dataset.ID && (job.State == "queued" || job.State == "running") {
					job.State = "cancelled"
				}
			}
			write(200, map[string]bool{"cancelled": true})
			return
		}
		if input.Revision != view.Dataset.Revision {
			write(409, map[string]string{"error": "Набор изменён"})
			return
		}
		for _, item := range view.Manifest.Images {
			wanted := false
			for _, id := range input.IDs {
				if id == item.ID {
					wanted = true
				}
			}
			if !wanted || item.Excluded || (input.OnlyEmpty && strings.TrimSpace(item.Caption) != "") {
				continue
			}
			source := captionPreviewSource{item, view.Manifest.Settings.TriggerWord, view.Manifest.Settings.ConceptType}
			exists := false
			for _, job := range state.captions {
				if job.DatasetID == view.Dataset.ID && job.ImageID == item.ID && (job.Source == source || job.State == "queued" || job.State == "running") {
					exists = true
				}
			}
			if exists {
				continue
			}
			now := time.Now()
			state.captions = append(state.captions, captionPreviewJob{LoraCaptionJob: domain.LoraCaptionJob{ID: rand.Text(), DatasetID: view.Dataset.ID, ImageID: item.ID, State: "queued", CreatedAt: now, UpdatedAt: now, AvailableAt: now.Add(500 * time.Millisecond)}, Source: source})
		}
	} else {
		for i := range state.captions {
			job := &state.captions[i]
			if job.DatasetID != view.Dataset.ID || job.State != "queued" || time.Now().Before(job.AvailableAt) {
				continue
			}
			job.State = "completed"
			job.Caption = job.Source.Trigger + ", portrait with light hair, daylight, neutral background"
			job.UpdatedAt = time.Now()
			break
		}
	}
	jobs := []captionPreviewJob{}
	for _, job := range state.captions {
		if job.DatasetID == view.Dataset.ID {
			jobs = append(jobs, job)
		}
	}
	code := 200
	if r.Method == "POST" {
		code = 202
	}
	write(code, map[string]any{"jobs": jobs})
}
func (p *datasetPreview) captionAction(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.session(w, r)
	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/lora-training/caption/"), "/")
	for i := range state.captions {
		job := &state.captions[i]
		if job.ID != parts[0] {
			continue
		}
		if len(parts) == 2 && r.Method == "POST" {
			if parts[1] == "retry" && (job.State == "failed" || job.State == "cancelled") {
				job.State = "queued"
				job.AvailableAt = time.Now().Add(500 * time.Millisecond)
			}
			if parts[1] == "cancel" && (job.State == "queued" || job.State == "running") {
				job.State = "cancelled"
			}
		}
		json.NewEncoder(w).Encode(job)
		return
	}
	w.WriteHeader(404)
	json.NewEncoder(w).Encode(map[string]string{"error": "Задание не найдено"})
}
