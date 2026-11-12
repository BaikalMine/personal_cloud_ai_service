package main

import (
	"net/http"
	"strings"
	"time"
)

func registerUpdatesPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, now time.Time) {
	page := func(w http.ResponseWriter, r *http.Request) {
		scenario := r.URL.Query().Get("scenario")
		selected, action := []string{}, ""
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.FormValue("csrf") != "preview" {
				http.Error(w, "csrf", http.StatusForbidden)
				return
			}
			action, selected = r.FormValue("action"), r.Form["components"]
			if target, direct := strings.CutPrefix(action, "install:"); direct {
				action, selected = "install", []string{target}
			}
			if action == "install" && r.FormValue("confirm_install") != "1" {
				http.Error(w, "confirmation required", http.StatusBadRequest)
				return
			}
		}
		rows := []map[string]any{}
		for i, id := range []string{"gateway", "comfyui", "openwebui"} {
			if scenario == "empty" {
				break
			}
			row := map[string]any{"Name": id, "DisplayName": []string{"Gateway", "ComfyUI", "OpenWebUI"}[i], "Configured": true, "CurrentVersion": []string{"16278624f83b", "9a9fdb10ed14", "v0.11.0"}[i], "LatestVersion": []string{"16278624f83b", "8d123ab78fc9", "v0.11.1"}[i], "CheckedAt": now.Add(-4 * time.Second), "Selected": false, "CanCheck": true, "InstallAllowed": i == 2, "Label": []string{"Версии совпадают", "Требует внимания", "Есть новая версия"}[i], "Tone": []string{"success", "warning", "info"}[i], "Icon": []string{"boxes", "workflow", "messages-square"}[i], "Hint": "", "Message": ""}
			for _, name := range selected {
				if name == id {
					row["Selected"] = true
				}
			}
			if i == 1 {
				row["Message"] = "В ComfyUI есть активная очередь: обновление отложено."
				row["Hint"] = "В ComfyUI есть активные задачи. Дождитесь завершения очереди."
			}
			if i == 2 {
				row["Hint"] = "Установка повторно проверит компонент и перезапустит его при обновлении."
			}
			if scenario == "long" {
				row["CurrentVersion"], row["LatestVersion"] = strings.Repeat("revision0123456789", 8), strings.Repeat("release9876543210", 8)
				if i == 0 {
					row["LatestVersion"] = row["CurrentVersion"]
				}
				if i == 1 {
					row["DisplayName"] = strings.Repeat("ДлинноеНазваниеКомпонента", 4)
					row["Message"] = strings.Repeat("Диагностика: локальные изменения требуют проверки. ", 12)
					row["Hint"] = "Есть локальные изменения исходников. Проверьте их перед обновлением."
				}
			}
			if scenario == "stale" {
				row["CheckedAt"], row["InstallAllowed"], row["Label"], row["Tone"] = now.Add(-time.Hour), false, "Требуется проверка", "warning"
			}
			if scenario == "offline" || scenario == "unconfirmed" {
				row["CanCheck"], row["InstallAllowed"], row["Label"], row["Tone"] = false, false, "Не подтверждено", "warning"
			}
			if scenario == "unknown" {
				row["CheckedAt"], row["CurrentVersion"], row["LatestVersion"], row["Label"], row["Tone"], row["InstallAllowed"] = time.Time{}, "", "", "Требуется проверка", "warning", false
			}
			rows = append(rows, row)
		}
		result := map[string]any{"Label": "", "Tone": "info", "Message": "", "Targets": []string{}}
		if action != "" {
			result["Label"] = "Проверка завершена"
			if action == "install" {
				result["Label"] = "Обработка команды завершена"
			}
			for _, id := range selected {
				name := map[string]string{"gateway": "Gateway", "comfyui": "ComfyUI", "openwebui": "OpenWebUI"}[id]
				result["Targets"] = append(result["Targets"].([]string), name)
			}
		}
		if scenario == "unconfirmed" {
			result["Label"], result["Tone"], result["Targets"] = "Результат установки не подтвержден", "warning", []string{"OpenWebUI"}
		}
		available := scenario != "offline" && scenario != "unconfirmed"
		agentLabel := "Ответ получен"
		if !available {
			agentLabel = "Нет подтвержденного ответа"
		}
		counts := map[string]int{"Available": 0, "Current": 0, "Blocked": 0}
		canInstall := false
		for _, row := range rows {
			if row["InstallAllowed"].(bool) {
				counts["Available"]++
				canInstall = true
			} else if row["Tone"] == "success" {
				counts["Current"]++
			} else {
				counts["Blocked"]++
			}
		}
		view := map[string]any{"At": now, "Available": available, "AgentLabel": agentLabel, "Components": rows, "Overview": counts, "CanCheck": available && len(rows) > 0, "CanInstall": canInstall, "Result": result}
		render("admin_updates", "Обновления", map[string]any{"UpdateWorkspace": view, "Validation": "", "RuntimeBuild": map[string]any{"Version": now.Format("2006.01.02") + "-preview", "Revision": strings.Repeat("7a", 20), "BuiltAt": now.Add(-30 * time.Minute).Format(time.RFC3339)}})(w, r)
	}
	mux.HandleFunc("/preview/updates", page)
	mux.HandleFunc("/admin/updates", page)
}
