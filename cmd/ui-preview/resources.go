package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
	"ai-access-gateway/internal/mining"
)

func registerResourcesPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, templates *gateway.Templates, now time.Time, analytics domain.ServiceAnalytics) {
	var favoriteMu sync.Mutex
	favoriteSets := map[string]map[string]bool{}
	favoritesFor := func(w http.ResponseWriter, r *http.Request) (string, []string) {
		cookie, err := r.Cookie("preview-mining-user")
		key := ""
		if err == nil {
			key = cookie.Value
		}
		if key == "" {
			key = fmt.Sprintf("%d", time.Now().UnixNano())
			http.SetCookie(w, &http.Cookie{Name: "preview-mining-user", Value: key, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
		favoriteMu.Lock()
		defer favoriteMu.Unlock()
		paths := []string{}
		for path, enabled := range favoriteSets[key] {
			if enabled {
				paths = append(paths, path)
			}
		}
		return key, paths
	}
	view := func(service, scenario string) map[string]any {
		dep := gateway.DependencyStatus{Key: service, State: gateway.DependencyOnline, StateLabel: "В сети", Detail: "Проверка сервиса прошла.", LastCheckedAt: timePointer(now.Add(-3 * time.Second)), LastSuccessAt: timePointer(now.Add(-3 * time.Second))}
		groups := []map[string]any{}
		for i, title := range []string{"Генерация", "Обучение LoRA", "Описание кадров", "Промпт-ассистент"} {
			group := map[string]any{"Title": title, "Running": 0, "Reserved": 0, "Unconfirmed": 0, "Waiting": 0, "Paused": 0}
			if i == 0 && scenario == "paused" {
				group["Running"], group["Waiting"] = 1, 2
			}
			if i == 2 && scenario == "stale" {
				group["Unconfirmed"] = 1
			}
			groups = append(groups, group)
		}
		v := map[string]any{"At": now, "Service": service, "Dependency": dep, "GPU": groups, "GPUAt": now.Add(-time.Second), "ObservedAt": now.Add(-3 * time.Second), "ObservationLabel": "Исполнители не сообщили об активной работе", "ObservationDetail": "", "Pauses": []map[string]any{}, "Label": "В сети", "Tone": "online", "Detail": dep.Detail, "ProfileVersion": "", "Profiles": map[int64]map[string]any{}, "Mining": nil, "Analytics": nil}
		if scenario == "stale" {
			v["ObservationLabel"] = "Наблюдение устарело"
			v["ObservedAt"] = now.Add(-10 * time.Minute)
		}
		if scenario == "paused" {
			v["ObservationLabel"] = "Исполнители заняты или ожидают задачу"
			v["Pauses"] = []map[string]any{{"Label": "Генерация", "Count": 1, "Since": now.Add(-4 * time.Minute), "Ready": 0}}
		}
		if scenario == "offline" {
			dep.State, dep.StateLabel, dep.Detail, dep.LastError = gateway.DependencyOffline, "Нет связи", "Агент не ответил.", "Превышено время ожидания ответа"
			dep.LastSuccessAt = timePointer(now.Add(-15 * time.Minute))
			v["Dependency"], v["Label"], v["Tone"], v["Detail"] = dep, dep.StateLabel, "offline", dep.Detail
		}
		if service != "mining" {
			a := analytics
			if service == "openwebui" {
				a.Service, a.DisplayName = service, "OpenWebUI"
			}
			if scenario == "empty" {
				a.Requests, a.Users, a.Bytes, a.Errors, a.AverageDuration, a.ActiveWebSockets, a.Trend = 0, 0, 0, 0, 0, 0, nil
			}
			v["Analytics"] = a
			return v
		}
		overview := gateway.MiningOverview{Available: scenario != "offline", Agent: dep, Script: mining.Script{Content: "@echo off\nSRBMiner-MULTI.exe --algorithm example", SHA256: strings.Repeat("abc12345", 8)}}
		states := map[int64]map[string]any{}
		for i, name := range []string{"SRBMiner Pearl", "Резервный профиль", "Архивный профиль"} {
			id := int64(i + 1)
			if scenario == "empty" {
				break
			}
			if scenario == "long" && i == 1 {
				name = strings.Repeat("ДлинноеНазваниеПрофиля", 5)
			}
			running := i == 0 && scenario != "paused" && scenario != "offline" && scenario != "stopped" && scenario != "stale" && scenario != "external"
			state := mining.State{Available: scenario != "offline", Running: running, CollectedAt: now.Add(-2 * time.Second)}
			if running {
				state.StartedAt = now.Add(-5 * time.Hour)
				state.PIDs = []int{22928}
				state.ScriptPath = fmt.Sprintf(`C:\Mining\profile-%d\start.bat`, id)
			}
			if scenario == "offline" {
				state.CollectedAt = time.Time{}
			}
			miner := gateway.MinerView{Miner: domain.Miner{ID: id, Name: name, Enabled: i != 2, Default: i == 0, ProcessName: "SRBMiner-MULTI.exe", ScriptPath: fmt.Sprintf(`C:\Mining\profile-%d\start.bat`, id)}, State: state}
			if scenario == "long" && i == 1 {
				miner.ScriptPath = `C:\Mining\` + strings.Repeat("long_path_component_", 30) + `\start.bat`
			}
			overview.Miners = append(overview.Miners, miner)
			s := map[string]any{"ID": id, "Running": running, "Label": "Остановлен", "Tone": "neutral", "Reason": "", "CollectedAt": state.CollectedAt, "StartedAt": state.StartedAt, "PIDs": state.PIDs, "CanStart": i != 2 && scenario == "stopped", "CanStop": running, "CanUpdate": scenario != "offline" && scenario != "paused" && scenario != "stale", "CanChange": !running && scenario != "offline"}
			switch {
			case scenario == "offline":
				s["Label"], s["Tone"], s["Reason"] = "Не подтверждено", "stale", "Нет свежего ответа агента. Обновите состояние."
			case running:
				s["Label"], s["Tone"] = "Работает", "online"
				overview.Running = true
			case i == 2:
				s["Label"], s["Reason"] = "Профиль отключён", "Включите профиль перед запуском."
			case scenario == "paused":
				s["Label"], s["Tone"], s["Reason"] = "Пауза", "stale", "GPU зарезервирован для связанных задач."
			case scenario == "stale":
				s["Reason"] = "Есть выполняемая или неподтверждённая GPU-задача."
			case scenario != "stopped":
				s["Reason"] = "Сначала остановите работающий профиль."
			}
			states[id] = s
			s["CanRestart"] = running
		}
		if scenario == "external" {
			overview.Running = true
			overview.External = []mining.State{{Available: true, Running: true, CollectedAt: now, ProcessName: "onezerominer.exe", ScriptPath: `C:\Mining\OneZero\quantus.bat`, PIDs: []int{23148, 6764}}}
			for _, state := range states {
				state["CanStart"], state["CanUpdate"], state["CanRestart"] = false, false, false
				state["Reason"] = "Работает внешний майнер. Новый запуск заблокирован."
			}
		}
		v["Mining"], v["Profiles"], v["ProfileVersion"] = overview, states, "preview-mining-configuration"
		v["Label"], v["Tone"], v["Detail"] = "Остановлен", "neutral", "Майнинг не выполняется."
		if overview.Running {
			v["Label"], v["Tone"], v["Detail"] = "Работает", "online", "Майнинг использует вычислительные ресурсы."
		}
		if scenario == "paused" {
			v["Label"], v["Tone"], v["Detail"] = "Пауза для задач", "stale", "Запуск удерживается до подтверждённого завершения связанных задач."
		}
		if scenario == "offline" {
			v["Label"], v["Tone"], v["Detail"] = "Состояние не подтверждено", "stale", dep.Detail
		}
		return v
	}
	page := func(service string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && service == "mining" {
				_ = r.ParseMultipartForm(1 << 20)
				if r.FormValue("csrf") != "preview" {
					http.Error(w, "csrf", 403)
					return
				}
				status := map[string]string{"start": "started", "stop": "stopped", "restart": "restarted", "update": "miner-updated", "create": "created", "delete": "updated", "enable": "updated", "disable": "updated", "default": "updated"}[r.FormValue("action")]
				if status == "" {
					http.Error(w, "action", 400)
					return
				}
				http.Redirect(w, r, "/preview/mining?scenario=stopped&status="+status, http.StatusSeeOther)
				return
			}
			v := view(service, r.URL.Query().Get("scenario"))
			name, title := "admin_service", "ComfyUI"
			if service == "openwebui" {
				title = "OpenWebUI"
			}
			if service == "mining" {
				name, title = "admin_mining", "Майнинг"
			}
			q := url.Values{"service": {service}, "scenario": {r.URL.Query().Get("scenario")}}
			files := mining.FileCatalog{}
			if service == "mining" && (r.URL.Query().Get("scenario") == "files" || r.URL.Query().Get("scenario") == "external") {
				files = mining.FileCatalog{Root: `C:\Mining`, Files: []mining.LaunchFile{
					{Path: `C:\Mining\SRBMiner\pearl.bat`, Name: "pearl.bat", Folder: "SRBMiner", Kind: "script"},
					{Path: `C:\Mining\SRBMiner\other-pool.bat`, Name: "other-pool.bat", Folder: "SRBMiner", Kind: "script"},
					{Path: `C:\Mining\SRBMiner\SRBMiner-MULTI.exe`, Name: "SRBMiner-MULTI.exe", Folder: "SRBMiner", Kind: "executable"},
					{Path: `C:\Mining\OtherMiner\start.bat`, Name: "start.bat", Folder: "OtherMiner", Kind: "script"},
					{Path: `C:\Mining\OtherMiner\miner.exe`, Name: "miner.exe", Folder: "OtherMiner", Kind: "executable"},
				}}
				files.Files = append(files.Files, mining.LaunchFile{Path: `C:\Mining\OneZero\quantus.bat`, Name: "quantus.bat", Folder: "OneZero", Kind: "script"}, mining.LaunchFile{Path: `C:\Mining\OneZero\onezerominer.exe`, Name: "onezerominer.exe", Folder: "OneZero", Kind: "executable"})
			}
			_, favorites := favoritesFor(w, r)
			render(name, title, map[string]any{"Resource": v, "Mining": v["Mining"], "MiningFiles": files, "MiningBrowser": gateway.BuildMiningBrowser(files, favorites, r.URL.Query()), "Error": "", "Status": r.URL.Query().Get("status"), "PublicBaseURL": "", "ResourceEndpoint": "/admin/resources/overview?" + q.Encode()})(w, r)
		}
	}
	mux.HandleFunc("/preview/service", page("comfyui"))
	mux.HandleFunc("/preview/mining", page("mining"))
	mux.HandleFunc("/admin/mining", page("mining"))
	mux.HandleFunc("/admin/mining/favorite", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(16 << 10)
		if r.Method != http.MethodPost || r.FormValue("csrf") != "preview" {
			http.Error(w, "csrf", 403)
			return
		}
		key, _ := favoritesFor(w, r)
		favoriteMu.Lock()
		if favoriteSets[key] == nil {
			favoriteSets[key] = map[string]bool{}
		}
		enabled := r.FormValue("enabled") == "true"
		favoriteSets[key][r.FormValue("path")] = enabled
		count := 0
		for _, value := range favoriteSets[key] {
			if value {
				count++
			}
		}
		favoriteMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"favorite": enabled, "count": count})
	})
	mux.HandleFunc("/admin/mining/script", func(w http.ResponseWriter, r *http.Request) {
		miner := domain.Miner{ID: 1, Name: "SRBMiner Pearl", ScriptPath: `C:\Mining\SRBMiner\start-mining-pearl.bat`}
		values := r.URL.Query()
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.Form.Get("csrf") != "preview" {
				http.Error(w, "csrf", 403)
				return
			}
			values = r.Form
			q := url.Values{"id": {values.Get("id")}, "path": {values.Get("path")}, "process": {values.Get("process")}, "saved": {"1"}}
			http.Redirect(w, r, "/admin/mining/script?"+q.Encode(), http.StatusSeeOther)
			return
		}
		editorURL := "/admin/mining/script?id=1"
		if values.Get("path") != "" {
			miner.ID = 0
			miner.ScriptPath = values.Get("path")
			parts := strings.Split(miner.ScriptPath, `\`)
			miner.Name = parts[len(parts)-1]
			editorURL = "/admin/mining/script?" + url.Values{"path": {miner.ScriptPath}, "process": {values.Get("process")}}.Encode()
		}
		process := values.Get("process")
		if process == "" {
			process = "SRBMiner-MULTI.exe"
		}
		render("admin_mining_script", "Редактор BAT", map[string]any{
			"Miner": miner, "EditorURL": editorURL, "Processes": []map[string]any{{"Name": process, "Selected": true}},
			"Script":  mining.Script{Content: "@echo off\r\ncd /d %~dp0\r\nSRBMiner-MULTI.exe --algorithm pearl --pool example.invalid:3333\r\npause\r\n", SHA256: strings.Repeat("a", 64)},
			"CanSave": true, "Saved": r.URL.Query().Get("saved") == "1",
		})(w, r)
	})
	mux.HandleFunc("/admin/services/comfyui", page("comfyui"))
	mux.HandleFunc("/admin/services/openwebui", page("openwebui"))
	mux.HandleFunc("/admin/resources/overview", func(w http.ResponseWriter, r *http.Request) {
		v := view(r.URL.Query().Get("service"), r.URL.Query().Get("scenario"))
		var html bytes.Buffer
		if err := templates.ExecuteTemplate(&html, "admin_resource_snapshot", v); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		states := map[int64]map[string]any{}
		for id, state := range v["Profiles"].(map[int64]map[string]any) {
			var fragment bytes.Buffer
			if err := templates.ExecuteTemplate(&fragment, "admin_miner_state", state); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			states[id] = map[string]any{"html": fragment.String(), "running": state["Running"], "start": state["CanStart"], "stop": state["CanStop"], "update": state["CanUpdate"], "change": state["CanChange"], "restart": state["CanRestart"]}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		var gpu bytes.Buffer
		if v["Mining"] != nil {
			if err := templates.ExecuteTemplate(&gpu, "admin_resource_gpu", v); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"snapshot_html": html.String(), "gpu_html": gpu.String(), "profiles": states, "profile_version": v["ProfileVersion"]})
	})
}
