package main

import (
	"database/sql"
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
		Script: mining.Script{
			Path: minerViews[0].ScriptPath, SHA256: "23cb47f58a53cdfce153f6c746b6d8e89ad72b5b9af42a956e7d885d652d963a",
			Content: "@echo off\r\ncd %~dp0\r\ncls\r\n\r\nSRBMiner-MULTI.exe --algorithm-gpu pearlhash --pool ru.pearl.herominers.com:1200 --wallet PEARL_WALLET\r\npause\r\n",
		},
	}
	miningOverview.Default = &miningOverview.Miners[0]
	miningOverview.Active = &miningOverview.Miners[0]
	previewSVG := `<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="1000" viewBox="0 0 1600 1000"><rect width="1600" height="1000" fill="#13201d"/><rect x="120" y="120" width="1360" height="760" rx="28" fill="#20453a"/><circle cx="800" cy="500" r="250" fill="#71dfb9"/><path d="M420 650 700 360l190 180 150-140 260 250z" fill="#0a1714"/></svg>`

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
	mux.HandleFunc("/generate/variants", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"variants":[
			{"id":"preview-1","template_id":"text-to-image","model_name":"Krea2 / Raw INT8 Mixed","seed":284797972294826,"state":"completed","duration_seconds":15,"values":{"template_id":"text-to-image","generation_workflow":"photoflow-krea2","model":"krea2:test","positive_prompt":"A cinematic portrait of a woman in a quiet neon-lit street, realistic skin, dramatic atmosphere."},"media":[{"media_type":"image","url":"/preview/result.svg","sensitive":false}]},
			{"id":"preview-2","template_id":"text-to-image","model_name":"Krea2 / Raw INT8 Mixed","seed":1033409957175067,"state":"completed","duration_seconds":16,"values":{"template_id":"text-to-image","generation_workflow":"photoflow-krea2","model":"krea2:test","positive_prompt":"A calm seaside embankment at sunset with editorial fashion photography and soft detailed light."},"media":[{"media_type":"image","url":"/preview/result.svg","sensitive":false}]},
			{"id":"preview-3","template_id":"image-to-image","model_name":"Flux 2 / Klein 9B","seed":1019794942414480,"state":"completed","duration_seconds":15,"values":{"template_id":"image-to-image","generation_workflow":"photoflow-flux2-edit","model":"flux2:test","positive_prompt":"Keep the subject identity and composition, replace the background with a warm studio environment."},"media":[{"media_type":"image","url":"/preview/result.svg","sensitive":true}]},
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
	mux.HandleFunc("/preview/generate", render("generate", "Быстрая генерация", map[string]any{
		"Workflows": []map[string]any{
			{"ID": "text-to-image", "Name": "Текст в изображение", "Description": "Создаёт изображение по вашему описанию.", "RequiresImage": false},
			{"ID": "image-to-image", "Name": "Фото и промт", "Description": "Перерисовывает загруженное фото.", "RequiresImage": true},
			{"ID": "minimax-h3-video", "Name": "Видео", "Description": "Создаёт ролик из текста, кадров или референсов.", "RequiresImage": false, "AllowsImages": true},
		},
		"GenerationPresets": []map[string]any{
			{"ID": "photoflow-krea2", "TemplateID": "text-to-image", "Name": "PhotoFlow Krea2", "Description": "Двухэтапная генерация с апскейлом и детализацией.", "Family": "krea2", "Available": true, "ModelID": "krea2:test", "ModelCount": 2, "DefaultSteps": 8, "DefaultCFG": 1, "DefaultSampler": "euler", "DefaultScheduler": "simple", "LoraStrength": 0.8},
			{"ID": "photoflow-krea2-edit", "TemplateID": "image-to-image", "Name": "Krea 2: редактирование", "Description": "Редактирование первого фото с опциональным вторым референсом.", "Family": "krea2", "Available": true, "ModelID": "krea2:test", "ModelCount": 2, "DefaultSteps": 8, "DefaultCFG": 1, "DefaultSampler": "euler", "DefaultScheduler": "simple", "LoraStrength": 0.8, "RequiresImage": true, "AllowsImages": true, "MaxInputImages": 2},
			{"ID": "photoflow-flux2-edit", "TemplateID": "image-to-image", "Name": "Flux2 Редактирование", "Description": "Редактирование исходного изображения через совместимую схему Flux 2.", "Family": "flux2", "Available": true, "ModelID": "flux2:test", "ModelCount": 1, "DefaultSteps": 20, "DefaultCFG": 5, "DefaultSampler": "euler", "DefaultScheduler": "normal", "RequiresImage": true},
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
	mux.HandleFunc("/preview/admin", render("admin_dashboard", "Обзор системы", map[string]any{
		"Services": []gateway.ServiceStatus{{Name: "ComfyUI", Online: true}, {Name: "OpenWebUI", Online: true}, {Name: "Ollama", Online: true}},
		"System":   gateway.SystemOverview{},
		"Stats": domain.AdminStats{
			ActiveUsers: 3, RequestsToday: 147, Requests7Days: 824, ActiveWebSockets: 2, AverageDuration: 1160, ErrorRate: "0,8%",
			TopUsersRequests: []domain.TopUser{{Username: "rayka", Value: 302}, {Username: "demo4518", Value: 188}, {Username: "admin", Value: 96}},
			TopUsersTraffic:  []domain.TopUser{{Username: "rayka", Value: 4200000000}, {Username: "admin", Value: 1800000000}},
			UsageByService:   []domain.ServiceUsage{{Service: "comfyui", Requests: 516, Users: 3, Bytes: 6400000000, Errors: 2}, {Service: "openwebui", Requests: 308, Users: 3, Bytes: 18400000, Errors: 1}},
			Trend:            chart,
		},
	}))
	serviceStats := []domain.ServiceUsage{{Service: "comfyui", Requests: 516, Users: 3, Bytes: 6400000000, Errors: 2}, {Service: "openwebui", Requests: 308, Users: 3, Bytes: 18400000, Errors: 1}}
	mux.HandleFunc("/preview/metrics", render("admin_metrics", "Метрики", map[string]any{
		"Stats":        domain.AdminStats{RequestsToday: 147, Requests7Days: 824, ActiveWebSockets: 2, ErrorRate: "0,8%", Trend: chart},
		"ServiceStats": serviceStats,
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
	users := []domain.UserRow{
		{ID: 1, Username: "admin", Email: "admin@example.local", Role: "admin", CanUseComfyUI: true, CanUseOpenWebUI: true, Requests: 96, LastLoginAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true}},
		{ID: 2, Username: "rayka", Email: "rayka@example.local", Role: "user", CanUseComfyUI: true, CanUseOpenWebUI: true, Requests: 302, LastLoginAt: sql.NullTime{Time: now.Add(-8 * time.Minute), Valid: true}},
		{ID: 3, Username: "demo4518", Role: "user", CanUseOpenWebUI: true, Requests: 188, LastLoginAt: sql.NullTime{Time: now.Add(-24 * time.Hour), Valid: true}},
		{ID: 4, Username: "disabled-user-with-long-name", Email: "long.address@example.local", Role: "user", Disabled: true, CanUseComfyUI: true, Requests: 4},
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
	profile := domain.User{ID: 2, Username: "rayka", Email: sql.NullString{String: "rayka@example.local", Valid: true}, Role: "user", CanUseComfyUI: true, CanUseOpenWebUI: true, CreatedAt: now.Add(-720 * time.Hour), LastLoginAt: sql.NullTime{Time: now.Add(-8 * time.Minute), Valid: true}}
	mux.HandleFunc("/preview/user", render("admin_user_detail", "Пользователь rayka", map[string]any{
		"Profile": profile, "Stats": domain.UserStats{TotalRequests: 302, TotalBytesOut: 4200000000, LastService: "ComfyUI", Chart: chart, ByService: []domain.ServiceUsage{{Service: "comfyui", Requests: 210, Bytes: 4100000000, Errors: 1}, {Service: "openwebui", Requests: 92, Bytes: 8700000}}}, "Activities": activities,
		"PasswordStatus": "", "AccessStatus": "", "SecurityStatus": "", "AccountLocked": false,
	}))
	mux.HandleFunc("/preview/content", render("admin_content", "AI-контент", map[string]any{
		"Username": "", "Service": "", "Query": "",
		"Overview": gateway.ContentOverview{Total: 28, ComfyUI: 11, OpenWebUI: 17, WithMedia: 6},
		"Events": []gateway.ContentEventView{
			{ID: 3, UserID: 2, Username: "rayka", Service: "comfyui", Kind: "generation", Model: "Krea2 / Raw INT8 Mixed", Prompt: "Editorial portrait of a woman holding a perfume bottle in a bright studio.", Metadata: `{"seed":284797972294826,"megapixels":1.9}`, Assistant: &gateway.ContentAssistantView{Applied: true, Template: "photographic", OriginalPrompt: "Девушка показывает флакон духов", Suggestion: "Create an editorial portrait of a woman naturally presenting a premium perfume bottle."}, GenerationState: "completed", MediaCount: 1, Media: []domain.ContentMediaSummary{{ID: 42, EventID: 3, MediaType: "image"}}, CreatedAt: now.Add(-5 * time.Minute), ExpiresAt: now.Add(7 * 24 * time.Hour)},
			{ID: 2, UserID: 2, Username: "rayka", Service: "comfyui", Kind: "generation", Model: "Flux 2 / Klein 9B", Prompt: "Adult editorial portrait used to verify sensitive-content masking.", Metadata: `{"seed":1019794942414480}`, GenerationState: "completed", Sensitive: true, MediaCount: 1, Media: []domain.ContentMediaSummary{{ID: 43, EventID: 2, MediaType: "image"}}, CreatedAt: now.Add(-7 * time.Minute), ExpiresAt: now.Add(7 * 24 * time.Hour)},
			{ID: 1, UserID: 2, Username: "rayka", Service: "comfyui", Kind: "generation", Model: "Krea2 / Raw INT8 Mixed", Prompt: "A detailed fashion portrait with dramatic lighting.", Metadata: `{"seed":45876641139403}`, GenerationState: "error", CreatedAt: now.Add(-9 * time.Minute), ExpiresAt: now.Add(7 * 24 * time.Hour)},
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
