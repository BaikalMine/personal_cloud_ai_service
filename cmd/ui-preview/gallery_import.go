package main

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"

	"ai-access-gateway/internal/domain"
)

func (p *datasetPreview) galleryImport(w http.ResponseWriter, r *http.Request, state *datasetPreviewSession, parts []string) {
	write := func(code int, value any) { w.WriteHeader(code); json.NewEncoder(w).Encode(value) }
	if state.imports == nil {
		state.imports = map[string]domain.GalleryDatasetImportReceipt{}
	}
	respond := func(receipt domain.GalleryDatasetImportReceipt) {
		_, available := state.sets[receipt.DatasetID]
		write(200, map[string]any{"receipt": receipt, "dataset_available": available})
	}
	if r.Method == http.MethodGet {
		receipt, ok := state.imports[r.URL.Query().Get("request")]
		if !ok {
			write(404, map[string]string{"error": "not found"})
			return
		}
		respond(receipt)
		return
	}
	var input struct {
		RequestID string `json:"request_id"`
		MediaID   string `json:"media_id"`
		DatasetID string `json:"dataset_id"`
		Revision  int64  `json:"revision"`
		Name      string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		write(400, map[string]string{"error": "invalid"})
		return
	}
	if receipt, ok := state.imports[input.RequestID]; ok {
		respond(receipt)
		return
	}
	id, err := strconv.Atoi(input.MediaID)
	if err != nil || id < 1 || id > 4 {
		write(404, map[string]string{"error": "not found"})
		return
	}
	receipt := domain.GalleryDatasetImportReceipt{RequestID: input.RequestID, MediaID: int64(id), CreatedAt: time.Now()}
	if len(parts) == 2 && parts[1] == "discard" {
		receipt.Cancelled = true
		state.imports[input.RequestID] = receipt
		respond(receipt)
		return
	}
	data, err := os.ReadFile(galleryFixturePaths[id-1])
	if err != nil {
		write(500, map[string]string{"error": "file"})
		return
	}
	asset, err := p.asset(state, "result.jpg", data)
	if err != nil {
		write(500, map[string]string{"error": "image"})
		return
	}
	manifest := domain.LoraDatasetManifest{Version: 1, Settings: domain.LoraDatasetSettings{Name: input.Name, Resolution: 1024, Preset: "balanced"}, Images: []domain.LoraDatasetImage{}}
	if input.DatasetID == "" {
		input.DatasetID = rand.Text()
	} else {
		view, ok := state.sets[input.DatasetID]
		if !ok || view.Dataset.Revision != input.Revision {
			write(409, map[string]string{"error": "Датасет изменился."})
			return
		}
		manifest = view.Manifest
	}
	receipt.ImageID = rand.Text()
	for _, image := range manifest.Images {
		if image.AssetID == asset.ID {
			receipt.AlreadyPresent = true
			receipt.ImageID = image.ID
		}
	}
	if !receipt.AlreadyPresent {
		manifest.Images = append(manifest.Images, domain.LoraDatasetImage{ID: receipt.ImageID, AssetID: asset.ID})
	}
	view := p.save(state, input.DatasetID, manifest)
	receipt.DatasetID = view.Dataset.ID
	receipt.Revision = view.Dataset.Revision
	state.imports[input.RequestID] = receipt
	respond(receipt)
}
