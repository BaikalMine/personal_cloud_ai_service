package gateway

import (
	"net/http"

	"ai-access-gateway/internal/domain"
)

// AuthPageView deliberately has no password or secret-token field.
type AuthPageView struct {
	Login      string
	Next       string
	Username   string
	Email      string
	Error      string
	ErrorField string
	State      string
	Access     *domain.InviteAccess
}

func (a *App) renderAuthPage(w http.ResponseWriter, r *http.Request, status int, name string, view AuthPageView) {
	title := "Вход"
	if name == "invite" {
		title = "Создание аккаунта"
	}
	view.Login = truncate(view.Login, 254)
	view.Username = truncate(view.Username, 256)
	view.Email = truncate(view.Email, 254)
	view.Next = safeNext(view.Next)
	a.renderStatus(w, r, status, name, map[string]any{"Title": title, "AuthPage": true, "Auth": view})
}
