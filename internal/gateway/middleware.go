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
			http.Error(w, "доступ запрещён", http.StatusForbidden)
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
			a.renderStatus(w, r, http.StatusForbidden, "service_forbidden", map[string]any{
				"Title":   "Доступ запрещён",
				"Service": serviceDisplayName(service),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) adminLANOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.isAdminAllowedIP(a.clientIP(r)) {
			http.Error(w, "доступ запрещён", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
