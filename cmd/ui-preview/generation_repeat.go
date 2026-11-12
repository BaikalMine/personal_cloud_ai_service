package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// UI fixtures only: these routes never call ComfyUI or the production store.
func registerGenerationRepeatPreview(mux *http.ServeMux) {
	var mutex sync.Mutex
	receipts := map[string]map[string]any{}
	for _, endpoint := range []string{"check", "submit", "status", "discard"} {
		mux.HandleFunc("/generate/jobs/repeat/"+endpoint, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "private, no-store")
			method := http.MethodPost
			if endpoint == "status" {
				method = http.MethodGet
			}
			if r.Method != method {
				w.WriteHeader(405)
				return
			}
			_ = r.ParseForm()
			requestID, sourceID := r.Form.Get("client_request_id"), r.Form.Get("source_job_id")
			if endpoint == "status" {
				requestID = r.URL.Query().Get("request_id")
			}
			mutex.Lock()
			defer mutex.Unlock()
			if receipt, ok := receipts[requestID]; ok {
				_ = json.NewEncoder(w).Encode(receipt)
				return
			}
			if endpoint == "status" {
				w.WriteHeader(404)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "Запрос пока не найден"})
				return
			}
			if endpoint == "discard" {
				receipts[requestID] = map[string]any{"accepted": false, "cancelled": true, "source_id": sourceID, "request_id": requestID}
				_ = json.NewEncoder(w).Encode(receipts[requestID])
				return
			}
			if endpoint == "submit" {
				receipts[requestID] = map[string]any{"accepted": true, "source_id": sourceID, "request_id": requestID, "job_id": "repeat-preview-" + requestID}
				_ = json.NewEncoder(w).Encode(receipts[requestID])
				return
			}
			state, message := "ready", "Сохранённые параметры и текущие файлы совпадают. Можно подтвердить новый запуск."
			if strings.Contains(sourceID, "failed") {
				state, message = "blocked", "Исходное изображение больше не хранится. Выберите его заново в редакторе."
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": 1, "source_id": sourceID, "state": state, "message": message,
				"seed": "9223372036854775806", "model": "Krea2 / Raw INT8 Mixed", "workflow": "PhotoFlow Krea2",
				"prompt":      "Редакционный портрет у окна. Сохранить естественную фактуру кожи, свет и спокойную композицию.",
				"template_id": "text-to-image", "recorded": true, "files_observed": true, "confirmation": "preview-repeat-only", "expires_at": time.Now().Add(5 * time.Minute).UnixMilli(),
				"differences": []any{}, "warnings": []string{"Повтор использует те же параметры. Побитовое совпадение результата не гарантируется."}, "assets": []any{},
				"quota_known": true, "quota": map[string]any{"image": map[string]any{"daily_limit": 12, "daily_remaining": 4, "total_limit": 100, "total_remaining": 70}},
				"source": map[string]any{"job_id": sourceID, "media": []map[string]any{{"id": 901, "url": "/preview/studio-result.jpg", "filename": "portrait.jpg", "media_type": "image", "expires_unix": time.Now().Add(time.Hour).UnixMilli()}}},
			})
		})
	}
}
