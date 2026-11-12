package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

type AdminUserProfileQuery struct {
	Tab    string
	Filter store.UserProfileFilter
	Tasks  domain.TaskFilter
	Cursor string
}

func ParseAdminUserProfileQuery(v url.Values) (AdminUserProfileQuery, error) {
	q := AdminUserProfileQuery{Tab: v.Get("tab")}
	for _, key := range []string{"tab", "q", "service", "result", "sort", "offset", "kind", "state", "cursor"} {
		if len(v[key]) > 1 {
			return q, store.ErrUserProfileFilter
		}
	}
	if q.Tab == "" {
		q.Tab = "access"
		if v.Get("password") != "" || v.Get("security") != "" || v.Get("delete") != "" {
			q.Tab = "sessions"
		}
	}
	switch q.Tab {
	case "access":
	case "tasks":
		f, _, cursor, err := parseTaskQuery(url.Values{"search": {v.Get("q")}, "kind": {v.Get("kind")}, "state": {v.Get("state")}, "cursor": {v.Get("cursor")}})
		if err != nil {
			return q, err
		}
		q.Tasks, q.Cursor = f, cursor
	case "activity", "sessions":
		q.Filter = store.UserProfileFilter{Search: v.Get("q"), Service: v.Get("service"), Result: v.Get("result"), Sort: v.Get("sort")}
		if raw := v.Get("offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || strconv.Itoa(n) != raw {
				return q, store.ErrUserProfileFilter
			}
			q.Filter.Offset = n
		}
		var err error
		q.Filter, err = q.Filter.Normalize(q.Tab == "sessions")
		if err != nil {
			return q, err
		}
	default:
		return q, store.ErrUserProfileFilter
	}
	return q, nil
}

type adminProfileTab struct {
	Key, Label, URL string
	Active          bool
}

func adminProfileTabs(id int64, selected string) []adminProfileTab {
	tabs := []adminProfileTab{}
	for _, t := range []struct{ key, label string }{{"access", "Доступ и лимиты"}, {"tasks", "Задачи"}, {"activity", "Активность"}, {"sessions", "Сессии"}} {
		tabs = append(tabs, adminProfileTab{Key: t.key, Label: t.label, URL: fmt.Sprintf("/admin/users/%d?tab=%s", id, t.key), Active: selected == t.key})
	}
	return tabs
}

func userAccessForm(user domain.User, submitted any) adminAccessForm {
	if values, ok := submitted.(url.Values); ok && values != nil {
		f := inviteForm(values)
		if f.Value("max_video_generation_quality") == "" {
			f.values.Set("max_video_generation_quality", "720")
		}
		return f
	}
	v := url.Values{}
	for name, value := range map[string]bool{
		"can_use_comfyui": user.CanUseComfyUI, "can_use_openwebui": user.CanUseOpenWebUI,
		"can_use_quick_generation": user.CanUseQuickGeneration, "can_generate_text_to_image": user.CanGenerateTextToImage,
		"can_use_harness":             user.CanUseHarness,
		"can_generate_image_to_image": user.CanGenerateImageToImage, "can_generate_video": user.CanGenerateVideo,
		"can_train_image_lora": user.CanTrainImageLora, "can_use_advanced_generation_settings": user.CanUseAdvancedGenerationSettings,
		"can_manage_mining": user.CanManageMining, "queue_priority": user.QueuePriority, "pause_mining_for_quick_generation": user.PauseMiningForQuickGeneration,
	} {
		if value {
			v.Set(name, "on")
		}
	}
	for name, value := range map[string]int64{
		"image_generation_daily_limit": int64(user.GenerationDailyLimit), "image_generation_total_limit": user.GenerationTotalLimit,
		"video_generation_daily_limit": int64(user.VideoGenerationDailyLimit), "video_generation_total_limit": user.VideoGenerationTotalLimit,
	} {
		v.Set(name, strconv.FormatInt(value, 10))
	}
	quality := user.MaxVideoGenerationQuality
	if quality == 0 {
		quality = 1440
	}
	v.Set("max_video_generation_quality", strconv.Itoa(quality))
	return inviteForm(v)
}

// Shares the production access parser with isolated UI fixtures.
func UserAccessSettingsFromForm(values url.Values) (domain.AccessSettings, error) {
	return parseAccessSettings(&http.Request{Form: values}, false)
}

type adminProfileAccess struct {
	domain.EffectiveAccess
	Label  string
	Quotas []accessQuotaView
}

func BuildAdminProfileAccess(user domain.User, quota store.QuickGenerationQuota, now time.Time) adminProfileAccess {
	effective := user.EffectiveAccess(now)
	return adminProfileAccess{EffectiveAccess: effective, Label: accessListLabel(effective.Settings),
		Quotas: accessListQuotas(effective.Settings, quota.Image.DailyUsed, quota.Video.DailyUsed, quota.Image.TotalUsed, quota.Video.TotalUsed, true)}
}

type adminProfilePagination struct {
	store.UserProfilePage
	First, Last                                int64
	PreviousURL, NextURL, ResetURL, CurrentURL string
}

func profileListURL(id int64, tab string, f store.UserProfileFilter, offset int) string {
	v := url.Values{"tab": {tab}}
	for key, value := range map[string]string{"q": f.Search, "service": f.Service, "result": f.Result, "sort": f.Sort} {
		if value != "" {
			v.Set(key, value)
		}
	}
	if offset > 0 {
		v.Set("offset", strconv.Itoa(offset))
	}
	return fmt.Sprintf("/admin/users/%d?%s#profile-results", id, v.Encode())
}

func BuildAdminProfilePagination(id int64, tab string, p store.UserProfilePage, count int) adminProfilePagination {
	v := adminProfilePagination{UserProfilePage: p, ResetURL: profileListURL(id, tab, store.UserProfileFilter{}, 0), CurrentURL: profileListURL(id, tab, p.Filter, p.Filter.Offset)}
	if p.Total > 0 {
		v.First, v.Last = int64(p.Filter.Offset)+1, int64(p.Filter.Offset+count)
	}
	if p.Filter.Offset > 0 {
		v.PreviousURL = profileListURL(id, tab, p.Filter, max(0, p.Filter.Offset-store.UserProfilePageSize))
	}
	if v.Last < p.Total {
		v.NextURL = profileListURL(id, tab, p.Filter, p.Filter.Offset+store.UserProfilePageSize)
	}
	return v
}

type adminProfileTask struct {
	domain.Task
	KindLabel, StateLabel, Tone, InspectURL string
}

func adminProfileTaskState(state string) string {
	if state == "partial" {
		return "Частично готово"
	}
	if state == "running" {
		return "В работе"
	}
	if domain.GenerationJobState(state).Valid() {
		return generationJobStateLabel(domain.GenerationJobState(state))
	}
	if domain.LoraTrainingState(state).Valid() {
		return loraTrainingStateLabel(domain.LoraTrainingState(state))
	}
	return "Обновление состояния"
}

func adminProfileTaskStates(kind string) []struct{ Value, Label string } {
	items := []struct{ Value, Label string }{}
	for _, state := range []string{"draft", "preparing", "uploading", "waiting_for_resources", "queued", "caching", "running", "postprocessing", "archiving", "installing", "completed", "failed", "cancelled", "expired"} {
		items = append(items, struct{ Value, Label string }{state, adminProfileTaskState(state)})
	}
	if kind == "lora_caption_series" {
		items = append(items, struct{ Value, Label string }{"partial", "Частично готово"})
	}
	return items
}

func BuildAdminProfileTasks(items []domain.Task) []adminProfileTask {
	rows := []adminProfileTask{}
	for _, t := range items {
		kind := map[string]string{"generation": "Генерация", "lora_training": "Обучение LoRA", "lora_caption": "Описание кадра", "lora_caption_series": "Описания набора"}[t.Kind]
		state := adminProfileTaskState(t.State)
		tone := "info"
		if t.State == "completed" {
			tone = "ok"
		} else if t.State == "failed" {
			tone = "danger"
		} else if t.State == "cancelled" || t.State == "partial" || t.State == "expired" {
			tone = "warning"
		}
		// Never reuse owner-only task URLs or thumbnails in another user's profile.
		t.Preview = nil
		row := adminProfileTask{Task: t, KindLabel: kind, StateLabel: state, Tone: tone}
		if t.Kind == "generation" && validGenerationRequestID(t.ID) {
			row.InspectURL = "/admin/jobs/" + url.PathEscape(t.ID)
		}
		rows = append(rows, row)
	}
	return rows
}

func AdminProfileTaskURL(id int64, q AdminUserProfileQuery, cursor string) string {
	v := url.Values{"tab": {"tasks"}}
	for key, value := range map[string]string{"q": q.Tasks.Search, "kind": q.Tasks.Kind, "state": q.Tasks.State, "cursor": cursor} {
		if value != "" {
			v.Set(key, value)
		}
	}
	return fmt.Sprintf("/admin/users/%d?%s#profile-results", id, v.Encode())
}

func (a *App) renderAdminUserProfile(w http.ResponseWriter, r *http.Request, id int64, q AdminUserProfileQuery, data map[string]any) {
	user, err := a.store.UserByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	data["Title"], data["Profile"], data["ProfileTab"] = "Пользователь "+user.Username, user, q.Tab
	data["PasswordStatus"], data["SecurityStatus"], data["DeleteStatus"] = r.URL.Query().Get("password"), r.URL.Query().Get("security"), r.URL.Query().Get("delete")
	data["AccessStatus"], data["PolicyStatus"] = r.URL.Query().Get("access"), r.URL.Query().Get("policy")
	data["AccountLocked"] = user.IsLocked(time.Now())
	data["EffectiveAccess"] = user.EffectiveAccess(time.Now())
	switch q.Tab {
	case "access":
		quota, err := a.store.QuickGenerationQuota(r.Context(), id)
		if err != nil {
			http.Error(w, "Не удалось загрузить расход квот. Обновите страницу.", http.StatusInternalServerError)
			return
		}
		data["EffectiveAccess"] = BuildAdminProfileAccess(user, quota, time.Now())
		grants, err := a.store.PromptAssistantModelGrants(r.Context(), id)
		if err != nil {
			http.Error(w, "Не удалось загрузить разрешения моделей ассистента. Обновите страницу.", http.StatusInternalServerError)
			return
		}
		data["AssistantModels"] = BuildAdminAssistantModels(a.cfg.PromptAssistantModels, grants)
		data["AssistantModelStatus"] = r.URL.Query().Get("assistant_model")
		if data["AccessReview"] == nil {
			data["AccessReview"] = user.AccessSettings().Normalize()
		}
		policy, err := a.store.GenerationAccessPolicy(r.Context(), id)
		if err != nil {
			http.Error(w, "Не удалось загрузить правила генерации. Обновите страницу.", http.StatusInternalServerError)
			return
		}
		catalog := a.comfyGenerationModels(r.Context())
		addAdminProfilePolicy(data, catalog, policy)
	case "tasks":
		page, err := a.store.ListTasks(r.Context(), domain.TaskScope{UserID: id, Generation: true, Training: true}, q.Tasks, store.UserProfilePageSize, q.Cursor)
		if errors.Is(err, store.ErrTaskQuery) {
			http.Error(w, "Проверьте фильтры задач или вернитесь к первой странице.", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "Не удалось загрузить задачи. Обновите страницу.", http.StatusInternalServerError)
			return
		}
		data["ProfileTasks"], data["TaskTotal"], data["TaskQuery"] = BuildAdminProfileTasks(page.Tasks), page.Total, q
		data["TaskFirstURL"] = AdminProfileTaskURL(id, q, "")
		if page.NextCursor != "" {
			data["TaskNextURL"] = AdminProfileTaskURL(id, q, page.NextCursor)
		}
	case "activity":
		stats, err := a.store.UserStats(r.Context(), id, 7)
		if err != nil {
			http.Error(w, "Не удалось загрузить статистику. Обновите страницу.", http.StatusInternalServerError)
			return
		}
		page, err := a.store.ListUserActivityPage(r.Context(), id, q.Filter)
		if err != nil {
			http.Error(w, "Не удалось загрузить активность. Обновите страницу.", http.StatusInternalServerError)
			return
		}
		data["Stats"], data["Activities"] = stats, adminProfileActivities(page.Items)
		data["ProfilePagination"] = BuildAdminProfilePagination(id, q.Tab, page.UserProfilePage, len(page.Items))
	case "sessions":
		page, err := a.store.ListUserSessionsPage(r.Context(), id, adminProfileSessionHash(r), a.cfg.SessionIdleTimeout, q.Filter)
		if err != nil {
			http.Error(w, "Не удалось загрузить сессии. Обновите страницу.", http.StatusInternalServerError)
			return
		}
		data["Sessions"] = page.Items
		data["ProfilePagination"] = BuildAdminProfilePagination(id, q.Tab, page.UserProfilePage, len(page.Items))
	}
	a.render(w, r, "admin_user_detail", data)
}

func adminProfileActivities(items []domain.Activity) []domain.Activity {
	// Pagination counts requests, not merged actions; keep every matching row.
	result := make([]domain.Activity, len(items))
	for i, item := range items {
		item.ServiceLabel, item.Summary = describeActivity(item)
		if isActivityNoise(item) {
			item.Summary = "Фоновый запрос"
		}
		item.Count = 1
		item.Path = activityPath(item.Path)
		result[i] = item
	}
	return result
}

func adminProfileSessionHash(r *http.Request) string {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		return security.HashToken(cookie.Value)
	}
	return ""
}

func missingProfilePolicyValues(selected []string, available []string) []string {
	known := map[string]bool{}
	for _, value := range available {
		known[value] = true
	}
	missing := []string{}
	for _, value := range selected {
		if !known[value] {
			missing = append(missing, value)
			known[value] = true
		}
	}
	return missing
}

func addAdminProfilePolicy(data map[string]any, catalog generationModelCatalog, policy domain.GenerationAccessPolicy) {
	presets, models := buildGenerationPresets(catalog), quickGenerationModels(catalog)
	data["GenerationPolicy"], data["GenerationPresets"], data["QuickModels"] = policy, presets, models
	data["LoraGroups"], data["FluxLoraGroups"] = catalog.LoraGroups, catalog.FluxLoraGroups
	var presetIDs, modelIDs, kreaGroups, fluxGroups []string
	for _, item := range presets {
		presetIDs = append(presetIDs, item.ID)
	}
	for _, item := range models {
		modelIDs = append(modelIDs, item.ID)
	}
	for _, item := range catalog.LoraGroups {
		kreaGroups = append(kreaGroups, item.Name)
	}
	for _, item := range catalog.FluxLoraGroups {
		fluxGroups = append(fluxGroups, item.Name)
	}
	data["MissingPresetIDs"] = missingProfilePolicyValues(policy.PresetIDs, presetIDs)
	data["MissingModelIDs"] = missingProfilePolicyValues(policy.ModelIDs, modelIDs)
	data["MissingKreaGroups"] = missingProfilePolicyValues(policy.KreaLoraGroups, kreaGroups)
	data["MissingFluxGroups"] = missingProfilePolicyValues(policy.FluxLoraGroups, fluxGroups)
}

func redirectAdminProfile(w http.ResponseWriter, r *http.Request, id int64, tab, key, value string) {
	v := url.Values{"tab": {tab}}
	if tab == "sessions" {
		if q, err := ParseAdminUserProfileQuery(r.URL.Query()); err == nil && q.Tab == tab {
			p, _ := url.Parse(profileListURL(id, tab, q.Filter, q.Filter.Offset))
			v = p.Query()
		}
	}
	if key != "" {
		v.Set(key, value)
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?%s", id, v.Encode()), http.StatusFound)
}
