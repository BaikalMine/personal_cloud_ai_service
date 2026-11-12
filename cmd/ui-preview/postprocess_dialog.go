package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/mediaprobe"
)

// Local presentation fixtures measure bundled files, never inspect installed
// models, charge quota, publish a job or contact ComfyUI. Consent is disabled.
func registerPostprocessDialogPreview(mux *http.ServeMux, originals, derived []map[string]any) {
	type source struct {
		job, id string
		index   int
		media   map[string]any
	}
	sources := map[string]source{}
	for index, item := range originals {
		if media, ok := item["Media"].(map[string]any); ok && index < len(galleryFixturePaths) {
			id, job := fmt.Sprint(media["ID"]), fmt.Sprint(item["JobID"])
			sources[job+":"+id] = source{job, id, index, media}
		}
	}
	for _, item := range derived {
		media, ok := item["Media"].(map[string]any)
		job, _ := item["JobID"].(string)
		if !ok || job == "" || item["HasMedia"] != true {
			continue
		}
		index := 0
		if media["MediaType"] == "video" {
			index = 4
		}
		id := fmt.Sprint(media["ID"])
		sources[job+":"+id] = source{job, id, index, media}
	}
	measure := func(ctx context.Context, s source) (mediaprobe.DecodedMeasurement, error) {
		mime := "image/jpeg"
		if s.index == 4 {
			mime = "video/mp4"
		}
		return mediaprobe.Decode(ctx, galleryFixturePaths[s.index], mime)
	}
	write := func(w http.ResponseWriter, status int, value any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	var mutex sync.Mutex
	receipts := map[string]map[string]any{}
	validID := regexp.MustCompile(`^[A-Za-z0-9_-]{16,96}$`)
	for _, endpoint := range []string{"options", "quote", "confirm", "status", "discard", "references"} {
		mux.HandleFunc("/generate/postprocess/"+endpoint, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			method := http.MethodPost
			if endpoint == "status" || endpoint == "references" {
				method = http.MethodGet
			}
			if r.Method != method {
				write(w, 405, map[string]string{"error": "Метод не поддерживается"})
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 8192)
			if err := r.ParseForm(); err != nil {
				write(w, 400, map[string]string{"error": "Некорректный запрос"})
				return
			}
			v := r.Form
			if endpoint == "confirm" {
				write(w, 503, map[string]string{"error": "Запуск в предпросмотре отключён. Расчёт можно отменить."})
				return
			}
			if endpoint == "references" {
				items := []map[string]any{}
				for index, item := range originals {
					media, ok := item["Media"].(map[string]any)
					if !ok || index >= 4 || fmt.Sprint(media["ID"]) == v.Get("exclude") || !strings.Contains(strings.ToLower(fmt.Sprint(media["Filename"])), strings.ToLower(v.Get("search"))) {
						continue
					}
					items = append(items, map[string]any{"job_id": item["JobID"], "media_id": fmt.Sprint(media["ID"]), "filename": media["Filename"], "url": galleryFixtureURLs[index], "sensitive": media["Sensitive"], "expires_at": time.Now().Add(time.Hour)})
				}
				write(w, 200, map[string]any{"items": items, "next": ""})
				return
			}
			mutex.Lock()
			defer mutex.Unlock()
			id := v.Get("request_id")
			if endpoint != "options" && !validID.MatchString(id) {
				write(w, 422, map[string]string{"error": "Некорректный запрос"})
				return
			}
			if endpoint == "discard" {
				receipts[id] = map[string]any{"request_id": id, "state": "cancelled", "accepted": false}
				write(w, 200, receipts[id])
				return
			}
			if receipt := receipts[id]; endpoint != "options" && receipt != nil {
				if expiry, ok := receipt["expires_at"].(time.Time); ok && !expiry.After(time.Now()) {
					write(w, 410, map[string]string{"error": "Срок расчёта истёк"})
					return
				}
				write(w, 200, receipt)
				return
			}
			if endpoint == "status" {
				write(w, 404, map[string]string{"error": "Запрос пока не найден"})
				return
			}
			s, ok := sources[v.Get("source_job_id")+":"+v.Get("source_media_id")]
			if !ok {
				write(w, 404, map[string]string{"error": "Исходный файл недоступен"})
				return
			}
			measurement, err := measure(r.Context(), s)
			if err != nil {
				write(w, 422, map[string]string{"error": "Этот файл не подходит для демонстрации обработки"})
				return
			}
			if endpoint == "options" {
				upscale, interpolation, color := []any{}, []any{}, []any{}
				for _, scale := range []float64{1.25, 1.5, 2} {
					width, height := int(float64(measurement.Width)*scale)/8*8, int(float64(measurement.Height)*scale)/8*8
					if width <= 4096 && height <= 4096 && width >= measurement.Width && height >= measurement.Height {
						for _, quality := range []string{"HIGH", "ULTRA"} {
							upscale = append(upscale, map[string]any{"options": map[string]any{"width": width, "height": height, "interpolate": 1, "rtx_quality": quality}, "requires_upscale": false})
						}
					}
				}
				if measurement.MediaType == "video" {
					interpolation = append(interpolation, map[string]any{"options": map[string]any{"width": measurement.Width, "height": measurement.Height, "interpolate": 2, "rife_checkpoint": "rife49.pth"}, "requires_upscale": false})
				}
				for _, method := range []string{"adain", "mean_std"} {
					color = append(color, map[string]any{"options": map[string]any{"width": measurement.Width, "height": measurement.Height, "interpolate": 1, "color_method": method, "color_strength": 0.7}, "requires_upscale": false})
				}
				write(w, 200, map[string]any{"version": 1, "source": map[string]any{"job_id": s.job, "media_id": s.id, "measurement": measurement}, "upscale": upscale, "interpolation": interpolation, "color": color, "reasons": map[string]string{"interpolation": "Только для видео"}, "checked_at": time.Now()})
				return
			}
			if len(receipts) >= 128 {
				write(w, 429, map[string]string{"error": "Лимит демонстрационных расчётов достигнут"})
				return
			}
			options := map[string]any{}
			for _, key := range []string{"width", "height", "interpolate"} {
				value, err := strconv.Atoi(v.Get(key))
				if err != nil || value <= 0 || value > 4096 {
					write(w, 422, map[string]string{"error": "Некорректные параметры"})
					return
				}
				options[key] = value
			}
			for _, key := range []string{"rtx_quality", "rife_checkpoint", "color_method"} {
				if v.Get(key) != "" {
					options[key] = v.Get(key)
				}
			}
			measuredSource := func(s source, m mediaprobe.DecodedMeasurement) map[string]any {
				return map[string]any{"job_id": s.job, "media_id": s.id, "width": m.Width, "height": m.Height, "fps_numerator": m.FPSNumerator, "fps_denominator": m.FPSDenominator}
			}
			inputs := []map[string]any{measuredSource(s, measurement)}
			if v.Get("color_method") != "" {
				reference, ok := sources[v.Get("reference_job_id")+":"+v.Get("reference_media_id")]
				strength, parseErr := strconv.ParseFloat(v.Get("color_strength"), 64)
				if !ok || reference.index == 4 || reference.id == s.id || parseErr != nil || !(strength > 0 && strength <= 1) {
					write(w, 422, map[string]string{"error": "Эталон недоступен"})
					return
				}
				m, err := measure(r.Context(), reference)
				if err != nil {
					write(w, 422, map[string]string{"error": "Эталон недоступен"})
					return
				}
				options["color_strength"] = strength
				inputs = append(inputs, measuredSource(reference, m))
			}
			receipt := map[string]any{"request_id": id, "state": "quoted", "accepted": false, "confirmation": strings.Repeat("a", 64), "expires_at": time.Now().Add(5 * time.Minute), "cost": 1, "sources": inputs,
				"plan": map[string]any{"version": 1, "media_type": measurement.MediaType, "options": options, "fps_numerator": measurement.FPSNumerator * int64(options["interpolate"].(int)), "fps_denominator": measurement.FPSDenominator}}
			receipts[id] = receipt
			write(w, 200, receipt)
		})
	}
}
