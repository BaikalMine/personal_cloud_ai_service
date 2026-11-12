package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
	"ai-access-gateway/internal/store"
)

func registerAdminTrainingPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, templates *gateway.Templates, now time.Time, profiles []map[string]any) {
	view := func(r *http.Request) map[string]any {
		q := r.URL.Query()
		count := 6
		if q.Get("scenario") == "pages" {
			count = 31
		}
		if q.Get("scenario") == "empty" {
			count = 0
		}
		jobs := make([]map[string]any, 0, count)
		for i := 0; i < count; i++ {
			state := domain.LoraTrainingCompleted
			label := "Готово"
			name := fmt.Sprintf("Предметный свет %02d", i+1)
			switch i {
			case 0:
				state = domain.LoraTrainingRunning
				label = "Обучение"
				name = "Редакционный портрет"
			case 1:
				state = domain.LoraTrainingQueued
				label = "В очереди"
				name = "Керамика и фактуры"
			case 2:
				state = domain.LoraTrainingFailed
				label = "Ошибка"
				name = "Мягкий дневной свет"
			case 4:
				state = domain.LoraTrainingCancelled
				label = "Отменено"
				name = "Пробный профиль"
			}
			family, familyLabel, profile := "krea2", "Krea2", "Krea 2 Raw Official"
			if i%2 == 1 {
				family, familyLabel, profile = "flux2-klein", "Flux.2 Klein", "Flux.2 Klein 9B Base"
			}
			job := map[string]any{"PublicID": fmt.Sprintf("admin-preview-training-%02d", i), "UserID": int64(i + 1), "UsernameSnapshot": []string{"admin", "anna", "studio-user"}[i%3], "Name": name,
				"Family": family, "FamilyLabel": familyLabel, "ProfileName": profile, "ProfileID": "preview-profile", "BaseModel": "models/krea2_raw_bf16.safetensors",
				"State": state, "StateLabel": label, "Stage": label, "Progress": 61, "ShowProgress": i == 0, "CanCancel": !state.Terminal(), "CanDelete": state.Terminal(), "CanDownload": state == domain.LoraTrainingCompleted,
				"SampleCount": 24, "DatasetBytes": int64(42 * 1024 * 1024), "Resolution": 768, "NetworkDim": 32, "MaxTrainSteps": 1600, "PresetLabel": "Основной", "TriggerWord": "editorial_subject", "CreatedAt": now.Add(-time.Duration(i+1) * time.Hour), "UpdatedAt": now.Add(-4 * time.Second)}
			if i == 0 {
				job["Stage"] = "Шаг 976 из 1600"
			}
			if i == 2 {
				job["ErrorMessage"] = "Агент остановил обучение: недостаточно свободной видеопамяти. Повторите запуск после завершения других задач."
			}
			if i == 4 {
				job["UserID"] = nil
			}
			if state == domain.LoraTrainingCompleted {
				job["ArtifactName"] = fmt.Sprintf("subject_light_%02d.safetensors", i)
				job["ArtifactBytes"] = int64(156 * 1024 * 1024)
			}
			if q.Get("scenario") == "long" && i == 2 {
				job["Name"] = strings.Repeat("ДлинноеНазвание", 10)
				job["BaseModel"] = strings.Repeat("model_component_", 30) + ".safetensors"
				job["ErrorMessage"] = strings.Repeat("long_diagnostic_without_spaces_", 60)
			}
			jobs = append(jobs, job)
		}
		f := store.AdminLoraTrainingFilter{Search: q.Get("q"), State: q.Get("state"), Family: q.Get("family"), Sort: q.Get("sort")}
		f.Offset, _ = strconv.Atoi(q.Get("offset"))
		f, _ = f.Normalize()
		filtered := jobs[:0]
		for _, job := range jobs {
			state := job["State"].(domain.LoraTrainingState)
			if f.State != "" && string(state) != f.State && !(f.State == "active" && !state.Terminal()) || f.Family != "" && job["Family"] != f.Family {
				continue
			}
			if !strings.Contains(strings.ToLower(fmt.Sprint(job)), strings.ToLower(f.Search)) {
				continue
			}
			filtered = append(filtered, job)
		}
		jobs = filtered
		sort.SliceStable(jobs, func(i, j int) bool {
			if f.Sort == "oldest" {
				return jobs[i]["CreatedAt"].(time.Time).Before(jobs[j]["CreatedAt"].(time.Time))
			}
			if f.Sort == "artifact" {
				a, _ := jobs[i]["ArtifactBytes"].(int64)
				b, _ := jobs[j]["ArtifactBytes"].(int64)
				if a != b {
					return a > b
				}
			}
			if f.Sort == "active" {
				rank := func(job map[string]any) int {
					s := job["State"].(domain.LoraTrainingState)
					if !s.Terminal() {
						return 0
					}
					if s == domain.LoraTrainingFailed {
						return 1
					}
					return 2
				}
				if rank(jobs[i]) != rank(jobs[j]) {
					return rank(jobs[i]) < rank(jobs[j])
				}
			}
			return jobs[i]["CreatedAt"].(time.Time).After(jobs[j]["CreatedAt"].(time.Time))
		})
		active, complete, failed, artifact := 0, 0, 0, int64(0)
		for _, job := range jobs {
			s := job["State"].(domain.LoraTrainingState)
			if !s.Terminal() {
				active++
			}
			if s == domain.LoraTrainingCompleted {
				complete++
			}
			if s == domain.LoraTrainingFailed {
				failed++
			}
			n, _ := job["ArtifactBytes"].(int64)
			artifact += n
		}
		total := len(jobs)
		if total == 0 {
			f.Offset = 0
		} else if f.Offset >= total {
			f.Offset = (total - 1) / 25 * 25
		}
		end := min(total, f.Offset+25)
		start := 0
		if total > 0 {
			start = f.Offset + 1
		}
		query := func(offset int) string {
			values := url.Values{}
			for _, key := range []string{"q", "state", "family", "sort", "scenario"} {
				if q.Get(key) != "" {
					values.Set(key, q.Get(key))
				}
			}
			if offset > 0 {
				values.Set("offset", strconv.Itoa(offset))
			}
			if len(values) == 0 {
				return ""
			}
			return "?" + values.Encode()
		}
		v := map[string]any{"Filter": f, "Filtered": f.Search != "" || f.State != "" || f.Family != "", "Total": total, "RangeStart": start, "RangeEnd": end, "Active": active, "Completed": complete, "Failed": failed, "ArtifactBytes": artifact, "Jobs": jobs[f.Offset:end], "Now": now, "Profiles": profiles, "ReadyProfiles": 2, "RefreshURL": "/admin/lora-training" + query(f.Offset), "ReturnQuery": query(f.Offset)}
		if f.Offset > 0 {
			v["PreviousURL"] = "/admin/lora-training" + query(max(0, f.Offset-25))
		}
		if end < total {
			v["NextURL"] = "/admin/lora-training" + query(f.Offset+25)
		}
		if q.Get("scenario") == "agent-offline" {
			v["Profiles"] = []map[string]any{}
			v["ReadyProfiles"] = 0
			v["AgentMessage"] = "Локальный агент обучения не отвечает. Повторяем проверку автоматически."
		}
		return map[string]any{"View": v, "CSRF": "preview", "PublicBaseURL": "", "Cancelled": q.Get("cancelled") == "1", "Deleted": q.Get("deleted") == "1"}
	}
	page := func(w http.ResponseWriter, r *http.Request) {
		render("admin_lora_training", "Обучение LoRA", view(r))(w, r)
	}
	mux.HandleFunc("/preview/admin-lora-training", page)
	mux.HandleFunc("/admin/lora-training", page)
	mux.HandleFunc("/admin/lora-training/overview", func(w http.ResponseWriter, r *http.Request) {
		var fragment bytes.Buffer
		if err := templates.ExecuteTemplate(&fragment, "admin_training_snapshot", view(r)); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"snapshot_html": fragment.String()})
	})
	mux.HandleFunc("/admin/lora-training/gpu", func(w http.ResponseWriter, r *http.Request) {
		tasks := []map[string]any{}
		timestamp := now.Format(time.RFC3339Nano)
		for _, value := range r.URL.Query()["task"] {
			kind, id, _ := strings.Cut(value, ":")
			var gpu any
			if strings.HasSuffix(id, "-00") {
				gpu = map[string]any{"code": "gpu_running", "snapshot_at": timestamp}
			}
			if strings.HasSuffix(id, "-01") {
				gpu = map[string]any{"code": "executor_unconfirmed", "position": 2, "snapshot_at": timestamp}
			}
			tasks = append(tasks, map[string]any{"kind": kind, "id": id, "available": true, "gpu": gpu})
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": tasks, "snapshot_at": timestamp})
	})
	mux.HandleFunc("/admin/lora-training/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") && r.Method == "GET" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="preview-artifact.txt"`)
			_, _ = w.Write([]byte("UI preview artifact fixture"))
			return
		}
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		if r.FormValue("csrf") != "preview" {
			http.Error(w, "csrf", 403)
			return
		}
		q := r.URL.Query()
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			q.Set("cancelled", "1")
		} else if strings.HasSuffix(r.URL.Path, "/delete") {
			q.Set("deleted", "1")
		} else {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/admin/lora-training?"+q.Encode(), http.StatusSeeOther)
	})
}
