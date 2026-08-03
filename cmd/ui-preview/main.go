package main

import (
	"database/sql"
	"log"
	"net/http"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
	"ai-access-gateway/internal/mining"
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

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("internal/gateway/static"))))
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
	mux.HandleFunc("/preview/login", render("login", "Вход", map[string]any{"CurrentUser": nil, "Next": ""}))
	mux.HandleFunc("/preview/invite", render("invite", "Создание аккаунта", map[string]any{
		"CurrentUser": nil,
		"Invalid":     false,
		"Token":       "preview",
		"Access":      map[string]any{"GrantComfyUI": true, "GrantOpenWebUI": true},
	}))
	mux.HandleFunc("/preview/admin", render("admin_dashboard", "Обзор системы", map[string]any{
		"Services": []gateway.ServiceStatus{{Name: "ComfyUI", Online: true}, {Name: "OpenWebUI", Online: true}, {Name: "Ollama", Online: true}},
		"Stats": domain.AdminStats{
			ActiveUsers: 3, RequestsToday: 147, Requests7Days: 824, ActiveWebSockets: 2, AverageDuration: 1160, ErrorRate: "0,8%",
			TopUsersRequests: []domain.TopUser{{Username: "rayka", Value: 302}, {Username: "demo4518", Value: 188}, {Username: "admin", Value: 96}},
			TopUsersTraffic:  []domain.TopUser{{Username: "rayka", Value: 4200000000}, {Username: "admin", Value: 1800000000}},
			UsageByService:   []domain.ServiceUsage{{Service: "comfyui", Requests: 516, Users: 3, Bytes: 6400000000, Errors: 2}, {Service: "openwebui", Requests: 308, Users: 3, Bytes: 18400000, Errors: 1}},
			Trend:            chart,
		},
	}))
	mux.HandleFunc("/preview/mining", render("admin_mining", "Управление майнингом", map[string]any{"Mining": miningOverview, "Error": "", "Status": ""}))
	users := []domain.UserRow{
		{ID: 1, Username: "admin", Email: "admin@example.local", Role: "admin", CanUseComfyUI: true, CanUseOpenWebUI: true, Requests: 96, LastLoginAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true}},
		{ID: 2, Username: "rayka", Email: "rayka@example.local", Role: "user", CanUseComfyUI: true, CanUseOpenWebUI: true, Requests: 302, LastLoginAt: sql.NullTime{Time: now.Add(-8 * time.Minute), Valid: true}},
		{ID: 3, Username: "demo4518", Role: "user", CanUseOpenWebUI: true, Requests: 188, LastLoginAt: sql.NullTime{Time: now.Add(-24 * time.Hour), Valid: true}},
		{ID: 4, Username: "disabled-user-with-long-name", Email: "long.address@example.local", Role: "user", Disabled: true, CanUseComfyUI: true, Requests: 4},
	}
	mux.HandleFunc("/preview/users", render("admin_users", "Пользователи", map[string]any{"Users": users, "Query": ""}))
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
		"Events":   []gateway.ContentEventView{{ID: 1, UserID: 2, Username: "rayka", Service: "openwebui", Model: "gemma-4-abliterated:e4b", Prompt: "Подготовь краткий план развёртывания сервиса.", Response: "1. Проверить окружение.\n2. Создать резервную копию.\n3. Развернуть и проверить healthcheck.", Metadata: `{\"temperature\":0.2}`, CreatedAt: now.Add(-9 * time.Minute), ExpiresAt: now.Add(7 * 24 * time.Hour)}},
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/preview/admin", http.StatusFound) })

	log.Println("UI preview listening on http://0.0.0.0:18080")
	log.Fatal(http.ListenAndServe(":18080", mux))
}
