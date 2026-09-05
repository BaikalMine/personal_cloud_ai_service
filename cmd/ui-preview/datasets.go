package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/domain"
)

type datasetPreviewView struct {
	Dataset  domain.LoraDatasetRow              `json:"dataset"`
	Manifest domain.LoraDatasetManifest         `json:"manifest"`
	Assets   map[string]domain.LoraDatasetAsset `json:"assets"`
	Warnings []any                              `json:"warnings"`
}
type datasetPreviewSession struct {
	sets     map[string]datasetPreviewView
	versions map[string]datasetPreviewView
	assets   map[string]domain.LoraDatasetAsset
	data     map[string][]byte
	jobs     []map[string]any
}
type datasetPreview struct {
	mu       sync.Mutex
	sessions map[string]*datasetPreviewSession
	revision int64
	now      time.Time
}

// Browser fixture only. Production persistence, ZIP validation, ownership and
// training archive integrity are exercised separately against PostgreSQL.
func registerDatasetPreview(mux *http.ServeMux, now time.Time) *datasetPreview {
	p := &datasetPreview{sessions: map[string]*datasetPreviewSession{}, now: now}
	mux.HandleFunc("/api/lora-datasets", p.serve)
	mux.HandleFunc("/api/lora-datasets/", p.serve)
	mux.Handle("/preview/dataset-media/", http.StripPrefix("/preview/dataset-media/", http.FileServer(http.Dir("docs/frontend/prototype/assets"))))
	return p
}
func (p *datasetPreview) session(w http.ResponseWriter, r *http.Request) *datasetPreviewSession {
	cookie, _ := r.Cookie("preview_lora_datasets")
	if cookie == nil {
		cookie = &http.Cookie{Name: "preview_lora_datasets", Value: rand.Text(), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode}
		http.SetCookie(w, cookie)
	}
	state := p.sessions[cookie.Value]
	if state == nil {
		state = &datasetPreviewSession{sets: map[string]datasetPreviewView{}, versions: map[string]datasetPreviewView{}, assets: map[string]domain.LoraDatasetAsset{}, data: map[string][]byte{}, jobs: []map[string]any{}}
		p.sessions[cookie.Value] = state
	}
	return state
}
func (p *datasetPreview) jobs(r *http.Request) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	cookie, _ := r.Cookie("preview_lora_datasets")
	if cookie == nil || p.sessions[cookie.Value] == nil {
		return nil
	}
	return append([]map[string]any(nil), p.sessions[cookie.Value].jobs...)
}
func cloneDatasetPreview(view datasetPreviewView) datasetPreviewView {
	data, _ := json.Marshal(view)
	var result datasetPreviewView
	json.Unmarshal(data, &result)
	return result
}
func (p *datasetPreview) save(state *datasetPreviewSession, id string, manifest domain.LoraDatasetManifest) datasetPreviewView {
	p.revision++
	view := datasetPreviewView{Dataset: domain.LoraDatasetRow{ID: id, Name: manifest.Settings.Name, Revision: p.revision, CreatedAt: p.now, UpdatedAt: p.now, ExpiresAt: p.now.Add(30 * 24 * time.Hour), ImageCount: len(manifest.Images)}, Manifest: manifest, Assets: map[string]domain.LoraDatasetAsset{}, Warnings: []any{}}
	for _, item := range manifest.Images {
		view.Assets[item.AssetID] = state.assets[item.AssetID]
		view.Dataset.SizeBytes += state.assets[item.AssetID].SizeBytes
	}
	view = cloneDatasetPreview(view)
	state.sets[id] = view
	return view
}
func (p *datasetPreview) asset(state *datasetPreviewSession, name string, data []byte) (domain.LoraDatasetAsset, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return domain.LoraDatasetAsset{}, err
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	for _, asset := range state.assets {
		if asset.Hash == digest {
			return asset, nil
		}
	}
	asset := domain.LoraDatasetAsset{ID: rand.Text(), Name: name, Hash: digest, MIMEType: "image/" + format, SizeBytes: int64(len(data)), Width: config.Width, Height: config.Height, CreatedAt: p.now}
	state.assets[asset.ID] = asset
	state.data[asset.ID] = data
	return asset, nil
}
func (p *datasetPreview) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.session(w, r)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	write := func(status int, value any) { w.WriteHeader(status); json.NewEncoder(w).Encode(value) }
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/lora-datasets"), "/")[1:]
	if len(parts) == 0 {
		if r.Method == http.MethodGet {
			rows := []domain.LoraDatasetRow{}
			for _, view := range state.sets {
				rows = append(rows, view.Dataset)
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Revision > rows[j].Revision })
			write(200, map[string]any{"datasets": rows})
			return
		}
		var input struct {
			ClientID string                     `json:"client_id"`
			Manifest domain.LoraDatasetManifest `json:"manifest"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		id := input.ClientID
		if id == "" {
			id = rand.Text()
		}
		if existing, ok := state.sets[id]; ok {
			write(201, existing)
			return
		}
		write(201, p.save(state, id, input.Manifest))
		return
	}
	if parts[0] == "gallery" {
		write(200, map[string]any{"images": []map[string]any{{"id": 1, "url": "/preview/dataset-media/portrait.jpg", "filename": "Портрет в дневном свете.jpg"}, {"id": 2, "url": "/preview/dataset-media/portrait-2.jpg", "filename": "Второй портрет.jpg"}, {"id": 3, "url": "/preview/dataset-media/interior.jpg", "filename": "Интерьер.jpg"}}})
		return
	}
	if parts[0] == "assets" && len(parts) == 2 {
		asset, ok := state.assets[parts[1]]
		if !ok {
			write(404, map[string]string{"error": "Файл недоступен"})
			return
		}
		w.Header().Set("Content-Type", asset.MIMEType)
		http.ServeContent(w, r, asset.Name, p.now, bytes.NewReader(state.data[asset.ID]))
		return
	}
	if parts[0] == "versions" {
		if len(parts) == 1 {
			rows := []domain.LoraDatasetSnapshot{}
			for id, view := range state.versions {
				if filter := r.URL.Query().Get("dataset_id"); filter != "" && view.Dataset.ID != filter {
					continue
				}
				rows = append(rows, domain.LoraDatasetSnapshot{ID: id, DatasetID: view.Dataset.ID, Name: view.Dataset.Name, Revision: view.Dataset.Revision, ImageCount: len(view.Manifest.Images), CreatedAt: p.now, ExpiresAt: view.Dataset.ExpiresAt})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Revision > rows[j].Revision })
			write(200, map[string]any{"versions": rows})
			return
		}
		view, ok := state.versions[parts[1]]
		if !ok {
			write(404, map[string]string{"error": "Версия недоступна"})
			return
		}
		if len(parts) == 2 {
			write(200, map[string]any{"manifest": view.Manifest})
			return
		}
		switch parts[2] {
		case "restore":
			write(201, p.save(state, rand.Text(), view.Manifest))
		case "delete":
			delete(state.versions, parts[1])
			write(200, map[string]bool{"deleted": true})
		case "export":
			p.export(w, view, state)
		default:
			write(404, map[string]string{"error": "not found"})
		}
		return
	}
	view, ok := state.sets[parts[0]]
	if !ok {
		write(404, map[string]string{"error": "Набор недоступен"})
		return
	}
	if len(parts) == 1 {
		write(200, view)
		return
	}
	if parts[1] == "assets" {
		if err := r.ParseMultipartForm(25 << 20); err != nil {
			write(400, map[string]string{"error": err.Error()})
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("image")
		if err != nil {
			write(400, map[string]string{"error": err.Error()})
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(io.LimitReader(file, 24<<20))
		asset, err := p.asset(state, header.Filename, data)
		if err != nil {
			write(400, map[string]string{"error": "Повреждённое фото"})
			return
		}
		write(201, map[string]any{"asset": asset})
		return
	}
	if parts[1] == "export" {
		p.export(w, view, state)
		return
	}
	if parts[1] == "import" {
		revision, _ := strconv.ParseInt(r.Header.Get("X-Dataset-Revision"), 10, 64)
		if revision != view.Dataset.Revision {
			write(409, map[string]string{"error": "Набор изменён"})
			return
		}
		r.ParseMultipartForm(25 << 20)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		file, _, err := r.FormFile("archive")
		if err != nil {
			write(400, map[string]string{"error": "Нет ZIP"})
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			write(400, map[string]string{"error": "Повреждённый ZIP"})
			return
		}
		var portable struct {
			Version  int                        `json:"version"`
			Settings domain.LoraDatasetSettings `json:"settings"`
			Images   []struct {
				File     string `json:"file"`
				Caption  string `json:"caption"`
				Excluded bool   `json:"excluded"`
			} `json:"images"`
		}
		entries := map[string][]byte{}
		for _, entry := range reader.File {
			source, _ := entry.Open()
			entries[entry.Name], _ = io.ReadAll(source)
			source.Close()
		}
		json.Unmarshal(entries["dataset.json"], &portable)
		manifest := domain.LoraDatasetManifest{Version: 1, Settings: portable.Settings, Images: []domain.LoraDatasetImage{}}
		for _, item := range portable.Images {
			asset, err := p.asset(state, path.Base(item.File), entries[item.File])
			if err != nil {
				write(400, map[string]string{"error": "Нет изображения"})
				return
			}
			manifest.Images = append(manifest.Images, domain.LoraDatasetImage{ID: rand.Text(), AssetID: asset.ID, Caption: string(entries[item.Caption]), Excluded: item.Excluded})
		}
		write(200, p.save(state, view.Dataset.ID, manifest))
		return
	}
	var input struct {
		Revision int64                      `json:"revision"`
		Manifest domain.LoraDatasetManifest `json:"manifest"`
		MediaID  int64                      `json:"media_id"`
	}
	json.NewDecoder(r.Body).Decode(&input)
	if parts[1] != "reuse" && input.Revision != view.Dataset.Revision {
		write(409, map[string]string{"error": "Набор изменён в другой вкладке."})
		return
	}
	switch parts[1] {
	case "save":
		write(200, p.save(state, view.Dataset.ID, input.Manifest))
	case "delete":
		delete(state.sets, view.Dataset.ID)
		write(200, map[string]bool{"deleted": true})
	case "versions":
		versionID := rand.Text()
		state.versions[versionID] = cloneDatasetPreview(view)
		write(201, map[string]any{"version": map[string]string{"id": versionID}})
	case "reuse":
		name := "portrait.jpg"
		if input.MediaID == 2 {
			name = "portrait-2.jpg"
		}
		if input.MediaID == 3 {
			name = "interior.jpg"
		}
		data, err := os.ReadFile("docs/frontend/prototype/assets/" + name)
		if err != nil {
			write(500, map[string]string{"error": err.Error()})
			return
		}
		asset, err := p.asset(state, name, data)
		if err != nil {
			write(500, map[string]string{"error": err.Error()})
			return
		}
		write(201, map[string]any{"asset": asset})
	case "train":
		versionID, jobID := rand.Text(), rand.Text()
		state.versions[versionID] = cloneDatasetPreview(view)
		count := 0
		for _, item := range view.Manifest.Images {
			if !item.Excluded {
				count++
			}
		}
		job := map[string]any{"id": jobID, "name": view.Manifest.Settings.Name, "family_label": "Krea2", "base_model": "krea2Raw_v10.safetensors", "state": "queued", "state_class": "is-active", "state_label": "В очереди", "stage": "В очереди", "progress": 0, "message": "Набор сохранён", "sample_count": count, "resolution": view.Manifest.Settings.Resolution, "concept_label": "Персонаж", "preset_label": "Основной", "max_train_steps": 1600, "can_cancel": true, "cancel_url": "/train-lora/" + jobID + "/cancel", "created_at": p.now.UnixMilli(), "dataset_snapshot_id": versionID}
		state.jobs = append(state.jobs, job)
		write(201, map[string]any{"job": job, "version": map[string]string{"id": versionID}})
	default:
		write(404, map[string]string{"error": "not found"})
	}
}
func (p *datasetPreview) export(w http.ResponseWriter, view datasetPreviewView, state *datasetPreviewSession) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="dataset.zip"`)
	writer := zip.NewWriter(w)
	defer writer.Close()
	images := []map[string]any{}
	for index, item := range view.Manifest.Images {
		ext := path.Ext(state.assets[item.AssetID].Name)
		stem := fmt.Sprintf("images/%04d", index+1)
		entry, _ := writer.Create(stem + ext)
		entry.Write(state.data[item.AssetID])
		caption, _ := writer.Create(stem + ".txt")
		io.WriteString(caption, item.Caption)
		images = append(images, map[string]any{"file": stem + ext, "caption": stem + ".txt", "excluded": item.Excluded})
	}
	entry, _ := writer.Create("dataset.json")
	json.NewEncoder(entry).Encode(map[string]any{"version": 1, "settings": view.Manifest.Settings, "images": images})
}
