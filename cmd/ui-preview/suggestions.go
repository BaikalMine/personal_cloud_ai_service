package main

import (
	"net/http"
	"strconv"
	"strings"
)

func registerSuggestionsPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, userRows, adminRows []map[string]any) {
	page := func(admin bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				if admin {
					http.Redirect(w, r, "/preview/admin-suggestions?message=changed", http.StatusSeeOther)
				} else {
					_ = r.ParseMultipartForm(6 << 20)
					if r.MultipartForm != nil {
						defer r.MultipartForm.RemoveAll()
					}
					target := "/preview/suggestions?message=changed"
					if r.FormValue("action") == "save" {
						target += "&edit=12"
					}
					http.Redirect(w, r, target, http.StatusSeeOther)
				}
				return
			}
			source := userRows
			name := "suggestions"
			if admin {
				source, name = adminRows, "admin_suggestions"
			}
			rows := make([]map[string]any, 0, len(source))
			for _, original := range source {
				row := make(map[string]any, len(original))
				for k, v := range original {
					row[k] = v
				}
				rows = append(rows, row)
			}
			scenario := r.URL.Query().Get("scenario")
			if scenario == "long" {
				for _, row := range rows {
					row["Title"] = strings.Repeat("ОченьДлинноеНазвание", 6)
					row["Description"] = "<script>never()</script> " + strings.Repeat("ДлинноеОписаниеБезПробелов", 160)
					row["ReviewComment"] = strings.Repeat("Комментарий", 80)
					row["JSONName"] = strings.Repeat("workflow-name-", 15) + ".json"
					if admin {
						row["Links"] = []map[string]any{{"SourceName": "Ссылка 1", "Safe": false, "URL": "https://example.com/" + strings.Repeat("long-url", 70)}}
					}
				}
			}
			if scenario == "unsafe" && admin {
				rows[0]["CanAccept"], rows[0]["CanDownloadJSON"] = false, false
				rows[0]["ScanStatusLabel"] = "Найдены риски"
				rows[0]["Links"] = []map[string]any{{"SourceName": "Ссылка 1", "Safe": false, "URL": "https://example.com/flagged"}}
			}
			if scenario == "empty" {
				rows = []map[string]any{}
			}
			data := map[string]any{"Suggestions": rows, "PublicIntakeEnabled": false, "VirusTotalConfigured": scenario != "unavailable", "CommentID": int64(0)}
			if scenario == "error" {
				if admin {
					data["SuggestionLoadError"] = true
				} else {
					data["Error"] = "Не удалось отправить предложение. Проверьте описание."
				}
			}
			if scenario == "uncertain" {
				data["SuggestionUncertain"] = true
				data["Error"] = "Результат сохранения не подтверждён. Обновите очередь перед повторным действием."
			}
			if r.URL.Query().Get("message") != "" {
				data["Message"] = "Изменения сохранены."
			}
			form := map[string]any{"ID": int64(0), "Kind": "workflow", "Title": "", "Description": "", "Links": "", "JSONName": "", "JSONSize": int64(0)}
			if id, _ := strconv.ParseInt(r.URL.Query().Get("edit"), 10, 64); id == 12 {
				form["ID"], form["Title"], form["Description"] = id, userRows[0]["Title"], userRows[0]["Description"]
				form["JSONName"], form["JSONSize"] = "portrait-workflow.json", int64(128)
			}
			data["Form"] = form
			render(name, "Предложения", data)(w, r)
		}
	}
	mux.HandleFunc("/preview/suggestions", page(false))
	mux.HandleFunc("/suggestions", page(false))
	mux.HandleFunc("/suggestions/", page(false))
	mux.HandleFunc("/preview/admin-suggestions", page(true))
	mux.HandleFunc("/admin/suggestions", page(true))
	mux.HandleFunc("/admin/suggestions/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", `attachment; filename="portrait-motion.json"`)
			_, _ = w.Write([]byte(`{"preview":true}`))
			return
		}
		page(true)(w, r)
	})
}
