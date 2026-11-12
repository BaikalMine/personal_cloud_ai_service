package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
	"ai-access-gateway/internal/store"
)

// Fake account values are scoped to one preview browser; no credentials are stored.
type accountPreviewState struct {
	Scenario, Username, Email       string
	Preferences                     domain.UserNotificationPreferences
	Priority, Mining, RevokedOthers bool
	Revoked                         []int64
}

func registerAccountPagesPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, now time.Time) {
	defaults := func() accountPreviewState {
		return accountPreviewState{Username: "admin", Email: "admin@example.local", Preferences: domain.UserNotificationPreferences{InAppEnabled: true, SuccessEnabled: true}}
	}
	read := func(r *http.Request) accountPreviewState {
		state := defaults()
		if cookie, err := r.Cookie("preview-account"); err == nil {
			if raw, err := base64.RawURLEncoding.DecodeString(cookie.Value); err == nil {
				_ = json.Unmarshal(raw, &state)
			}
		}
		if r.URL.Query().Has("scenario") {
			state = defaults()
			state.Scenario = r.URL.Query().Get("scenario")
		}
		return state
	}
	remember := func(w http.ResponseWriter, state accountPreviewState) {
		raw, _ := json.Marshal(state)
		http.SetCookie(w, &http.Cookie{Name: "preview-account", Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/", SameSite: http.SameSiteLaxMode})
	}
	profile := func(w http.ResponseWriter, r *http.Request) {
		state := read(r)
		remember(w, state)
		tab := r.URL.Query().Get("tab")
		if tab == "" {
			tab = "profile"
		}
		if strings.Contains(r.URL.Path, "password") {
			tab = "password"
		}
		if strings.Contains(r.URL.Path, "sessions") {
			tab = "sessions"
		}
		if r.URL.Path == "/account/notifications" {
			tab = "notifications"
		}
		if r.URL.Path == "/account/quick-generation-priority" || r.URL.Path == "/account/generation-mining" {
			tab = "access"
		}
		title, name := "Профиль", "account_profile"
		switch tab {
		case "notifications":
			title = "Уведомления"
		case "access":
			title = "Доступ и лимиты"
		case "password":
			title, name = "Смена пароля", "account_password"
		case "sessions":
			title, name = "Активные сессии", "account_sessions"
		}
		user := domain.User{ID: 1, Username: state.Username, Email: sql.NullString{String: state.Email, Valid: state.Email != ""}, Role: "admin", QueuePriority: state.Priority, PauseMiningForQuickGeneration: state.Mining}
		if state.Scenario == "limited" || state.Scenario == "no-access" {
			user.Role = "user"
			user.Username = "creator"
			user.AccountExpiresAt = sql.NullTime{Time: now.Add(7 * 24 * time.Hour), Valid: true}
		}
		if state.Scenario == "limited" {
			user.CanUseQuickGeneration = true
			user.CanGenerateTextToImage = true
			user.CanGenerateVideo = true
			user.CanTrainImageLora = true
			user.GenerationDailyLimit = 12
			user.GenerationTotalLimit = 50
			user.VideoGenerationDailyLimit = 2
			user.VideoGenerationTotalLimit = 8
			user.MaxVideoGenerationQuality = 720
		}
		if state.Scenario == "long" {
			user.Username = strings.Repeat("long.name", 28)
			user.Email.String = strings.Repeat("long.email", 23) + "@example.com"
		}
		data := map[string]any{"CurrentUser": &user, "AccountTab": tab, "AccountTabs": gateway.AccountTabs(tab), "AccountRefreshURL": "/account/profile", "ProfileUsername": user.Username, "ProfileEmail": user.Email.String, "CanChangeUsername": user.Role == "admin", "NotificationPreferences": state.Preferences, "PriorityPreference": state.Priority, "MiningPreference": state.Mining, "Stats": domain.UserStats{WeekRequests: 241, TotalRequests: 1682, TotalBytesOut: 512 << 20, ByService: []domain.ServiceUsage{{Service: "ComfyUI", Requests: 1220, Errors: 2}, {Service: "OpenWebUI", Requests: 462, Errors: 1}}}}
		for _, item := range gateway.AccountTabs(tab) {
			if item.Active {
				data["AccountRefreshURL"] = item.URL
			}
		}
		if r.URL.Path == "/preview/admin-password" {
			data["AdminWorkspace"] = true
		}
		quota := store.QuickGenerationQuota{Image: store.QuickGenerationQuotaBucket{DailyUsed: 4, TotalUsed: 18}, Video: store.QuickGenerationQuotaBucket{DailyUsed: 1, TotalUsed: 3}}
		data["Access"] = gateway.BuildAdminProfileAccess(user, quota, now)
		filter, err := gateway.ParseAccountSessionFilter(r.URL.Query())
		if err != nil {
			filter = store.UserProfileFilter{Sort: "newest"}
			data["Error"] = "Параметры списка не распознаны. Установите фильтры заново."
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", 400)
				return
			}
			if r.PostForm.Get("csrf") != "preview" {
				http.Error(w, "bad csrf", 403)
				return
			}
			uncertain := state.Scenario == "uncertain"
			successURL := ""
			switch r.URL.Path {
			case "/account/profile":
				data["ProfileEmail"] = r.PostForm.Get("email")
				if user.Role == "admin" {
					data["ProfileUsername"] = r.PostForm.Get("username")
				}
				if r.PostForm.Get("username") == "taken" {
					data["ErrorField"], data["Error"] = "username", "Этот логин уже используется другой учётной записью."
				} else if r.PostForm.Get("email") == "taken@example.com" {
					data["ErrorField"], data["Error"] = "email", "Этот e-mail уже используется другой учётной записью."
				} else if !uncertain {
					state.Username = data["ProfileUsername"].(string)
					state.Email = r.PostForm.Get("email")
					successURL = "/account/profile?updated=1"
				}
			case "/account/password":
				switch {
				case len(r.PostForm.Get("new_password")) < 10:
					data["ErrorField"], data["Error"] = "new_password", "Новый пароль должен содержать не менее 10 символов."
				case r.PostForm.Get("new_password") != r.PostForm.Get("confirm_password"):
					data["ErrorField"], data["Error"] = "confirm_password", "Новый пароль и подтверждение не совпадают."
				case r.PostForm.Get("current_password") != "preview-password":
					data["ErrorField"], data["Error"] = "current_password", "Текущий пароль указан неверно."
				default:
					if !uncertain {
						state.RevokedOthers = true
						successURL = "/account/password?updated=1"
					}
				}
			case "/account/notifications":
				prefs := domain.UserNotificationPreferences{InAppEnabled: r.PostForm.Get("in_app_enabled") == "on", SuccessEnabled: r.PostForm.Get("success_enabled") == "on", BrowserEnabled: r.PostForm.Get("browser_enabled") == "on"}
				if !prefs.InAppEnabled {
					prefs.BrowserEnabled = false
				}
				data["NotificationPreferences"] = prefs
				if !uncertain {
					state.Preferences = prefs
					successURL = "/account/profile?tab=notifications&notifications_updated=1#notification-settings"
				}
			case "/account/quick-generation-priority":
				if !uncertain {
					state.Priority = r.PostForm.Get("enabled") == "on"
					successURL = "/account/profile?tab=access&priority_updated=1#generation-resources"
				}
			case "/account/generation-mining":
				if !uncertain {
					state.Mining = r.PostForm.Get("pause_mining_for_quick_generation") == "on"
					successURL = "/account/profile?tab=access&mining_updated=1#generation-resources"
				}
			case "/account/sessions":
				if r.PostForm.Get("confirm_revoke") != "1" {
					data["Error"] = "Подтвердите завершение сессии."
				} else if !uncertain {
					id, _ := strconv.ParseInt(r.PostForm.Get("session_id"), 10, 64)
					if r.PostForm.Get("action") == "revoke_others" {
						state.RevokedOthers = true
					} else if id != 71 && id > 0 {
						state.Revoked = append(state.Revoked, id)
					}
					target, _ := url.Parse(gateway.AccountSessionURL(filter, filter.Offset))
					query := target.Query()
					query.Set("status", "revoked")
					target.RawQuery = query.Encode()
					successURL = target.String()
				}
			}
			if uncertain {
				data["WriteUncertain"], data["ErrorField"], data["Error"] = true, "", "Результат сохранения не подтверждён. Проверьте данные перед повторной попыткой."
			}
			if successURL != "" {
				remember(w, state)
				http.Redirect(w, r, successURL, http.StatusSeeOther)
				return
			}
		}
		if state.Scenario == "unavailable" {
			data["NotificationLoadError"], data["QuotaLoadError"], data["StatsLoadError"], data["SessionLoadError"] = true, true, true, true
		}
		if state.Scenario == "uncertain" {
			data["WriteUncertain"] = true
			data["Error"] = "Результат сохранения не подтверждён. Проверьте данные перед повторной попыткой."
		}
		if state.Scenario == "long" {
			data["Error"] = "Не удалось сохранить данные. " + strings.Repeat("ПодробностьБезПробелов", 30)
		}
		if tab == "sessions" {
			items := []domain.AccountSession{{ID: 71, Current: true, IP: "192.168.1.86", UserAgent: "Chrome 139 / Windows 11", CreatedAt: now.Add(-8 * time.Hour), LastSeenAt: now.Add(-time.Minute), ExpiresAt: now.Add(22 * time.Hour)}, {ID: 68, IP: "2001:db8:1234:5678:9012:3456:7890:abcd", UserAgent: "Chrome Mobile / Android 15 with a deliberately long device description", CreatedAt: now.Add(-36 * time.Hour), LastSeenAt: now.Add(-3 * time.Hour), ExpiresAt: now.Add(12 * time.Hour)}}
			if state.Scenario == "many" {
				for n := 0; n < 105; n++ {
					items = append(items, domain.AccountSession{ID: int64(200 + n), IP: fmt.Sprintf("10.0.0.%d", n), UserAgent: fmt.Sprintf("Browser %d", n), CreatedAt: now.Add(-48 * time.Hour), LastSeenAt: now.Add(-time.Duration(n+2) * time.Minute), ExpiresAt: now.Add(12 * time.Hour)})
				}
			}
			if state.Scenario == "long" {
				items[1].UserAgent = strings.Repeat("LongDeviceDescriptionWithoutSpaces", 60)
			}
			items = slices.DeleteFunc(items, func(item domain.AccountSession) bool {
				return state.Scenario == "empty" || (state.RevokedOthers && !item.Current) || slices.Contains(state.Revoked, item.ID) || (filter.Result == "current" && !item.Current) || (filter.Result == "others" && item.Current) || !strings.Contains(strings.ToLower(item.IP+" "+item.UserAgent), strings.ToLower(filter.Search))
			})
			slices.SortFunc(items, func(a, b domain.AccountSession) int {
				order := b.LastSeenAt.Compare(a.LastSeenAt)
				if order == 0 {
					order = int(b.ID - a.ID)
				}
				if filter.Sort == "oldest" {
					order = -order
				}
				return order
			})
			total := len(items)
			if filter.Offset >= total {
				filter.Offset = max(0, (total-1)/store.UserProfilePageSize*store.UserProfilePageSize)
			}
			end := min(total, filter.Offset+store.UserProfilePageSize)
			items = items[filter.Offset:end]
			data["Sessions"] = gateway.BuildAccountSessionRows(items, 6*time.Hour, time.Time{})
			data["Filter"] = filter
			data["Pagination"] = gateway.BuildAccountSessionPagination(store.UserProfilePage{Filter: filter, Total: int64(total), Now: now}, len(items))
			data["SnapshotAt"] = now
			data["AccountRefreshURL"] = gateway.AccountSessionURL(filter, filter.Offset)
		}
		if r.URL.Query().Get("updated") == "1" {
			if tab == "password" {
				data["Success"] = "Пароль изменён. Остальные сессии завершены."
			} else {
				data["Success"] = "Профиль обновлён."
			}
		}
		for key, message := range map[string]string{"notifications_updated": "Настройки уведомлений обновлены.", "priority_updated": "Приоритет заданий сохранён.", "mining_updated": "Настройка майнинга сохранена.", "theme_updated": "Тема сохранена.", "status": "Сессия завершена."} {
			if r.URL.Query().Get(key) != "" {
				data["Success"] = message
			}
		}
		render(name, title, data)(w, r)
	}
	for _, path := range []string{"/preview/profile", "/account/profile", "/preview/password", "/preview/admin-password", "/account/password", "/preview/account-sessions", "/account/sessions", "/account/notifications", "/account/quick-generation-priority", "/account/generation-mining"} {
		mux.HandleFunc(path, profile)
	}
	mux.HandleFunc("/account/theme", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if r.ParseForm() != nil || r.PostForm.Get("csrf") != "preview" {
			http.Error(w, "bad form", 403)
			return
		}
		theme := r.PostForm.Get("theme")
		if theme != "system" && theme != "light" && theme != "dark" {
			http.Error(w, "theme", 400)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "ai_gateway_theme", Value: theme, Path: "/", MaxAge: 31536000, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/account/profile?theme_updated=1#appearance", http.StatusSeeOther)
	})
}
