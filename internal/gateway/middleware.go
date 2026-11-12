package gateway

import (
	"context"
	"net/http"
	"net/url"
)

func (a *App) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := a.currentUser(r)
		if user == nil {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) requireAdmin(next http.Handler) http.Handler {
	return a.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := a.currentUser(r)
		if user == nil || user.Role != "admin" {
			a.forbiddenPage(w, r, "Для этой страницы нужны права администратора.")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (a *App) requireLANAdmin(next http.Handler) http.Handler {
	return a.adminLANOnly(a.requireAdmin(next))
}

func (a *App) requireServiceAccess(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := a.currentUser(r)
		if user == nil || !user.CanUseService(service) {
			a.forbiddenPage(w, r, "Вашему аккаунту не разрешён доступ к "+serviceDisplayName(service)+".")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) adminLANOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.isAdminAllowedIP(a.clientIP(r)) {
			a.forbiddenPage(w, r, "Эта страница доступна только из разрешённой сети.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
