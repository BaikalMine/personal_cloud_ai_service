package gateway

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type accountTab struct {
	Key, Label, URL string
	Active          bool
}

func AccountTabs(selected string) []accountTab {
	tabs := []accountTab{
		{Key: "profile", Label: "Профиль", URL: "/account/profile"},
		{Key: "notifications", Label: "Уведомления", URL: "/account/profile?tab=notifications"},
		{Key: "access", Label: "Доступ и лимиты", URL: "/account/profile?tab=access"},
		{Key: "password", Label: "Пароль", URL: "/account/password"},
		{Key: "sessions", Label: "Сессии", URL: "/account/sessions"},
	}
	for i := range tabs {
		tabs[i].Active = tabs[i].Key == selected
	}
	return tabs
}

func accountProfileTab(r *http.Request) (string, bool) {
	v := r.URL.Query()
	if len(v["tab"]) > 1 {
		return "profile", false
	}
	switch v.Get("tab") {
	case "", "profile":
		return "profile", true
	case "notifications":
		return "notifications", true
	case "access":
		return "access", true
	default:
		return "profile", false
	}
}

func (a *App) renderAccountPage(w http.ResponseWriter, r *http.Request, status int, name, tab string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["AccountTab"], data["AccountTabs"] = tab, AccountTabs(tab)
	if _, provided := data["AccountRefreshURL"]; !provided {
		data["AccountRefreshURL"] = "/account/profile"
		for _, item := range AccountTabs(tab) {
			if item.Active {
				data["AccountRefreshURL"] = item.URL
			}
		}
		if tab == "sessions" {
			f, err := ParseAccountSessionFilter(r.URL.Query())
			if err == nil {
				data["AccountRefreshURL"] = AccountSessionURL(f, f.Offset)
			}
		}
	}
	a.renderStatus(w, r, status, name, data)
}

func (a *App) renderAccountProfilePage(w http.ResponseWriter, r *http.Request, status int, tab string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	user := a.currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	data["Title"] = "Профиль"
	if tab == "notifications" {
		data["Title"] = "Уведомления"
	}
	if tab == "access" {
		data["Title"] = "Доступ и лимиты"
	}
	if _, ok := data["ProfileUsername"]; !ok {
		data["ProfileUsername"] = user.Username
	}
	if _, ok := data["ProfileEmail"]; !ok {
		data["ProfileEmail"] = user.Email.String
	}
	data["CanChangeUsername"] = user.Role == "admin"
	data["AccountNow"] = time.Now()
	if tab == "notifications" {
		if _, provided := data["NotificationPreferences"]; !provided {
			summary, err := a.store.UserNotificationSummary(r.Context(), user.ID)
			if err != nil {
				data["NotificationLoadError"] = true
				data["NotificationSummary"] = domain.UserNotificationSummary{}
				if status == http.StatusOK {
					status = http.StatusServiceUnavailable
				}
			} else {
				data["NotificationSummary"], data["NotificationPreferences"] = summary, summary.Preferences
			}
		}
	}
	if tab == "access" {
		quota, err := a.store.QuickGenerationQuota(r.Context(), user.ID)
		data["Access"] = BuildAdminProfileAccess(*user, quota, time.Now())
		if err != nil {
			data["QuotaLoadError"] = true
			if status == http.StatusOK {
				status = http.StatusServiceUnavailable
			}
		}
		stats, err := a.store.UserStats(r.Context(), user.ID, 7)
		if err != nil {
			data["StatsLoadError"] = true
			if status == http.StatusOK {
				status = http.StatusServiceUnavailable
			}
		} else {
			data["Stats"] = stats
		}
		if _, ok := data["MiningPreference"]; !ok {
			data["MiningPreference"] = user.PauseMiningForQuickGeneration
		}
		if _, ok := data["PriorityPreference"]; !ok {
			data["PriorityPreference"] = user.QueuePriority
		}
	}
	a.renderAccountPage(w, r, status, "account_profile", tab, data)
}

func (a *App) handleAccountTheme(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !a.validCSRF(r) {
		a.renderAccountProfilePage(w, r, 403, "profile", map[string]any{"Error": "Не удалось подтвердить запрос. Обновите страницу.", "WriteUncertain": true})
		return
	}
	theme := r.Form.Get("theme")
	if len(r.PostForm["theme"]) != 1 || (theme != "system" && theme != "light" && theme != "dark") {
		a.renderAccountProfilePage(w, r, 400, "profile", map[string]any{"Error": "Выберите системную, светлую или тёмную тему."})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "ai_gateway_theme", Value: theme, Path: "/", MaxAge: 31536000, SameSite: http.SameSiteLaxMode, Secure: a.cookieSecure(r)})
	http.Redirect(w, r, "/account/profile?theme_updated=1#appearance", http.StatusSeeOther)
}

func ParseAccountSessionFilter(v url.Values) (store.UserProfileFilter, error) {
	f := store.UserProfileFilter{Search: v.Get("q"), Result: v.Get("result"), Sort: v.Get("sort")}
	for _, key := range []string{"q", "result", "sort", "offset"} {
		if len(v[key]) > 1 {
			return f, store.ErrUserProfileFilter
		}
	}
	if raw := v.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || strconv.Itoa(n) != raw {
			return f, store.ErrUserProfileFilter
		}
		f.Offset = n
	}
	return f.Normalize(true)
}

func AccountSessionURL(f store.UserProfileFilter, offset int) string {
	v := url.Values{}
	for key, value := range map[string]string{"q": f.Search, "result": f.Result, "sort": f.Sort} {
		if value != "" {
			v.Set(key, value)
		}
	}
	if offset > 0 {
		v.Set("offset", strconv.Itoa(offset))
	}
	if len(v) == 0 {
		return "/account/sessions#session-results"
	}
	return "/account/sessions?" + v.Encode() + "#session-results"
}

func BuildAccountSessionPagination(p store.UserProfilePage, count int) adminProfilePagination {
	v := adminProfilePagination{UserProfilePage: p, ResetURL: "/account/sessions#session-results", CurrentURL: AccountSessionURL(p.Filter, p.Filter.Offset)}
	if p.Total > 0 {
		v.First, v.Last = int64(p.Filter.Offset)+1, int64(p.Filter.Offset+count)
	}
	if p.Filter.Offset > 0 {
		v.PreviousURL = AccountSessionURL(p.Filter, max(0, p.Filter.Offset-store.UserProfilePageSize))
	}
	if v.Last < p.Total {
		v.NextURL = AccountSessionURL(p.Filter, p.Filter.Offset+store.UserProfilePageSize)
	}
	return v
}
