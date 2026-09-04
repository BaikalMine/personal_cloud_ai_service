package gateway

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (a *App) withServiceSelection(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/comfyui/" || r.URL.Path == "/openwebui/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		http.SetCookie(w, &http.Cookie{
			Name:     serviceCookieName,
			Value:    service,
			Path:     "/",
			Expires:  time.Now().Add(a.cfg.SessionTTL),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   a.cookieSecure(r),
		})
		next.ServeHTTP(w, r)
	})
}

func (a *App) serviceSelector(service string) http.Handler {
	target := "/?gateway_service=" + url.QueryEscape(service)
	return a.withServiceSelection(service, http.RedirectHandler(target, http.StatusSeeOther))
}

func (a *App) serviceCompatibilityRouter(comfyUI, openWebUI http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if selectedService(r) == "openwebui" {
			openWebUI.ServeHTTP(w, r)
			return
		}
		comfyUI.ServeHTTP(w, r)
	})
}

func (a *App) rootOrServices(services http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The selection cookie is required for OpenWebUI's root-relative API and
		// asset requests, but it must never turn the Gateway home page into the
		// last opened service. The selector's explicit query remains the only
		// way to proxy an upstream root document.
		if r.URL.Path == "/" && r.URL.Query().Get("gateway_service") == "" {
			a.handleRoot(w, r)
			return
		}
		if _, ok := requestedService(r); ok {
			services.ServeHTTP(w, r)
			return
		}
		a.handleRoot(w, r)
	})
}

func selectedService(r *http.Request) string {
	if service, ok := requestedService(r); ok {
		return service
	}
	return "comfyui"
}

func requestedService(r *http.Request) (string, bool) {
	if service := r.URL.Query().Get("gateway_service"); service == "openwebui" || service == "comfyui" {
		return service, true
	}
	// OpenWebUI uses an absolute backend URL and can omit Referer. This endpoint
	// does not exist in ComfyUI and is needed before the frontend can initialize.
	if r.URL.Path == "/api/config" {
		return "openwebui", true
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		if parsed, err := url.Parse(referer); err == nil {
			switch {
			case strings.HasPrefix(parsed.Path, "/openwebui/"):
				return "openwebui", true
			case strings.HasPrefix(parsed.Path, "/comfyui/"):
				return "comfyui", true
			}
		}
	}
	if cookie, err := r.Cookie(serviceCookieName); err == nil {
		if cookie.Value == "openwebui" || cookie.Value == "comfyui" {
			return cookie.Value, true
		}
	}
	return "", false
}
