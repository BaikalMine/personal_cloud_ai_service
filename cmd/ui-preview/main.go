package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/updates"
)

func main() {
	templates, err := gateway.ParseTemplates()
	if err != nil {
		log.Fatal(err)
	}
	now := time.Now()
	admin := &domain.User{ID: 1, Username: "admin", Role: "admin", CanUseComfyUI: true, CanUseOpenWebUI: true}
	common := map[string]any{
		"CSRF":          "preview",
		"AssetVersion":  templates.AssetVersion,
		"CurrentUser":   admin,
		"PublicBaseURL": "http://127.0.0.1:8090",
		"AdminBaseURL":  "http://127.0.0.1:8091",
		"Retention": map[string]any{
			"GenerationHistoryLabel": "24 часа",
			"GenerationMediaLabel":   "24 часа",
			"AIContentLabel":         "7 дней",
			"ComfyInputsLabel":       "3 дня",
			"HostMetricsLabel":       "7 дней",
			"AuditLogLabel":          "90 дней",
		},
	}
	activities := []domain.Activity{
		{Service: "openwebui", ServiceLabel: "OpenWebUI", Summary: "Сообщение нейросети", Count: 1, Method: "POST", Path: "/ollama/api/chat", Status: 200, Duration: 1840, Bytes: 4820, CreatedAt: now.Add(-2 * time.Minute)},
		{Service: "comfyui", ServiceLabel: "ComfyUI", Summary: "Запуск генерации", Count: 2, Method: "POST", Path: "/prompt", Status: 200, Duration: 122, Bytes: 614, CreatedAt: now.Add(-14 * time.Minute)},
		{Service: "comfyui", ServiceLabel: "ComfyUI", Summary: "Просмотр результата", Count: 1, Method: "GET", Path: "/view?filename=result.png", Status: 200, Duration: 31, Bytes: 2480000, CreatedAt: now.Add(-16 * time.Minute)},
	}
	chart := []domain.ChartPoint{{Label: "Сегодня", Count: 84, Percent: 100}, {Label: "Вчера", Count: 61, Percent: 73}, {Label: "10 июл", Count: 46, Percent: 55}, {Label: "9 июл", Count: 32, Percent: 38}}
	minerViews := []gateway.MinerView{{
		Miner: domain.Miner{ID: 1, Name: "Example miner", ScriptPath: `mining-root/example/start-mining.bat`, ProcessName: "miner.exe", Enabled: true, Default: true},
		State: mining.State{Available: true, Running: true, PIDs: []int{22928}, ProcessName: "SRBMiner-MULTI.exe", StartedAt: now.Add(-5 * time.Hour)},
	}}
	miningOverview := gateway.MiningOverview{
		Available: true, Running: true, Miners: minerViews, Message: "Майнинг работает и использует вычислительные ресурсы.",
		Agent: gateway.DependencyStatus{Key: "mining-agent", Name: "Управление майнингом", State: gateway.DependencyOnline, StateLabel: "В сети", Detail: "Heartbeat получен."},
		Script: mining.Script{
			Path: minerViews[0].ScriptPath, SHA256: "23cb47f58a53cdfce153f6c746b6d8e89ad72b5b9af42a956e7d885d652d963a",
			Content: "@echo off\r\ncd %~dp0\r\ncls\r\n\r\nSRBMiner-MULTI.exe --algorithm-gpu pearlhash --pool ru.pearl.herominers.com:1200 --wallet PEARL_WALLET\r\npause\r\n",
		},
	}
	miningOverview.Default = &miningOverview.Miners[0]
	miningOverview.Active = &miningOverview.Miners[0]
	previewSVG := `<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="1000" viewBox="0 0 1600 1000"><rect width="1600" height="1000" fill="#13201d"/><rect x="120" y="120" width="1360" height="760" rx="28" fill="#20453a"/><circle cx="800" cy="500" r="250" fill="#71dfb9"/><path d="M420 650 700 360l190 180 150-140 260 250z" fill="#0a1714"/></svg>`
	generationJobs := []map[string]any{
		{
			"job_id": "job-video-running", "request_id": "request-video-running", "prompt_id": "prompt-video-running",
			"state": "running", "job_state": "running", "message": "Генерация видео: шаг 8 из 25", "template_id": "minimax-h3-video",
			"workflow_id": "minimax-h3-v4", "model_name": `MiniMaxH3\MiniMax_H3_FL2VA_pruned_int8_convrot.safetensors`, "seed": int64(73950205217521),
			"attempt": 1, "input_count": 2, "prompt": "Камера медленно приближается к героине; волосы и ткань естественно движутся от ветра, лицо и одежда сохраняются без изменений.",
			"cancellable": true, "retryable": false, "created_at": now.Add(-6 * time.Minute), "updated_at": now.Add(-12 * time.Second),
			"duration_seconds": int64(348), "media": []map[string]any{},
		},
		{
			"job_id": "job-image-completed", "request_id": "request-image-completed", "prompt_id": "prompt-image-completed",
			"state": "completed", "job_state": "completed", "message": "Изображение готово и сохранено", "template_id": "text-to-image",
			"workflow_id": "photoflow-krea2", "model_name": `Krea2\Krea2_gonzalomo_v40.safetensors`, "seed": int64(284797972294826),
			"attempt": 1, "input_count": 0, "prompt": "Кинематографичный портрет в тихом ночном городе, естественная кожа, мягкий свет витрин и аккуратная глубина резкости.",
			"cancellable": false, "retryable": true, "created_at": now.Add(-28 * time.Minute), "updated_at": now.Add(-27*time.Minute - 42*time.Second),
			"finished_at": now.Add(-27*time.Minute - 42*time.Second), "expires_at": now.Add(23*time.Hour + 32*time.Minute), "duration_seconds": int64(18),
			"media": []map[string]any{{"id": int64(301), "url": "/preview/result.svg", "filename": "AI-Gateway-Krea2-preview.png", "media_type": "image", "expires_unix": now.Add(23*time.Hour + 32*time.Minute).UnixMilli(), "sensitive": false}},
		},
		{
			"job_id": "job-image-failed", "request_id": "request-image-failed", "state": "error", "job_state": "failed",
			"message": "ComfyUI отклонил workflow: не удалось подготовить обязательный вход модели", "error_code": "workflow_validation_failed", "template_id": "image-to-image",
			"workflow_id": "photoflow-flux2-edit", "model_name": `Flux2\flux2-klein-9b-fp8.safetensors`, "seed": int64(1019794942414480),
			"attempt": 2, "input_count": 1, "prompt": "Сохранить внешность и композицию исходного фото, перенести сцену в светлую редакционную студию с холодным рассеянным светом.",
			"cancellable": false, "retryable": true, "created_at": now.Add(-51 * time.Minute), "updated_at": now.Add(-48 * time.Minute),
			"finished_at": now.Add(-48 * time.Minute), "expires_at": now.Add(23*time.Hour + 9*time.Minute), "duration_seconds": int64(181), "media": []map[string]any{},
		},
		{
			"job_id": "job-cancelled", "request_id": "request-cancelled", "state": "cancelled", "job_state": "cancelled", "message": "Отменено пользователем", "template_id": "minimax-h3-video",
			"workflow_id": "minimax-h3-v4", "model_name": `MiniMaxH3\MiniMax_H3_REF2VA_pruned_int8_convrot.safetensors`, "seed": int64(-1),
			"attempt": 1, "input_count": 4, "prompt": "Первый кадр задаёт сцену; остальные изображения используются как референсы персонажа, одежды и окружения.",
			"cancellable": false, "retryable": true, "created_at": now.Add(-85 * time.Minute), "updated_at": now.Add(-84*time.Minute - 31*time.Second),
			"finished_at": now.Add(-84*time.Minute - 31*time.Second), "expires_at": now.Add(22*time.Hour + 35*time.Minute), "duration_seconds": int64(29), "media": []map[string]any{},
		},
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("internal/gateway/static"))))
	mux.HandleFunc("/preview/result.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(previewSVG))
	})
	mux.HandleFunc("/admin/content/media/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(previewSVG))
	})
	mux.HandleFunc("/generate/upload/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"name":"preview-input.png","subfolder":"preview"}`))
	})
	mux.HandleFunc("/generate/library/reuse-image", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"name":"gallery-preview.png","subfolder":"preview","type":"input"}`))
	})
	mux.HandleFunc("/generate/library/images", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"images": []map[string]any{
			{"id": 1, "url": "/preview/result.svg", "filename": "AI-Gateway-Krea2-portrait.png", "model_name": "Krea2 Raw INT8 Mixed", "created_unix": now.Add(-8 * time.Minute).UnixMilli(), "expires_unix": now.Add(23*time.Hour + 52*time.Minute).UnixMilli(), "sensitive": false},
			{"id": 2, "url": "/preview/result.svg", "filename": "AI-Gateway-Flux2-editorial.png", "model_name": "Flux 2 Klein 9B", "created_unix": now.Add(-34 * time.Minute).UnixMilli(), "expires_unix": now.Add(23*time.Hour + 26*time.Minute).UnixMilli(), "sensitive": true},
			{"id": 3, "url": "/preview/result.svg", "filename": "AI-Gateway-Krea2-product.png", "model_name": "Krea2 Gonzalomo v40", "created_unix": now.Add(-72 * time.Minute).UnixMilli(), "expires_unix": now.Add(22*time.Hour + 48*time.Minute).UnixMilli(), "sensitive": false},
		}})
	})
	mux.HandleFunc("/generate/jobs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": generationJobs, "revision": int64(17)})
	})
	mux.HandleFunc("/generate/jobs/detail", func(w http.ResponseWriter, r *http.Request) {
		jobID := r.URL.Query().Get("job_id")
		transitions := []map[string]any{
			{"state": "draft", "message": "Задание создано", "attempt": 1, "created_at": now.Add(-6 * time.Minute)},
			{"state": "preparing", "message": "Параметры проверены", "attempt": 1, "created_at": now.Add(-5*time.Minute - 58*time.Second)},
			{"state": "uploading", "message": "Референсы переданы в ComfyUI", "attempt": 1, "created_at": now.Add(-5*time.Minute - 54*time.Second)},
			{"state": "queued", "message": "Workflow принят в очередь", "attempt": 1, "created_at": now.Add(-5*time.Minute - 50*time.Second)},
		}
		if jobID == "job-video-running" {
			transitions = append(transitions, map[string]any{"state": "running", "message": "ComfyUI выполняет workflow", "attempt": 1, "created_at": now.Add(-5*time.Minute - 46*time.Second)})
		} else {
			transitions = append(transitions,
				map[string]any{"state": "running", "message": "ComfyUI выполняет workflow", "attempt": 1, "created_at": now.Add(-5*time.Minute - 46*time.Second)},
				map[string]any{"state": "postprocessing", "message": "Подготавливаем результат", "attempt": 1, "created_at": now.Add(-5*time.Minute - 10*time.Second)},
				map[string]any{"state": "archiving", "message": "Сохраняем результат и параметры", "attempt": 1, "created_at": now.Add(-5*time.Minute - 4*time.Second)},
				map[string]any{"state": "completed", "message": "Задание завершено", "attempt": 1, "created_at": now.Add(-5 * time.Minute)},
			)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"transitions": transitions})
	})
	mux.HandleFunc("/generate/jobs/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte("retry: 2000\nevent: ready\ndata: 17\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("/generate/jobs/cancel", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"cancelled":false,"job":{"message":"Отмена отправлена в ComfyUI"}}`))
	})
	mux.HandleFunc("/generate/jobs/retry", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		jobID := r.Form.Get("job_id")
		requiresInputs := jobID != "job-image-completed"
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"parent_job_id": jobID, "requires_inputs": requiresInputs,
			"values": map[string]string{"template_id": "text-to-image", "generation_workflow": "photoflow-krea2", "model": "krea2:test", "positive_prompt": "Кинематографичный портрет в тихом ночном городе."},
		})
	})
	mux.HandleFunc("/generate/variants", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"variants":[
			{"id":"preview-1","template_id":"text-to-image","model_name":"Krea2 / Raw INT8 Mixed","seed":284797972294826,"state":"completed","duration_seconds":15,"values":{"template_id":"text-to-image","generation_workflow":"photoflow-krea2","model":"krea2:test","positive_prompt":"A cinematic portrait of a woman in a quiet neon-lit street, realistic skin, dramatic atmosphere."},"media":[{"id":1,"filename":"AI-Gateway-Krea2-portrait.png","media_type":"image","url":"/preview/result.svg","sensitive":false}]},
			{"id":"preview-2","template_id":"text-to-image","model_name":"Krea2 / Raw INT8 Mixed","seed":1033409957175067,"state":"completed","duration_seconds":16,"values":{"template_id":"text-to-image","generation_workflow":"photoflow-krea2","model":"krea2:test","positive_prompt":"A calm seaside embankment at sunset with editorial fashion photography and soft detailed light."},"media":[{"id":2,"filename":"AI-Gateway-Krea2-seaside.png","media_type":"image","url":"/preview/result.svg","sensitive":false}]},
			{"id":"preview-3","template_id":"image-to-image","model_name":"Flux 2 / Klein 9B","seed":1019794942414480,"state":"completed","duration_seconds":15,"values":{"template_id":"image-to-image","generation_workflow":"photoflow-flux2-edit","model":"flux2:test","positive_prompt":"Keep the subject identity and composition, replace the background with a warm studio environment."},"media":[{"id":3,"filename":"AI-Gateway-Flux2-editorial.png","media_type":"image","url":"/preview/result.svg","sensitive":true}]},
			{"id":"preview-4","template_id":"text-to-image","model_name":"Krea2 / Raw INT8 Mixed","seed":45876641139403,"state":"error","duration_seconds":0,"error_message":"Недостаточно памяти для выбранного разрешения.","values":{"positive_prompt":"A detailed fashion portrait with dramatic lighting."},"media":[]}
		]}`))
	})
	render := func(name, title string, values map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			data := make(map[string]any, len(common)+len(values)+1)
			for key, value := range common {
				data[key] = value
			}
			for key, value := range values {
				data[key] = value
			}
			data["Title"] = title
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := templates.ExecuteTemplate(w, name, data); err != nil {
				log.Printf("render %s: %v", name, err)
			}
		}
	}

	mux.HandleFunc("/preview/app", render("app", "Рабочее пространство", map[string]any{
		"Services":     []gateway.ServiceStatus{{Name: "ComfyUI", Online: true, Latency: 24 * time.Millisecond, Detail: "Готов"}, {Name: "OpenWebUI", Online: true, Latency: 41 * time.Millisecond, Detail: "Готов"}},
		"Stats":        domain.UserStats{TodayRequests: 18, WeekRequests: 93, TotalBytesOut: 584000000, LastService: "OpenWebUI", Chart: chart},
		"Activities":   activities,
		"Mining":       miningOverview,
		"MiningStatus": "",
	}))
	mux.HandleFunc("/preview/components", render("ui_components", "Компоненты интерфейса", map[string]any{}))
	mux.HandleFunc("/preview/generate", render("generate", "Быстрая генерация", map[string]any{
		"Workflows": []map[string]any{
			{"ID": "text-to-image", "Name": "Текст в изображение", "Description": "Создаёт изображение по вашему описанию.", "RequiresImage": false},
			{"ID": "image-to-image", "Name": "Фото и промт", "Description": "Перерисовывает загруженное фото.", "RequiresImage": true},
			{"ID": "minimax-h3-video", "Name": "Видео", "Description": "Создаёт ролик из текста, кадров или референсов.", "RequiresImage": false, "AllowsImages": true},
		},
		"GenerationPresets": []map[string]any{
			{"ID": "photoflow-krea2", "TemplateID": "text-to-image", "Name": "PhotoFlow Krea2", "Description": "Двухэтапная генерация с апскейлом и детализацией.", "Family": "krea2", "Available": true, "ModelID": "krea2:test", "ModelCount": 2, "DefaultSteps": 8, "DefaultCFG": 1, "DefaultSampler": "euler", "DefaultScheduler": "simple", "LoraStrength": 0.8},
			{"ID": "photoflow-krea2-edit", "TemplateID": "image-to-image", "Name": "Krea 2: редактирование", "Description": "Редактирование первого фото с опциональным вторым референсом.", "Family": "krea2", "Available": true, "ModelID": "krea2:test", "ModelCount": 2, "DefaultSteps": 8, "DefaultCFG": 1, "DefaultSampler": "euler", "DefaultScheduler": "simple", "LoraStrength": 0.8, "RequiresImage": true, "AllowsImages": true, "MaxInputImages": 2},
			{"ID": "photoflow-flux2-edit", "TemplateID": "image-to-image", "Name": "Flux2 Редактирование", "Description": "Редактирование основного изображения и до трёх дополнительных референсов.", "Family": "flux2", "Available": true, "ModelID": "flux2:test", "ModelCount": 1, "DefaultSteps": 20, "DefaultCFG": 5, "DefaultSampler": "euler", "DefaultScheduler": "normal", "RequiresImage": true, "AllowsImages": true, "MaxInputImages": 4},
			{"ID": "minimax-h3-video", "TemplateID": "minimax-h3-video", "Name": "MiniMaxH3 Видео", "Description": "Текст в видео, ключевые кадры или мультимодальные референсы.", "Family": "minimax_h3", "Available": true, "ModelID": "minimax:h3", "ModelCount": 2, "DefaultSteps": 25, "DefaultCFG": 1, "DefaultSampler": "euler", "DefaultScheduler": "simple", "RequiresImage": false, "AllowsImages": true, "MaxInputImages": 4},
		},
		"QuickModels": []map[string]any{
			{"ID": "krea2:test", "DisplayName": "Krea2 / Raw INT8 Mixed", "Family": "krea2", "Available": true, "DefaultSteps": 8, "DefaultCFG": 1, "DefaultSampler": "euler", "DefaultScheduler": "simple", "LoraStrength": 0.8},
			{"ID": "flux2:test", "DisplayName": "Flux 2 / Klein 9B", "Family": "flux2", "Available": true, "DefaultSteps": 20, "DefaultCFG": 5, "DefaultSampler": "euler", "DefaultScheduler": "flux2"},
			{"ID": "minimax:h3", "DisplayName": "MiniMax H3 FL2VA", "Family": "minimax_h3", "Available": true, "DefaultSteps": 25, "DefaultCFG": 1, "DefaultSampler": "euler", "DefaultScheduler": "simple", "DefaultVideoShift": 11, "DefaultAudioShift": 3},
			{"ID": "minimax:eros", "DisplayName": "H3 Eros Max beta4 · встроенный Turbo", "Family": "minimax_h3", "Available": true, "VideoIntegratedTurbo": true, "VideoReferenceOnly": true, "DefaultSteps": 8, "DefaultCFG": 1, "DefaultSampler": "euler", "DefaultScheduler": "simple", "DefaultVideoShift": 12, "DefaultAudioShift": 7},
		},
		"LoraGroups": []map[string]any{
			{"Name": "Базовые Krea2", "Loras": []map[string]any{{"Name": "lenovo_krea2.safetensors", "DisplayName": "Lenovo Krea2", "DefaultStrength": 0.8, "Default": true}, {"Name": "krea2_turbo.safetensors", "DisplayName": "Krea2 Turbo", "DefaultStrength": 1.0}}},
			{"Name": "Реализм и детали", "Loras": []map[string]any{{"Name": "Krea2-realism-V2.safetensors", "DisplayName": "Krea2 Realism V2", "DefaultStrength": 1.0}, {"Name": "Detailer-KREA2.safetensors", "DisplayName": "Detailer Krea2", "DefaultStrength": 2.0}}},
			{"Name": "Стили", "Loras": []map[string]any{{"Name": "AltGirlKreaV1.5.safetensors", "DisplayName": "AltGirl Krea", "DefaultStrength": 1.0}}},
		},
		"FluxLoraGroups": []map[string]any{
			{"Name": "Flux2", "Loras": []map[string]any{
				{"Name": "Flux2\\cinematic_detail.safetensors", "DisplayName": "Cinematic Detail", "DefaultStrength": 1.0},
				{"Name": "Flux2\\product_identity.safetensors", "DisplayName": "Product Identity", "DefaultStrength": 0.8},
			}},
		},
		"MiniMaxLoraGroups": []map[string]any{
			{"Name": "MiniMax H3", "Loras": []map[string]any{
				{"Name": "MiniMaxH3\\h3_Better_NSFW_Motion_V1.safetensors", "DisplayName": "Better NSFW Motion (H3 Ref2VA V1)", "DefaultStrength": 0.9},
				{"Name": "MiniMaxH3\\HMNSFW-AIO-V2.5.safetensors", "DisplayName": "HMNSFW AIO V2.5", "DefaultStrength": 1.0},
				{"Name": "MiniMaxH3\\Minimaxh3-cowgirl_position-Ref2V-512_000000550.safetensors", "DisplayName": "Cowgirl Position Ref2V", "DefaultStrength": 1.0},
				{"Name": "MiniMaxH3\\SexGod-NaughtyTimes-v2-rank256.safetensors", "DisplayName": "NaughtyTimes V2", "DefaultStrength": 1.0},
				{"Name": "MiniMaxH3\\SynthPussy_H3_closeups_v1-step00008300.safetensors", "DisplayName": "Closeups H3 V1", "DefaultStrength": 1.0},
				{"Name": "MiniMaxH3\\VBVR_H3_attn_only.safetensors", "DisplayName": "VBVR H3", "DefaultStrength": 1.0},
			}},
		},
		"ComfyOnline": true, "ModelsAvailable": true, "SelectedWorkflow": "", "PreviewOutputURL": "/preview/result.svg",
		"GenerationQuota": map[string]any{"HasLimits": true, "DailyLimit": 12, "DailyRemaining": 7, "TotalLimit": int64(100), "TotalRemaining": int64(73)},
		"RecentGenerationMedia": []map[string]any{
			{"ID": int64(1), "URL": "/preview/result.svg", "Filename": "AI-Gateway-preview.png", "MediaType": "image", "ExpiresUnix": now.Add(18*time.Hour + 27*time.Minute).UnixMilli()},
			{"ID": int64(2), "URL": "/preview/result.svg", "Filename": "AI-Gateway-sensitive-preview.png", "MediaType": "image", "Sensitive": true, "ExpiresUnix": now.Add(17*time.Hour + 5*time.Minute).UnixMilli()},
		},
	}))
	galleryItems := []map[string]any{
		{"VariantID": int64(101), "Scenario": "Текст в изображение", "ModelName": "Krea2 Raw INT8 Mixed", "Prompt": "Кинематографичный портрет в тихом ночном городе, естественная кожа и мягкий свет витрин.", "Seed": int64(284797972294826), "CreatedAt": now.Add(-8 * time.Minute), "Media": map[string]any{"ID": int64(1), "URL": "/preview/result.svg", "Filename": "AI-Gateway-Krea2-portrait.png", "MediaType": "image", "ExpiresUnix": now.Add(23*time.Hour + 52*time.Minute).UnixMilli(), "Sensitive": false}},
		{"VariantID": int64(102), "Scenario": "Фото и промт", "ModelName": "Flux 2 Klein 9B", "Prompt": "Сохранить внешность и позу, перенести сцену в светлую редакционную студию с холодным рассеянным светом.", "Seed": int64(1033409957175067), "CreatedAt": now.Add(-34 * time.Minute), "Media": map[string]any{"ID": int64(2), "URL": "/preview/result.svg", "Filename": "AI-Gateway-Flux2-editorial.png", "MediaType": "image", "ExpiresUnix": now.Add(23*time.Hour + 26*time.Minute).UnixMilli(), "Sensitive": true}},
		{"VariantID": int64(103), "Scenario": "Текст в изображение", "ModelName": "Krea2 Gonzalomo v40", "Prompt": "Предметная фотография прозрачного флакона духов на камне, солнечные блики и натуральные тени.", "Seed": int64(1019794942414480), "CreatedAt": now.Add(-72 * time.Minute), "Media": map[string]any{"ID": int64(3), "URL": "/preview/result.svg", "Filename": "AI-Gateway-Krea2-product.png", "MediaType": "image", "ExpiresUnix": now.Add(22*time.Hour + 48*time.Minute).UnixMilli(), "Sensitive": false}},
		{"VariantID": int64(104), "Scenario": "Видео", "ModelName": "MiniMax H3 FL2VA", "Prompt": "Камера медленно приближается, волосы и ткань естественно движутся от лёгкого ветра.", "Seed": int64(45876641139403), "CreatedAt": now.Add(-95 * time.Minute), "Media": map[string]any{"ID": int64(4), "URL": "/preview/result.svg", "Filename": "AI-Gateway-MiniMaxH3-video.mp4", "MediaType": "video", "ExpiresUnix": now.Add(22*time.Hour + 25*time.Minute).UnixMilli(), "Sensitive": false}},
	}
	mux.HandleFunc("/preview/gallery", render("gallery", "Моя галерея", map[string]any{"Items": galleryItems, "ImageCount": 3, "VideoCount": 1}))
	mux.HandleFunc("/preview/gallery-empty", render("gallery", "Моя галерея", map[string]any{"Items": []map[string]any{}, "ImageCount": 0, "VideoCount": 0}))
	mux.HandleFunc("/preview/login", render("login", "Вход", map[string]any{"CurrentUser": nil, "Next": ""}))
	mux.HandleFunc("/preview/profile", render("account_profile", "Профиль", map[string]any{
		"ProfileUsername": "admin", "ProfileEmail": "admin@example.local", "CanChangeUsername": true,
	}))
	mux.HandleFunc("/preview/password", render("account_password", "Смена пароля", map[string]any{}))
	accountSessions := []domain.AccountSession{
		{ID: 71, Current: true, IP: "192.168.1.86", UserAgent: "Chrome 139 · Windows 11", CreatedAt: now.Add(-8 * time.Hour), LastSeenAt: now.Add(-time.Minute), ExpiresAt: now.Add(22 * time.Hour)},
		{ID: 68, IP: "192.168.1.24", UserAgent: "Chrome Mobile · Android 15 with a deliberately long device description", CreatedAt: now.Add(-36 * time.Hour), LastSeenAt: now.Add(-3 * time.Hour), ExpiresAt: now.Add(12 * time.Hour)},
	}
	mux.HandleFunc("/preview/account-sessions", render("account_sessions", "Активные сессии", map[string]any{"Sessions": accountSessions}))
	mux.HandleFunc("/preview/invite", render("invite", "Создание аккаунта", map[string]any{
		"CurrentUser": nil,
		"Invalid":     false,
		"Token":       "preview",
		"Access": domain.InviteAccess{
			GrantComfyUI:         true,
			GrantOpenWebUI:       true,
			GrantQuickGeneration: true,
			GrantTextToImage:     true,
			GenerationDailyLimit: 12,
			GenerationTotalLimit: 50,
		},
	}))
	hostHistory := []domain.HostMetric{
		{RecordedAt: now.Add(-18 * time.Minute), CPUPercent: 34, MemoryUsedBytes: 24 << 30, MemoryTotalBytes: 64 << 30, GPUAvailable: true, GPUName: "NVIDIA GeForce RTX", GPUPercent: 62, GPUMemoryUsedBytes: 13 << 30, GPUMemoryTotalBytes: 24 << 30},
		{RecordedAt: now.Add(-12 * time.Minute), CPUPercent: 51, MemoryUsedBytes: 27 << 30, MemoryTotalBytes: 64 << 30, GPUAvailable: true, GPUName: "NVIDIA GeForce RTX", GPUPercent: 84, GPUMemoryUsedBytes: 18 << 30, GPUMemoryTotalBytes: 24 << 30},
		{RecordedAt: now.Add(-7 * time.Minute), CPUPercent: 28, MemoryUsedBytes: 26 << 30, MemoryTotalBytes: 64 << 30, GPUAvailable: true, GPUName: "NVIDIA GeForce RTX", GPUPercent: 46, GPUMemoryUsedBytes: 12 << 30, GPUMemoryTotalBytes: 24 << 30},
		{RecordedAt: now.Add(-2 * time.Minute), CPUPercent: 19, MemoryUsedBytes: 25 << 30, MemoryTotalBytes: 64 << 30, GPUAvailable: true, GPUName: "NVIDIA GeForce RTX", GPUPercent: 17, GPUMemoryUsedBytes: 9 << 30, GPUMemoryTotalBytes: 24 << 30},
	}
	lastHost := hostHistory[len(hostHistory)-1]
	lastSuccess := now.Add(-4 * time.Second)
	nextCheck := now.Add(10 * time.Minute)
	lastError := now.Add(-19 * time.Second)
	dependencies := []gateway.DependencyStatus{
		{Key: "comfyui", Name: "ComfyUI", State: gateway.DependencyOnline, StateLabel: "В сети", Detail: "Соединение подтверждено.", LastSuccessAt: timePointer(lastSuccess), LastDataAt: timePointer(lastSuccess), NextCheckAt: timePointer(nextCheck), RetryInSeconds: 6, LatencyMillis: 18},
		{Key: "openwebui", Name: "OpenWebUI", State: gateway.DependencyOnline, StateLabel: "В сети", Detail: "Соединение подтверждено.", LastSuccessAt: timePointer(lastSuccess), LastDataAt: timePointer(lastSuccess), NextCheckAt: timePointer(nextCheck), RetryInSeconds: 6, LatencyMillis: 22},
		{Key: "ollama", Name: "Промт-ассистент", State: gateway.DependencyOffline, StateLabel: "Нет связи", Detail: "Подключение отклонено.", LastErrorAt: timePointer(lastError), LastError: "connection refused", NextCheckAt: timePointer(nextCheck), RetryInSeconds: 6},
		{Key: "moderator", Name: "Проверка 18+", State: gateway.DependencyOnline, StateLabel: "В сети", Detail: "Соединение подтверждено.", LastSuccessAt: timePointer(lastSuccess), LastDataAt: timePointer(lastSuccess), NextCheckAt: timePointer(nextCheck), RetryInSeconds: 6, LatencyMillis: 31},
		{Key: "mining-agent", Name: "Управление майнингом", State: gateway.DependencyOnline, StateLabel: "В сети", Detail: "Heartbeat получен.", LastSuccessAt: timePointer(lastSuccess), LastDataAt: timePointer(lastSuccess), NextCheckAt: timePointer(nextCheck), RetryInSeconds: 6, LatencyMillis: 9},
		{Key: "system-monitor", Name: "Мониторинг Windows", State: gateway.DependencyStale, StateLabel: "Данные устарели", Detail: "Последние данные устарели; ждём следующую успешную проверку.", LastSuccessAt: timePointer(lastSuccess), LastDataAt: timePointer(lastHost.RecordedAt), LastErrorAt: timePointer(lastError), LastError: "Не удалось получить метрики Windows.", NextCheckAt: timePointer(nextCheck), RetryInSeconds: 6, RequiresFreshData: true, LatencyMillis: 15},
	}
	workerSuccess := now.Add(-22 * time.Second)
	workerNext := now.Add(8 * time.Second)
	workers := []gateway.MaintenanceWorkerState{
		{Key: "generation_jobs", Name: "Задания генераций", Status: "running", StatusLabel: "Выполняется", Interval: 30 * time.Second, Timeout: 20 * time.Second, Running: true, LastStartedAt: timePointer(now.Add(-2 * time.Second)), LastSuccessAt: timePointer(workerSuccess), LastDurationMillis: 842, LastItems: 3},
		{Key: "mining_leases", Name: "Аренды майнинга", Status: "healthy", StatusLabel: "Работает", Interval: 30 * time.Second, Timeout: 15 * time.Second, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 74, LastItems: 1},
		{Key: "host_metrics", Name: "Метрики Windows", Status: "retrying", StatusLabel: "Повтор после ошибки", Interval: 30 * time.Second, Timeout: 8 * time.Second, LastFinishedAt: timePointer(lastError), NextRunAt: timePointer(workerNext), LastDurationMillis: 3012, ConsecutiveFailures: 2, LastError: "Windows-agent временно недоступен"},
		{Key: "observability_snapshot", Name: "Снимок наблюдаемости", Status: "healthy", StatusLabel: "Работает", Interval: 30 * time.Second, Timeout: 8 * time.Second, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 28, LastItems: 1},
		{Key: "comfy_memory", Name: "Освобождение памяти ComfyUI", Status: "healthy", StatusLabel: "Работает", Interval: 10 * time.Second, Timeout: 8 * time.Second, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 118, LastItems: 0},
		{Key: "dependency_health", Name: "Состояние зависимостей", Status: "healthy", StatusLabel: "Работает", Interval: 10 * time.Second, Timeout: 4 * time.Second, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 346, LastItems: 6},
		{Key: "websocket_authorization", Name: "Авторизация WebSocket", Status: "healthy", StatusLabel: "Работает", Interval: time.Minute, Timeout: 8 * time.Second, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 12, LastItems: 0},
		{Key: "suggestion_scans", Name: "Проверка предложений", Status: "waiting", StatusLabel: "Ожидает запуска", Interval: 15 * time.Minute, Timeout: 2 * time.Minute, NextRunAt: timePointer(workerNext)},
		{Key: "media_archive", Name: "Архивация результатов", Status: "healthy", StatusLabel: "Работает", Interval: 15 * time.Minute, Timeout: 2 * time.Minute, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 1640, LastItems: 4},
		{Key: "media_hashes", Name: "Хэши архивных медиа", Status: "healthy", StatusLabel: "Работает", Interval: 15 * time.Minute, Timeout: 2 * time.Minute, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 920, LastItems: 4},
		{Key: "comfy_input_cleanup", Name: "Очистка входных файлов", Status: "healthy", StatusLabel: "Работает", Interval: 15 * time.Minute, Timeout: 3 * time.Minute, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 223, LastItems: 2},
		{Key: "comfy_media_cleanup", Name: "Очистка результатов ComfyUI", Status: "healthy", StatusLabel: "Работает", Interval: 15 * time.Minute, Timeout: 3 * time.Minute, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 510, LastItems: 8},
		{Key: "other_media_cleanup", Name: "Очистка остальных медиа", Status: "healthy", StatusLabel: "Работает", Interval: 15 * time.Minute, Timeout: 15 * time.Second, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 63, LastItems: 0},
		{Key: "database_retention", Name: "Сроки хранения БД", Status: "healthy", StatusLabel: "Работает", Interval: 15 * time.Minute, Timeout: 2 * time.Minute, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 1840, LastItems: 1243},
		{Key: "session_cleanup", Name: "Очистка сессий", Status: "healthy", StatusLabel: "Работает", Interval: 15 * time.Minute, Timeout: 15 * time.Second, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 84, LastItems: 2},
		{Key: "temporary_users", Name: "Удаление временных аккаунтов", Status: "healthy", StatusLabel: "Работает", Interval: 15 * time.Minute, Timeout: 15 * time.Second, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 48, LastItems: 1},
		{Key: "content_cleanup", Name: "Очистка AI-контента", Status: "healthy", StatusLabel: "Работает", Interval: 15 * time.Minute, Timeout: 15 * time.Second, LastSuccessAt: timePointer(workerSuccess), NextRunAt: timePointer(workerNext), LastDurationMillis: 177, LastItems: 5},
	}
	mux.HandleFunc("/preview/admin", render("admin_dashboard", "Обзор системы", map[string]any{
		"System": gateway.SystemOverview{
			DatabaseBytes: 184 << 20,
			OnlineUsers: []domain.OnlineUser{
				{Username: "rayka", Role: "user", LastSeenAt: now.Add(-time.Minute)},
				{Username: "admin", Role: "admin", LastSeenAt: now.Add(-2 * time.Minute)},
			},
			Host: &lastHost, History: hostHistory, AgentAvailable: false,
			AgentMessage: dependencies[len(dependencies)-1].Detail, Agent: dependencies[len(dependencies)-1], Dependencies: dependencies, Workers: workers,
		},
		"Stats": domain.AdminStats{
			ActiveUsers: 3, RequestsToday: 147, Requests7Days: 824, ActiveWebSockets: 2, AverageDuration: 1160, ErrorRate: "0,8%",
			TopUsersRequests: []domain.TopUser{{Username: "rayka", Value: 302}, {Username: "demo4518", Value: 188}, {Username: "admin", Value: 96}},
			TopUsersTraffic:  []domain.TopUser{{Username: "rayka", Value: 4200000000}, {Username: "admin", Value: 1800000000}},
			UsageByService:   []domain.ServiceUsage{{Service: "comfyui", Requests: 516, Users: 3, Bytes: 6400000000, Errors: 2}, {Service: "openwebui", Requests: 308, Users: 3, Bytes: 18400000, Errors: 1}},
			Trend:            chart,
		},
	}))
	serviceStats := []domain.ServiceUsage{{Service: "comfyui", Requests: 516, Users: 3, Bytes: 6400000000, Errors: 2}, {Service: "openwebui", Requests: 308, Users: 3, Bytes: 18400000, Errors: 1}}
	observability := map[string]any{
		"Generation": domain.GenerationObservabilitySummary{ActiveJobs: 2, OverdueJobs: 1, Completed: 18, Failed: 2, Cancelled: 1, SuccessRate: 90, QueueP50MS: 13800, QueueP95MS: 64200, ExecutionP50MS: 186000, ExecutionP95MS: 1274000, ObservationHours: 24},
		"Gateway": domain.GatewayObservationSummary{
			Latest:                domain.GatewayObservation{DatabaseBytes: 238 << 20, ActiveJobs: 2, OverdueJobs: 1, ActiveLeases: 1, ContentModerationBacklog: 0, MediaModerationBacklog: 3, CleanupStatus: "ok", CleanupAgeSeconds: 682, RecordedAt: now.Add(-18 * time.Second)},
			DatabaseGrowth24Hours: 18 << 20,
		},
		"Outcomes": []domain.GenerationOutcomeGroup{
			{WorkflowID: "minimax-h3-video-v4", ModelName: "MiniMax_H3_FL2VA_pruned_int8", Total: 9, Completed: 8, Failed: 1, SuccessRate: 89},
			{WorkflowID: "krea2-text-to-image", ModelName: "Krea2_v40.safetensors", Total: 8, Completed: 8, SuccessRate: 100},
			{WorkflowID: "flux2-image-edit", ModelName: "Flux2-dev", Total: 4, Completed: 2, Failed: 1, Cancelled: 1, SuccessRate: 67},
		},
		"Failures": []domain.GenerationFailureSummary{
			{JobPublicID: "job-preview-minimax-000001", CorrelationID: "correlation-preview-minimax-000001", Username: "rayka", WorkflowID: "minimax-h3-video-v4", ModelName: "MiniMax_H3_FL2VA_pruned_int8", ErrorCode: "comfy_execution_failed", ErrorMessage: "RTXVideoSuperResolution: отсутствует обязательный параметр resize_type", FailedAt: now.Add(-37 * time.Minute)},
			{JobPublicID: "job-preview-flux2-0000002", CorrelationID: "correlation-preview-flux2-0000002", Username: "demo4518", WorkflowID: "flux2-image-edit", ModelName: "Flux2-dev", ErrorCode: "workflow_validation_failed", ErrorMessage: "Выбранная модель не поддерживает этот вход", FailedAt: now.Add(-3 * time.Hour)},
		},
		"Latencies": []domain.ServiceLatencySummary{
			{Component: "comfyui", Operation: "submit_prompt", Samples: 22, Failures: 1, P50MS: 84, P95MS: 430, LastLatencyMS: 91, LastOutcome: "ok", LastObservedAt: now.Add(-8 * time.Minute)},
			{Component: "comfyui", Operation: "generation_status", Samples: 184, P50MS: 42, P95MS: 130, LastLatencyMS: 39, LastOutcome: "ok", LastObservedAt: now.Add(-22 * time.Second)},
			{Component: "ollama", Operation: "enhance_video", Samples: 7, Failures: 1, P50MS: 12800, P95MS: 47100, LastLatencyMS: 15300, LastOutcome: "ok", LastObservedAt: now.Add(-19 * time.Minute)},
			{Component: "moderator", Operation: "classify_image", Samples: 15, Failures: 2, P50MS: 1640, P95MS: 74000, LastLatencyMS: 75000, LastOutcome: "timeout", LastErrorCode: "moderation_failed", LastDetail: "context deadline exceeded", LastObservedAt: now.Add(-31 * time.Minute)},
			{Component: "database", Operation: "gateway_snapshot", Samples: 120, P50MS: 18, P95MS: 44, LastLatencyMS: 21, LastOutcome: "ok", LastObservedAt: now.Add(-18 * time.Second)},
		},
		"Leases":         []domain.QuickGenerationMiningLease{{ID: "lease-preview-001", CorrelationID: "correlation-preview-minimax-000001", GenerationJobID: 41, UserID: 2, MinerID: 1, ResumeMining: true, CreatedAt: now.Add(-4 * time.Minute)}},
		"Dependencies":   dependencies,
		"Workers":        workers,
		"WorkerIssues":   []gateway.MaintenanceWorkerState{workers[0], workers[2]},
		"HealthyWorkers": 15,
		"Host":           &lastHost,
		"GeneratedAt":    now,
		"OverdueAfter":   45 * time.Minute,
	}
	mux.HandleFunc("/preview/metrics", render("admin_metrics", "Наблюдаемость", map[string]any{
		"Stats":         domain.AdminStats{RequestsToday: 147, Requests7Days: 824, ActiveWebSockets: 2, ErrorRate: "0,8%", Trend: chart},
		"ServiceStats":  serviceStats,
		"Observability": observability,
	}))
	traceJob := domain.GenerationJob{ID: 41, PublicID: "job-preview-minimax-000001", CorrelationID: "correlation-preview-minimax-000001", UsernameSnapshot: "rayka", RequestID: "request-preview-minimax-000001", PromptID: "9a0b0e16-67b2-46c2-ae10-dc09901bb919", TemplateID: "minimax-h3-video", WorkflowID: "minimax-h3-video-v4", ModelName: "MiniMax_H3_FL2VA_pruned_int8", Seed: 3950205217521, State: domain.GenerationJobFailed, StatusMessage: "ComfyUI завершил генерацию с ошибкой", ErrorCode: "comfy_execution_failed", ErrorMessage: "RTXVideoSuperResolution: отсутствует обязательный параметр resize_type", Attempt: 1, InputCount: 2, CreatedAt: now.Add(-58 * time.Minute), StateChangedAt: now.Add(-37 * time.Minute), FinishedAt: timePointer(now.Add(-37 * time.Minute))}
	trace := domain.GenerationJobTrace{
		Job: traceJob,
		Transitions: []domain.GenerationJobTransition{
			{JobID: 41, CorrelationID: traceJob.CorrelationID, ToState: domain.GenerationJobDraft, Message: "Запуск создан", CreatedAt: traceJob.CreatedAt},
			{JobID: 41, CorrelationID: traceJob.CorrelationID, FromState: domain.GenerationJobDraft, ToState: domain.GenerationJobPreparing, Message: "Проверяем параметры и workflow", DurationMS: 320, CreatedAt: traceJob.CreatedAt.Add(320 * time.Millisecond)},
			{JobID: 41, CorrelationID: traceJob.CorrelationID, FromState: domain.GenerationJobPreparing, ToState: domain.GenerationJobWaitingForResources, Message: "Ожидаем ресурсы", DurationMS: 1840, CreatedAt: traceJob.CreatedAt.Add(2160 * time.Millisecond)},
			{JobID: 41, CorrelationID: traceJob.CorrelationID, FromState: domain.GenerationJobWaitingForResources, ToState: domain.GenerationJobQueued, Message: "Генерация поставлена в очередь ComfyUI", DurationMS: 9100, CreatedAt: traceJob.CreatedAt.Add(11260 * time.Millisecond)},
			{JobID: 41, CorrelationID: traceJob.CorrelationID, FromState: domain.GenerationJobQueued, ToState: domain.GenerationJobRunning, Message: "ComfyUI выполняет workflow", DurationMS: 58000, CreatedAt: traceJob.CreatedAt.Add(69 * time.Second)},
			{JobID: 41, CorrelationID: traceJob.CorrelationID, FromState: domain.GenerationJobRunning, ToState: domain.GenerationJobFailed, Message: "ComfyUI завершил генерацию с ошибкой", ErrorCode: traceJob.ErrorCode, ErrorMessage: traceJob.ErrorMessage, DurationMS: 1189000, CreatedAt: now.Add(-37 * time.Minute)},
		},
		ServiceObservations: []domain.ServiceObservationRecord{
			{Component: "comfyui", Operation: "submit_prompt", Outcome: "ok", LatencyMS: 96, CorrelationID: traceJob.CorrelationID, ObservedAt: traceJob.CreatedAt.Add(11 * time.Second)},
			{Component: "comfyui", Operation: "generation_status", Outcome: "ok", LatencyMS: 41, CorrelationID: traceJob.CorrelationID, ObservedAt: now.Add(-38 * time.Minute)},
		},
		ProxyRequests: []domain.TraceProxyRequest{{ID: 81, RequestID: traceJob.RequestID, CorrelationID: traceJob.CorrelationID, Service: "comfyui", Method: "POST", Path: "/generate/run/" + traceJob.RequestID, Status: 202, DurationMS: 12740, CreatedAt: traceJob.CreatedAt}},
		AuditEvents:   []domain.TraceAuditEvent{{ID: 51, RequestID: traceJob.RequestID, CorrelationID: traceJob.CorrelationID, Action: "quick_generation_started", TargetType: "comfyui", Metadata: `{}`, CreatedAt: traceJob.CreatedAt.Add(12 * time.Second)}},
		ContentEvents: []domain.TraceContentEvent{{ID: 65, CorrelationID: traceJob.CorrelationID, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: traceJob.PromptID, Model: traceJob.ModelName, GenerationState: "error", CreatedAt: traceJob.CreatedAt.Add(12 * time.Second)}},
		MiningLease:   &domain.QuickGenerationMiningLease{ID: "lease-preview-001", CorrelationID: traceJob.CorrelationID, GenerationJobID: 41, UserID: 2, MinerID: 1, ResumeMining: true, CreatedAt: traceJob.CreatedAt.Add(2 * time.Second)},
	}
	mux.HandleFunc("/preview/job-trace", render("admin_job_trace", "Трасса генерации", map[string]any{"Trace": trace}))
	storageManaged := []map[string]any{
		{"Name": "content_media", "Label": "Медиа генераций", "Owner": "Контроль контента", "Retention": "24 часа", "Configuration": "GENERATION_RETENTION", "Managed": true, "EstimatedRows": int64(9), "TotalBytes": int64(225116160), "OldestAt": timePointer(now.Add(-22 * time.Hour))},
		{"Name": "proxy_requests", "Label": "Запросы к сервисам", "Owner": "Телеметрия Gateway", "Retention": "90 дней", "Configuration": "PROXY_REQUEST_RETENTION", "Managed": true, "EstimatedRows": int64(19931), "TotalBytes": int64(8781824), "OldestAt": timePointer(now.Add(-38 * 24 * time.Hour))},
		{"Name": "host_metrics", "Label": "Метрики компьютера", "Owner": "Мониторинг", "Retention": "7 дней", "Configuration": "HOST_METRIC_RETENTION", "Managed": true, "EstimatedRows": int64(9588), "TotalBytes": int64(2072576), "OldestAt": timePointer(now.Add(-7 * 24 * time.Hour))},
		{"Name": "audit_log", "Label": "Журнал аудита", "Owner": "Безопасность", "Retention": "90 дней", "Configuration": "AUDIT_LOG_RETENTION", "Managed": true, "EstimatedRows": int64(710), "TotalBytes": int64(393216), "OldestAt": timePointer(now.Add(-64 * 24 * time.Hour))},
		{"Name": "quick_generation_variants", "Label": "История генераций", "Owner": "Быстрая генерация", "Retention": "24 часа", "Configuration": "GENERATION_RETENTION", "Managed": true, "EstimatedRows": int64(12), "TotalBytes": int64(311296), "OldestAt": timePointer(now.Add(-20 * time.Hour))},
		{"Name": "future_table", "Label": "future_table", "Owner": "Не назначен", "Retention": "Политика не задана", "Unmapped": true, "EstimatedRows": int64(4), "TotalBytes": int64(65536), "OldestAt": timePointer(now.Add(-3 * time.Hour))},
	}
	storageLifecycle := []map[string]any{
		{"Name": "users", "Label": "Учётные записи", "Owner": "Управление доступом", "Retention": "До удаления; временные — до срока", "EstimatedRows": int64(9), "TotalBytes": int64(122880), "OldestAt": timePointer(now.Add(-180 * 24 * time.Hour))},
		{"Name": "quick_generation_recipes", "Label": "Сохранённые рецепты", "Owner": "Пользователь", "Retention": "До ручного удаления", "EstimatedRows": int64(1), "TotalBytes": int64(49152), "OldestAt": timePointer(now.Add(-10 * 24 * time.Hour))},
		{"Name": "database_cleanup_state", "Label": "Состояние очистки БД", "Owner": "Система", "Retention": "Одна служебная запись", "EstimatedRows": int64(1), "TotalBytes": int64(32768), "OldestAt": timePointer(now.Add(-12 * time.Minute))},
	}
	mux.HandleFunc("/preview/storage", render("admin_storage", "Хранилище базы данных", map[string]any{
		"Storage": map[string]any{
			"DatabaseBytes": int64(238 << 20), "VisibleTableBytes": int64(237 << 20), "EstimatedRows": int64(30692), "UnmappedCount": 1,
			"ManagedTables": storageManaged, "LifecycleTables": storageLifecycle,
			"Cleanup": map[string]any{
				"Status": "partial", "StatusLabel": "Очистка завершена частично", "LastStartedAt": timePointer(now.Add(-12 * time.Minute)), "LastFinishedAt": timePointer(now.Add(-11*time.Minute - 58*time.Second)), "LastSuccessAt": timePointer(now.Add(-27 * time.Minute)), "DurationMS": int64(1840), "TotalDeleted": int64(1243),
				"Items": []map[string]any{{"Table": "proxy_requests", "Label": "Запросы к сервисам", "Deleted": int64(1000)}, {"Table": "host_metrics", "Label": "Метрики компьютера", "Deleted": int64(243)}, {"Table": "audit_log", "Label": "Журнал аудита", "Error": "контекст очистки завершился до обработки таблицы"}},
			},
		},
	}))
	serviceAnalytics := domain.ServiceAnalytics{
		Service: "comfyui", DisplayName: "ComfyUI", Requests: 516, Users: 3, Bytes: 6400000000, Errors: 2, AverageDuration: 1160, ActiveWebSockets: 2,
		Trend: []domain.ServiceTrendPoint{{Label: "Сегодня", Requests: 84, Users: 3, Errors: 1, Bytes: 1600000000, RequestPercent: 100}, {Label: "Вчера", Requests: 61, Users: 3, Bytes: 1200000000, RequestPercent: 73}, {Label: "10 июл", Requests: 46, Users: 2, Errors: 1, Bytes: 920000000, RequestPercent: 55}},
	}
	mux.HandleFunc("/preview/service", render("admin_service", "ComfyUI", map[string]any{"Analytics": serviceAnalytics}))
	mux.HandleFunc("/preview/mining", render("admin_mining", "Управление майнингом", map[string]any{"Mining": miningOverview, "Error": "", "Status": ""}))
	mux.HandleFunc("/preview/updates", render("admin_updates", "Обновления", map[string]any{
		"Updates": updates.Status{Available: true, Components: []updates.ComponentStatus{
			{Name: updates.ComponentGateway, DisplayName: "AI Access Gateway", Configured: true, CurrentVersion: "16278624f83b", LatestVersion: "16278624f83b"},
			{Name: updates.ComponentComfyUI, DisplayName: "ComfyUI", Configured: true, CurrentVersion: "9a9fdb10ed14", LatestVersion: "9a9fdb10ed14"},
			{Name: updates.ComponentOpenWebUI, DisplayName: "Open WebUI", Configured: true, CurrentVersion: "v0.9.5", LatestVersion: "v0.11.0", UpdateAvailable: true, CanInstall: true},
		}},
		"Overview": gateway.UpdateOverview{Available: 1, Current: 2},
		"Message":  "",
	}))
	mux.HandleFunc("/preview/workflows", render("admin_workflows", "Совместимость workflow", map[string]any{
		"Report": map[string]any{
			"Snapshot":    map[string]any{"FetchedAt": now.Add(-2 * time.Minute)},
			"SourceLabel": "свежий каталог ComfyUI",
			"Fingerprint": "c42b3f9a7d81",
			"NodeCount":   418,
			"Compatible":  7,
			"Failed":      1,
			"Unavailable": 1,
			"Results": []map[string]any{
				{"Status": "error", "Scenario": "Полная обработка видео", "Family": "MiniMax H3 v4", "Description": "RIFE, RTX, ColorMatch и Sharpen", "Model": "MiniMax H3 FL2VA INT8 ConvRot", "NodeCount": 31, "Issues": []map[string]any{{"Label": "Финальный апскейл", "Message": "У ноды отсутствует обязательный параметр resize_type.", "ClassType": "RTXVideoSuperResolution", "InputName": "resize_type"}}},
				{"Status": "unavailable", "Scenario": "Готовность модели", "Family": "Flux2", "Description": "Модель отсутствует в установленном ComfyUI", "Model": "Flux 2 Klein 9B", "NodeCount": 0, "Issues": []map[string]any{{"Label": "Зависимости модели", "Message": "Checkpoint не найден в каталоге diffusion_models."}}},
				{"Status": "ok", "Scenario": "Первый и последний кадр", "Family": "MiniMax H3 v4", "Description": "FL2VA с двумя точными кадрами", "Model": "MiniMax H3 FL2VA INT8 ConvRot", "NodeCount": 24, "Issues": []map[string]any{}},
			},
		},
		"Message": "",
	}))
	users := []domain.UserRow{
		{ID: 1, Username: "admin", Email: "admin@example.local", Role: "admin", CanUseComfyUI: true, CanUseOpenWebUI: true, Requests: 96, LastLoginAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true}},
		{ID: 2, Username: "rayka", Email: "rayka@example.local", Role: "user", CanUseComfyUI: true, CanUseOpenWebUI: true, Requests: 302, LastLoginAt: sql.NullTime{Time: now.Add(-8 * time.Minute), Valid: true}, AccountExpiresAt: sql.NullTime{Time: now.Add(5*time.Hour + 43*time.Minute + 18*time.Second), Valid: true}},
		{ID: 3, Username: "demo4518", Role: "user", CanUseOpenWebUI: true, Requests: 188, LastLoginAt: sql.NullTime{Time: now.Add(-24 * time.Hour), Valid: true}},
		{ID: 4, Username: "disabled-user-with-long-name", Email: "long.address@example.local", Role: "user", Disabled: true, CanUseComfyUI: true, Requests: 4, AccountExpiresAt: sql.NullTime{Time: now.Add(-2 * time.Minute), Valid: true}},
	}
	mux.HandleFunc("/preview/users", render("admin_users", "Пользователи", map[string]any{"Users": users, "Query": ""}))
	adminSessions := []domain.SessionRow{
		{ID: 71, Username: "admin", IP: "192.168.1.86", UserAgent: "Chrome 139 · Windows 11", CreatedAt: now.Add(-8 * time.Hour), LastSeenAt: now.Add(-time.Minute), ExpiresAt: now.Add(22 * time.Hour)},
		{ID: 68, Username: "rayka", IP: "192.168.1.24", UserAgent: "Chrome Mobile · Android 15 with a deliberately long device description", CreatedAt: now.Add(-36 * time.Hour), LastSeenAt: now.Add(-3 * time.Hour), ExpiresAt: now.Add(12 * time.Hour)},
	}
	mux.HandleFunc("/preview/admin-sessions", render("admin_sessions", "Активные сессии", map[string]any{"Sessions": adminSessions}))
	audits := []domain.AuditRow{
		{ID: 15, Actor: "admin", Action: "user_service_access_updated", TargetType: "user", TargetID: sql.NullInt64{Int64: 2, Valid: true}, IP: "192.168.1.86", CreatedAt: now.Add(-4 * time.Minute), Metadata: `{"quick_generation":true,"video":true}`},
		{ID: 14, Action: "temporary_users_cleanup", TargetType: "system", IP: "127.0.0.1", CreatedAt: now.Add(-2 * time.Hour), Metadata: `{"deleted":2}`},
	}
	mux.HandleFunc("/preview/audit", render("admin_audit", "Журнал аудита", map[string]any{"Audits": audits}))
	invites := []domain.InviteRow{
		{ID: 48, CreatedBy: "admin", MaxUses: 1, ExpiresAt: now.Add(48 * time.Hour), GrantComfyUI: true, GrantOpenWebUI: true, Status: "active", CreatedAt: now.Add(-time.Hour)},
		{ID: 47, CreatedBy: "admin", MaxUses: 3, UsedCount: 1, ExpiresAt: now.Add(24 * time.Hour), Revoked: true, GrantOpenWebUI: true, Status: "revoked", CreatedAt: now.Add(-4 * time.Hour)},
		{ID: 46, CreatedBy: "admin", MaxUses: 1, UsedCount: 1, ExpiresAt: now.Add(-24 * time.Hour), GrantComfyUI: true, Status: "used", CreatedAt: now.Add(-72 * time.Hour)},
	}
	mux.HandleFunc("/preview/invites", render("admin_invites", "Invites UI preview", map[string]any{"Invites": invites, "InviteLink": "https://ai.example.test/invite/preview-token"}))
	profile := domain.User{ID: 2, Username: "rayka", Email: sql.NullString{String: "rayka@example.local", Valid: true}, Role: "user", CanUseComfyUI: true, CanUseOpenWebUI: true, CreatedAt: now.Add(-720 * time.Hour), LastLoginAt: sql.NullTime{Time: now.Add(-8 * time.Minute), Valid: true}, AccountExpiresAt: sql.NullTime{Time: now.Add(5*time.Hour + 43*time.Minute + 18*time.Second), Valid: true}}
	mux.HandleFunc("/preview/user", render("admin_user_detail", "Пользователь rayka", map[string]any{
		"Profile": profile, "Stats": domain.UserStats{TotalRequests: 302, TotalBytesOut: 4200000000, LastService: "ComfyUI", Chart: chart, ByService: []domain.ServiceUsage{{Service: "comfyui", Requests: 210, Bytes: 4100000000, Errors: 1}, {Service: "openwebui", Requests: 92, Bytes: 8700000}}}, "Activities": activities,
		"PasswordStatus": "", "AccessStatus": "", "SecurityStatus": "", "AccountLocked": false,
	}))
	mux.HandleFunc("/preview/content", render("admin_content", "AI-контент", map[string]any{
		"Username": "", "Service": "", "Query": "",
		"Overview":       gateway.ContentOverview{Total: 28, ComfyUI: 11, OpenWebUI: 17, WithMedia: 6},
		"RetentionStats": domain.ContentRetentionStats{EventCount: 28, MediaCount: 6, MediaBytes: 2480000 * 6, NextMediaExpiry: timePointer(now.Add(18 * time.Hour)), NextEventExpiry: timePointer(now.Add(6 * 24 * time.Hour))},
		"Events": []gateway.ContentEventView{
			{ID: 3, UserID: 2, Username: "rayka", Service: "comfyui", Kind: "generation", Model: "Krea2 / Raw INT8 Mixed", Prompt: "Editorial portrait of a woman holding a perfume bottle in a bright studio.", Metadata: `{"seed":284797972294826,"megapixels":1.9}`, Assistant: &gateway.ContentAssistantView{Applied: true, Template: "photographic", OriginalPrompt: "Девушка показывает флакон духов", Suggestion: "Create an editorial portrait of a woman naturally presenting a premium perfume bottle."}, GenerationState: "completed", MediaCount: 1, Media: []domain.ContentMediaSummary{{ID: 42, EventID: 3, MediaType: "image"}}, CreatedAt: now.Add(-5 * time.Minute), ExpiresAt: now.Add(7 * 24 * time.Hour)},
			{ID: 2, UserID: 2, Username: "rayka", Service: "comfyui", Kind: "generation", Model: "Flux 2 / Klein 9B", Prompt: "Adult editorial portrait used to verify sensitive-content masking.", Metadata: `{"seed":1019794942414480}`, GenerationState: "completed", Sensitive: true, MediaCount: 1, Media: []domain.ContentMediaSummary{{ID: 43, EventID: 2, MediaType: "image"}}, CreatedAt: now.Add(-7 * time.Minute), ExpiresAt: now.Add(7 * 24 * time.Hour)},
			{ID: 1, UserID: 2, Username: "rayka", Service: "comfyui", Kind: "generation", Model: "Krea2 / Raw INT8 Mixed", Prompt: "A detailed fashion portrait with dramatic lighting.", Metadata: `{"seed":45876641139403}`, GenerationState: "error", CreatedAt: now.Add(-9 * time.Minute), ExpiresAt: now.Add(7 * 24 * time.Hour)},
			{ID: 4, UserID: 2, Username: "rayka", Service: "comfyui", Kind: "generation", Model: "Krea2 / Raw INT8 Mixed", Prompt: "Archived generation with parameters retained for review.", Metadata: `{"seed":8675309}`, GenerationState: "completed", GeneratedMediaCount: 1, MediaExpired: true, MediaExpiresAt: now.Add(-2 * time.Hour), CreatedAt: now.Add(-26 * time.Hour), ExpiresAt: now.Add(6 * 24 * time.Hour)},
		},
	}))
	mux.HandleFunc("/preview/media", render("admin_media_viewer", "Просмотр результата", map[string]any{"Filename": "AI-Gateway-preview-result.png", "MediaID": int64(42), "MediaType": "image"}))
	mux.HandleFunc("/preview/suggestions", render("suggestions", "Предложить улучшение", map[string]any{"VirusTotalConfigured": true}))
	previewSuggestions := []map[string]any{{
		"ID": int64(9), "Username": "rayka", "Title": "Новая LoRA для портретного движения", "Description": "Добавить адаптер для более естественного движения волос и ткани.", "Status": "clean", "CreatedAt": now.Add(-5 * time.Hour),
		"Links": []string{"https://example.test/model/9"}, "JSONName": "portrait-motion.json", "JSONSize": 18432,
		"Scans": []map[string]any{{"SourceName": "Ссылка 1", "Status": "completed", "Harmless": 73, "Undetected": 4, "Malicious": 0, "Suspicious": 0}},
	}}
	mux.HandleFunc("/preview/admin-suggestions", render("admin_suggestions", "Предложения пользователей", map[string]any{"VirusTotalConfigured": true, "Suggestions": previewSuggestions}))
	mux.HandleFunc("/preview/bad-gateway", render("bad_gateway", "Сервис недоступен", map[string]any{"Service": "ComfyUI"}))
	mux.HandleFunc("/preview/forbidden", render("service_forbidden", "Доступ запрещён", map[string]any{"Service": "ComfyUI"}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/preview/admin", http.StatusFound) })

	log.Println("UI preview listening on http://0.0.0.0:18080")
	log.Fatal(http.ListenAndServe(":18080", mux))
}

func timePointer(value time.Time) *time.Time {
	return &value
}
