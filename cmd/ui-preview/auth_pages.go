package main

import (
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
)

func registerAuthPagesPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, now time.Time) {
	access := domain.InviteAccess{ID: 21, ExpiresAt: now.Add(48 * time.Hour), AccountLifetimeSeconds: 7 * 24 * 60 * 60,
		GrantComfyUI: true, GrantOpenWebUI: true, GrantQuickGeneration: true, GrantTextToImage: true, GrantImageToImage: true,
		GrantVideo: true, GrantTrainImageLora: true, GrantAdvancedGenerationSettings: true, PauseMiningForQuickGeneration: true, QueuePriority: true,
		GenerationDailyLimit: 12, GenerationTotalLimit: 50, VideoGenerationDailyLimit: 2, VideoGenerationTotalLimit: 8, MaxVideoGenerationQuality: 720}
	auth := func(invite bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			name, title := "login", "Вход"
			view := gateway.AuthPageView{State: "active", Next: r.URL.Query().Get("next")}
			scenario := r.URL.Query().Get("scenario")
			if invite {
				name, title = "invite", "Создание аккаунта"
				copy := access
				if scenario == "training" {
					copy.GrantComfyUI, copy.GrantOpenWebUI, copy.GrantQuickGeneration = false, false, false
				}
				if scenario == "unlimited" {
					copy.GenerationDailyLimit, copy.GenerationTotalLimit, copy.VideoGenerationDailyLimit, copy.VideoGenerationTotalLimit, copy.AccountLifetimeSeconds = 0, 0, 0, 0, 0
				}
				view.Access = &copy
			}
			if scenario == "long" {
				view.Username, view.Email, view.Login = strings.Repeat("long-name", 28), strings.Repeat("long-email", 23)+"@example.com", strings.Repeat("long-name", 28)
				view.Error = "Не удалось сохранить данные. " + strings.Repeat("ПодробностьБезПробелов", 20)
			}
			if r.Method == http.MethodPost {
				r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
				if err := r.ParseForm(); err != nil {
					http.Error(w, "bad form", 400)
					return
				}
				view.Login, view.Username, view.Email, view.Next = r.FormValue("login"), r.FormValue("username"), r.FormValue("email"), r.FormValue("next")
				if !invite {
					if view.Login == "demo" && r.FormValue("password") == "preview-password" {
						http.Redirect(w, r, "/preview/auth-complete", http.StatusFound)
						return
					}
					view.Error, view.ErrorField = "Неверный логин или пароль.", "login"
				} else {
					switch {
					case view.Username == "taken":
						view.Error, view.ErrorField = "Этот логин уже занят.", "username"
					case view.Email == "taken@example.com":
						view.Error, view.ErrorField = "Этот email уже используется.", "email"
					case len(r.FormValue("password")) < 10:
						view.Error, view.ErrorField = "Пароль должен содержать не менее 10 символов.", "password"
					default:
						http.Redirect(w, r, "/login", http.StatusFound)
						return
					}
				}
			}
			switch scenario {
			case "invalid":
				view.State, view.Access, view.Error = "invalid", nil, "Приглашение недействительно, просрочено, отозвано или уже использовано."
			case "unavailable":
				view.State, view.Error = "unavailable", "Не удалось проверить приглашение. Повторите проверку позже."
				view.Access = nil
			case "uncertain":
				view.State, view.Username, view.Email, view.Error = "uncertain", "saved-name", "saved@example.com", "Результат регистрации не подтверждён. Попробуйте войти с указанными данными."
			case "error":
				view.Error = "Сервер занят. Повторите попытку позже."
			}
			render(name, title, map[string]any{"CurrentUser": nil, "AuthPage": true, "Auth": view})(w, r)
		}
	}
	mux.HandleFunc("/preview/login", auth(false))
	mux.HandleFunc("/login", auth(false))
	mux.HandleFunc("/preview/invite", auth(true))
	mux.HandleFunc("/invite/", auth(true))
	mux.HandleFunc("/preview/auth-complete", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/preview/profile", http.StatusSeeOther)
	})
	for _, item := range []struct {
		path string
		view gateway.RecoveryPageView
	}{
		{"/preview/forbidden", gateway.RecoveryPageView{Code: 403, Title: "Доступ запрещён", Reason: "Вашему аккаунту не разрешён доступ к ComfyUI.", Icon: "shield-alert"}},
		{"/preview/not-found", gateway.RecoveryPageView{Code: 404, Title: "Страница не найдена", Reason: "Адрес изменился или этой страницы больше нет.", Icon: "file-question"}},
		{"/preview/bad-gateway", gateway.RecoveryPageView{Code: 502, Title: "Сервис недоступен", Reason: "Не удалось подключиться к ComfyUI. Повторите попытку позже.", Icon: "unplug", RetryURL: "/preview/service", RetryLabel: "Повторить подключение"}},
	} {
		mux.HandleFunc(item.path, func(w http.ResponseWriter, r *http.Request) {
			view := item.view
			if r.Method == http.MethodPost {
				view.Reason, view.RetryLabel = "Запрос мог быть обработан. Проверьте состояние перед повторной отправкой.", "Проверить сервис"
			}
			if r.URL.Query().Get("scenario") == "long" {
				view.Reason += strings.Repeat("ДлинноеОписаниеБезПробелов", 60)
			}
			data := map[string]any{"Recovery": view}
			if r.URL.Query().Get("anonymous") == "1" {
				data["CurrentUser"] = nil
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(view.Code)
			render("recovery_page", view.Title, data)(w, r)
		})
	}
}
