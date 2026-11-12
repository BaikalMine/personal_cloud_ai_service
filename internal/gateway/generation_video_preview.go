package gateway

import (
	"context"
	"net/http"
	"strconv"

	"ai-access-gateway/internal/mediaprobe"
	"ai-access-gateway/internal/thumbnail"
)

func (a *App) handleGenerationVideoPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil || !a.validCSRF(r) {
		http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
		return
	}
	filename := r.Form.Get("input_video")
	if err := a.validateGenerationVideo(filename, a.currentUser(r).ID); err != nil {
		http.NotFound(w, r)
		return
	}
	if a.cfg.ComfyUIUpstream == nil {
		http.Error(w, "хранилище материалов временно недоступно", http.StatusServiceUnavailable)
		return
	}
	release, ok := a.mediaByteLimiter().tryAcquire(thumbnail.MaxVideoPreviewBytes)
	if !ok {
		http.Error(w, "предпросмотр занят; повторите позже", http.StatusServiceUnavailable)
		return
	}
	defer release()
	var data []byte
	var duration string
	err := a.withInputVideo(r.Context(), filename, func(ctx context.Context, local, mime string) error {
		if measurement, err := mediaprobe.Probe(ctx, local, mime); err == nil {
			duration = measurement.DurationSeconds
		}
		var err error
		data, err = thumbnail.VideoPreview(ctx, local, mime)
		return err
	})
	if err != nil {
		http.Error(w, "не удалось подготовить предпросмотр; исходник доступен для генерации", http.StatusUnprocessableEntity)
		return
	}
	defer clear(data)
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Preview-Max-Seconds", "30")
	if duration != "" {
		w.Header().Set("X-Source-Video-Duration", duration)
	}
	_, _ = w.Write(data)
}
