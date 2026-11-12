package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
	"ai-access-gateway/internal/store"
)

func registerUserProfilePreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, base domain.User, stats domain.UserStats, now time.Time) {
	type state struct {
		user            domain.User
		sessions        []domain.AccountSession
		tasks           []domain.Task
		activities      []domain.Activity
		policy          domain.GenerationAccessPolicy
		assistantGrants map[string]bool
	}
	states := map[int64]*state{}
	var mutex sync.Mutex
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id := int64(2)
		if r.URL.Path != "/preview/user" {
			var err error
			id, err = strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/admin/users/"), 10, 64)
			if err != nil || id <= 0 {
				http.NotFound(w, r)
				return
			}
		}
		q, err := gateway.ParseAdminUserProfileQuery(r.URL.Query())
		if err != nil {
			http.Error(w, "Проверьте фильтры профиля.", 400)
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		s := states[id]
		if s == nil {
			u := base
			u.ID = id
			if id == 1 {
				u.Role = "admin"
				u.Username = "admin"
				u.AccountExpiresAt = sql.NullTime{}
			}
			if id == 4 {
				u.Disabled = true
				u.Username = "disabled-user-with-a-very-long-account-name"
			}
			if id == 5 {
				u.AccountExpiresAt = sql.NullTime{Time: now.Add(-time.Hour), Valid: true}
			}
			s = &state{user: u, assistantGrants: map[string]bool{}}
			if id == 7 {
				s.assistantGrants["retired-model"] = true
				s.policy = domain.GenerationAccessPolicy{UserID: id, PresetIDs: []string{"saved-workflow"}, ModelIDs: []string{"saved-model"}, KreaLoraGroups: []string{"saved-krea-group"}, FluxLoraGroups: []string{"saved-flux-group"}}
			}
			states[id] = s
			count := 5
			if id >= 1000 {
				count = 125
			}
			if id == 6 {
				count = 0
			}
			for i := 0; i < count; i++ {
				seen := now.Add(-time.Duration(i+1) * time.Minute)
				ua := []string{"Chrome 139 · Windows 11", "Firefox · Linux", "Safari · iPhone", "Chrome Mobile · Android 15 с длинным названием устройства для проверки переноса", "Browser 100%_test"}[i%5]
				s.sessions = append(s.sessions, domain.AccountSession{ID: int64(i + 1), UserAgent: ua, IP: fmt.Sprintf("192.0.2.%d", i%250+1), Current: id == 1 && i == 0, CreatedAt: seen.Add(-8 * time.Hour), LastSeenAt: seen, ExpiresAt: now.Add(24 * time.Hour)})
				kind := []string{"generation", "lora_training", "lora_caption", "lora_caption_series"}[i%4]
				name := []string{"Портрет для каталога", "LoRA: предметная съёмка", "Описание референсного кадра", "Описания набора с длинным названием коллекции"}[i%4]
				status := []string{"completed", "queued", "running", "failed", "cancelled"}[i%5]
				s.tasks = append(s.tasks, domain.Task{Kind: kind, ID: fmt.Sprintf("profile-%d-job-%03d", id, i), Name: fmt.Sprintf("%s %03d", name, i), State: status, CreatedAt: seen, UpdatedAt: seen.Add(30 * time.Second), Message: "Состояние задачи сохранено", Stage: "Работа с моделью"})
				service, summary, path := "comfyui", "Запуск генерации", "/prompt"
				if i%2 == 0 {
					service, summary, path = "openwebui", "Сообщение нейросети", "/api/chat"
				}
				code := 200
				if i%5 == 0 {
					code = 500
				}
				s.activities = append(s.activities, domain.Activity{Service: service, ServiceLabel: map[string]string{"comfyui": "ComfyUI", "openwebui": "OpenWebUI"}[service], Summary: summary, Method: "POST", Path: path, Count: 1, Status: code, Duration: 120, Bytes: 2400, CreatedAt: seen})
			}
		}
		data := map[string]any{}
		redirect := func(tab, key, value string) {
			v := url.Values{"tab": {tab}}
			if tab == q.Tab {
				for _, k := range []string{"q", "sort", "result", "offset"} {
					if r.URL.Query().Get(k) != "" {
						v.Set(k, r.URL.Query().Get(k))
					}
				}
			}
			v.Set(key, value)
			http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?%s", id, v.Encode()), 302)
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if r.ParseForm() != nil || r.PostForm.Get("csrf") != "preview" {
				http.Error(w, "Неверный защитный токен", 403)
				return
			}
			action := r.PostForm.Get("action")
			if s.user.Role == "admin" && (action == "disable" || action == "enable" || action == "delete" || action == "update_access" || action == "review_access" || action == "update_generation_policy" || action == "update_assistant_model") {
				http.Error(w, "Права администратора нельзя изменить в этой форме.", 403)
				return
			}
			switch action {
			case "update_generation_resources":
				if s.user.Role != "admin" {
					http.Error(w, "Настройки доступны только для администратора.", 400)
					return
				}
				s.user.QueuePriority = r.PostForm.Get("queue_priority") == "on"
				s.user.PauseMiningForQuickGeneration = r.PostForm.Get("pause_mining_for_quick_generation") == "on"
				redirect("access", "access", "changed")
				return
			case "update_assistant_model":
				modelID, grant := r.PostForm.Get("assistant_model"), r.PostForm.Get("granted") == "1"
				if !domain.ValidPromptAssistantModelID(modelID) || (grant && modelID != "model-large" && modelID != "model-vision") {
					http.Error(w, "Модель отсутствует в каталоге.", 400)
					return
				}
				if grant {
					s.assistantGrants[modelID] = true
				} else {
					delete(s.assistantGrants, modelID)
				}
				redirect("access", "assistant_model", "changed")
				return
			case "review_access", "update_access":
				access, err := gateway.UserAccessSettingsFromForm(r.PostForm)
				q.Tab = "access"
				data["AccessForm"] = r.PostForm
				if err != nil {
					data["AccessError"] = err.Error()
				} else if action == "review_access" {
					data["AccessReview"], data["AccessReviewed"] = access, true
				} else {
					s.user.CanUseComfyUI, s.user.CanUseOpenWebUI = access.ComfyUI, access.OpenWebUI
					s.user.CanUseQuickGeneration, s.user.CanGenerateTextToImage, s.user.CanGenerateImageToImage, s.user.CanGenerateVideo = access.QuickGeneration, access.TextToImage, access.ImageToImage, access.Video
					s.user.CanTrainImageLora, s.user.CanUseAdvancedGenerationSettings = access.Training, access.AdvancedSettings
					s.user.PauseMiningForQuickGeneration, s.user.QueuePriority = access.PauseMining, access.QueuePriority
					s.user.GenerationDailyLimit, s.user.GenerationTotalLimit = access.ImageDailyLimit, access.ImageTotalLimit
					s.user.VideoGenerationDailyLimit, s.user.VideoGenerationTotalLimit = access.VideoDailyLimit, access.VideoTotalLimit
					s.user.MaxVideoGenerationQuality = access.MaxVideoQuality
					s.user.CanManageMining = r.PostForm.Get("can_manage_mining") == "on"
					redirect("access", "access", "changed")
					return
				}
			case "update_generation_policy":
				s.policy = domain.GenerationAccessPolicy{UserID: id, PresetIDs: r.PostForm["preset_id"], ModelIDs: r.PostForm["model_id"], KreaLoraGroups: r.PostForm["krea_lora_group"], FluxLoraGroups: r.PostForm["flux_lora_group"]}
				redirect("access", "policy", "changed")
				return
			case "revoke_session":
				sessionID, _ := strconv.ParseInt(r.PostForm.Get("session_id"), 10, 64)
				status := "session_unavailable"
				for i, item := range s.sessions {
					if item.ID == sessionID && !item.Current {
						s.sessions = append(s.sessions[:i], s.sessions[i+1:]...)
						status = "session_revoked"
						break
					}
				}
				redirect("sessions", "security", status)
				return
			case "revoke_sessions":
				s.sessions = nil
				redirect("sessions", "security", "sessions_revoked")
				return
			case "disable", "enable":
				s.user.Disabled = action == "disable"
				if s.user.Disabled {
					s.sessions = nil
				}
				redirect("sessions", "security", "status_changed")
				return
			case "unlock":
				s.user.FailedLoginCount = 0
				s.user.LockedUntil = sql.NullTime{}
				redirect("sessions", "security", "unlocked")
				return
			case "change_password":
				status := "changed"
				if len(r.PostForm.Get("new_password")) < 10 || r.PostForm.Get("new_password") != r.PostForm.Get("confirm_password") {
					status = "invalid"
				}
				redirect("sessions", "password", status)
				return
			case "delete":
				redirect("sessions", "delete", "confirmation_invalid")
				return
			default:
				http.Error(w, "Неверное действие", 400)
				return
			}
		} else if r.Method != http.MethodGet {
			http.Error(w, "Метод не поддерживается", 405)
			return
		}
		data["Profile"], data["ProfileTab"], data["Stats"], data["AccountLocked"] = s.user, q.Tab, stats, s.user.IsLocked(now)
		quota := store.QuickGenerationQuota{Image: store.QuickGenerationQuotaBucket{DailyUsed: 3, TotalUsed: s.user.GenerationTotalUsed}, Video: store.QuickGenerationQuotaBucket{DailyUsed: 1, TotalUsed: s.user.VideoGenerationTotalUsed}}
		data["EffectiveAccess"] = gateway.BuildAdminProfileAccess(s.user, quota, now)
		data["GenerationPolicy"] = s.policy
		grants := []string{}
		for modelID := range s.assistantGrants {
			grants = append(grants, modelID)
		}
		sort.Strings(grants)
		data["AssistantModels"] = gateway.BuildAdminAssistantModels([]domain.PromptAssistantModel{
			{ID: "model-large", Label: "Текстовая модель 31B"},
			{ID: "model-vision", Label: "Мультимодальная модель для подробного описания референсов"},
		}, grants)
		data["AssistantModelStatus"] = r.URL.Query().Get("assistant_model")
		data["MissingPresetIDs"], data["MissingModelIDs"] = s.policy.PresetIDs, s.policy.ModelIDs
		data["MissingKreaGroups"], data["MissingFluxGroups"] = s.policy.KreaLoraGroups, s.policy.FluxLoraGroups
		for key, name := range map[string]string{"access": "AccessStatus", "policy": "PolicyStatus", "password": "PasswordStatus", "security": "SecurityStatus", "delete": "DeleteStatus"} {
			data[name] = r.URL.Query().Get(key)
		}
		page := func(total int) store.UserProfilePage {
			f := q.Filter
			if total == 0 {
				f.Offset = 0
			} else if f.Offset >= total {
				f.Offset = (total - 1) / 50 * 50
			}
			return store.UserProfilePage{Filter: f, Total: int64(total), Now: now}
		}
		switch q.Tab {
		case "sessions":
			items := []domain.AccountSession{}
			for _, item := range s.sessions {
				if q.Filter.Search != "" && !strings.Contains(strings.ToLower(item.IP+" "+item.UserAgent), strings.ToLower(q.Filter.Search)) {
					continue
				}
				if q.Filter.Result == "current" && !item.Current || q.Filter.Result == "others" && item.Current {
					continue
				}
				items = append(items, item)
			}
			sort.Slice(items, func(i, j int) bool {
				if q.Filter.Sort == "oldest" {
					return items[i].ID > items[j].ID
				}
				return items[i].ID < items[j].ID
			})
			p := page(len(items))
			selected := items[p.Filter.Offset:min(p.Filter.Offset+50, len(items))]
			data["Sessions"], data["ProfilePagination"] = selected, gateway.BuildAdminProfilePagination(id, q.Tab, p, len(selected))
		case "activity":
			items := []domain.Activity{}
			for _, item := range s.activities {
				if q.Filter.Service != "" && q.Filter.Service != item.Service {
					continue
				}
				if q.Filter.Search != "" && !strings.Contains(strings.ToLower(item.Path), strings.ToLower(q.Filter.Search)) {
					continue
				}
				if q.Filter.Result == "error" && item.Status < 400 || q.Filter.Result == "success" && item.Status >= 400 {
					continue
				}
				items = append(items, item)
			}
			if q.Filter.Sort == "oldest" {
				sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
			}
			p := page(len(items))
			selected := items[p.Filter.Offset:min(p.Filter.Offset+50, len(items))]
			data["Activities"], data["ProfilePagination"] = selected, gateway.BuildAdminProfilePagination(id, q.Tab, p, len(selected))
		case "tasks":
			items := []domain.Task{}
			for _, item := range s.tasks {
				if q.Tasks.Kind != "" && q.Tasks.Kind != item.Kind || q.Tasks.State != "" && q.Tasks.State != item.State {
					continue
				}
				if q.Tasks.Search != "" && !strings.Contains(strings.ToLower(item.Name+" "+item.ID), strings.ToLower(q.Tasks.Search)) {
					continue
				}
				items = append(items, item)
			}
			offset := 0
			if q.Cursor != "" {
				prefix := fmt.Sprintf("preview-profile-%d-", id)
				if !strings.HasPrefix(q.Cursor, prefix) {
					http.Error(w, "Неверная страница", 400)
					return
				}
				offset, err = strconv.Atoi(strings.TrimPrefix(q.Cursor, prefix))
				if err != nil || offset < 0 {
					http.Error(w, "Неверная страница", 400)
					return
				}
			}
			offset = min(offset, len(items))
			end := min(offset+50, len(items))
			data["ProfileTasks"], data["TaskTotal"], data["TaskQuery"] = gateway.BuildAdminProfileTasks(items[offset:end]), int64(len(items)), q
			data["TaskFirstURL"] = gateway.AdminProfileTaskURL(id, q, "")
			if end < len(items) {
				data["TaskNextURL"] = gateway.AdminProfileTaskURL(id, q, fmt.Sprintf("preview-profile-%d-%d", id, end))
			}
		}
		render("admin_user_detail", "Пользователь "+s.user.Username, data)(w, r)
	}
	mux.HandleFunc("/preview/user", handler)
	mux.HandleFunc("/admin/users/", handler)
}
