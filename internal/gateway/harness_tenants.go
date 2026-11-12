package gateway

import (
	"crypto/subtle"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"
)

func (a *App) harnessTenantProxy(id int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if a.cfg.RestoreDrillMode || a.cfg.Harness.TenantURL == nil || len(a.cfg.Harness.TenantKey) < 32 {
			http.Error(w, "Пользовательский Harness пока недоступен.", 503)
			return
		}
		if a.harnessSourceRejection(r) != "" {
			http.Error(w, "Источник запроса Harness не подтверждён.", 403)
			return
		}
		if r.URL.Path == "/harness" {
			http.Redirect(w, r, harnessMount, http.StatusTemporaryRedirect)
			return
		}
		limit := harnessBrowserBodyLimit(r)
		if r.ContentLength > limit {
			http.Error(w, "Размер загрузки превышает допустимый предел.", 413)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		proxy := httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(a.cfg.Harness.TenantURL)
			p.Out.URL.Path = strings.TrimPrefix(p.In.URL.Path, "/harness")
			p.Out.Header.Del("Cookie")
			p.Out.Header.Set("Authorization", "Bearer "+a.cfg.Harness.TenantKey)
			p.Out.Header.Set("X-Harness-User", strconv.FormatInt(id, 10))
			p.Out.Header.Del("Idempotency-Key")
			p.Out.Header.Set("Accept-Encoding", "")
		}, ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Set-Cookie")
			response.Header.Set("Cache-Control", "no-store")
			response.Header.Set("Referrer-Policy", "no-referrer")
			rewriteLocation(response, "/harness", a.cfg.Harness.TenantURL)
			return injectAIReturnLink(response, strings.Replace(openWebUIReturnLink, `href="/app"`, `href="/account/profile"`, 1)+`<span id="gateway-upload-retention" hidden data-hours="24"></span>`)
		}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Пользовательский Harness временно недоступен.", 502)
		}, FlushInterval: -1}
		proxy.ServeHTTP(w, r)
	})
}

func (a *App) harnessTenantInternal() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := a.cfg.Harness.TenantKey
		if a.cfg.RestoreDrillMode || len(key) < 32 || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+key)) != 1 {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/internal/harness-users/"), "/")
		if len(parts) < 2 || r.URL.RawPath != "" {
			http.NotFound(w, r)
			return
		}
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[0] {
			http.NotFound(w, r)
			return
		}
		user, err := a.store.UserByID(r.Context(), id)
		if err != nil || user.Role == "admin" || user.Disabled || user.AccountExpiresAt.Valid && !user.AccountExpiresAt.Time.After(time.Now()) || !user.CanUseHarness {
			http.Error(w, "Harness access denied", 403)
			return
		}
		if len(parts) == 2 && parts[1] == "access" && r.Method == "GET" {
			writeJSON(w, 200, map[string]any{"allowed": true, "user_id": id})
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.NotFound(w, r)
			return
		}
		path := strings.Join(parts[1:], "/")
		if !(path == "v1/models" && r.Method == "GET" || path == "v1/chat/completions" && r.Method == "POST" || path == "harness/task" && r.Method == "POST") {
			http.NotFound(w, r)
			return
		}
		r = r.Clone(r.Context())
		r.URL.Path = harnessModelMount + "/" + path
		r.Header = r.Header.Clone()
		r.Header.Set("Authorization", "Bearer "+a.cfg.Harness.ModelKey)
		a.harnessModelProxy().ServeHTTP(w, r)
	})
}
