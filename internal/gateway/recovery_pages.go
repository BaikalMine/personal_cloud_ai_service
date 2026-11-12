package gateway

import (
	"net/http"
	"strings"
)

type RecoveryPageView struct {
	Code       int
	Title      string
	Reason     string
	Icon       string
	RetryURL   string
	RetryLabel string
}

func wantsHTMLPage(r *http.Request) bool {
	return !isWebSocket(r) && (r.Header.Get("Sec-Fetch-Dest") == "document" || strings.Contains(r.Header.Get("Accept"), "text/html"))
}

func (a *App) renderRecoveryPage(w http.ResponseWriter, r *http.Request, view RecoveryPageView) {
	w.Header().Set("Cache-Control", "no-store")
	if !wantsHTMLPage(r) || a.tpl == nil {
		http.Error(w, view.Reason, view.Code)
		return
	}
	view.RetryURL = safeNext(view.RetryURL)
	a.renderStatus(w, r, view.Code, "recovery_page", map[string]any{"Title": view.Title, "Recovery": view})
}

func (a *App) notFoundPage(w http.ResponseWriter, r *http.Request) {
	a.renderRecoveryPage(w, r, RecoveryPageView{Code: http.StatusNotFound, Title: "Страница не найдена", Reason: "Адрес изменился или этой страницы больше нет.", Icon: "file-question"})
}

func (a *App) forbiddenPage(w http.ResponseWriter, r *http.Request, reason string) {
	a.renderRecoveryPage(w, r, RecoveryPageView{Code: http.StatusForbidden, Title: "Доступ запрещён", Reason: reason, Icon: "shield-alert"})
}

func (a *App) upstreamUnavailablePage(w http.ResponseWriter, r *http.Request, service string) {
	view := RecoveryPageView{Code: http.StatusBadGateway, Title: "Сервис недоступен", Icon: "unplug", Reason: "Не удалось подключиться к " + serviceDisplayName(service) + ". Повторите попытку позже."}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		view.RetryURL, view.RetryLabel = r.URL.RequestURI(), "Повторить подключение"
	} else {
		view.Reason = "Ответ от " + serviceDisplayName(service) + " не получен. Запрос мог быть обработан. Проверьте состояние перед повторной отправкой."
		if service == "comfyui" || service == "openwebui" {
			view.RetryURL, view.RetryLabel = "/"+service, "Проверить сервис"
		}
	}
	a.renderRecoveryPage(w, r, view)
}
