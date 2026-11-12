package gateway

import "net/http"

// Keep the existing admin-only raw progress channel without exposing the
// ComfyUI browser or its root-relative APIs. Other users retain durable polling.
func (a *App) quickGenerationSocketHandler() http.Handler {
	proxy := a.proxyRootHandler("comfyui", a.cfg.ComfyUIUpstream, a.cfg.ComfyUIUpstreamAuthHeader)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		if !isWebSocket(r) {
			http.Error(w, "ожидается канал прогресса WebSocket", http.StatusBadRequest)
			return
		}
		if !validFormOrigin(r) {
			http.Error(w, "источник запроса не подтверждён", http.StatusForbidden)
			return
		}
		if a.cfg.ComfyUIUpstream == nil {
			http.Error(w, "канал прогресса недоступен", http.StatusServiceUnavailable)
			return
		}
		cloned := r.Clone(r.Context())
		cloned.URL.Path = "/ws"
		cloned.URL.RawPath = ""
		cloned.URL.RawQuery = ""
		proxy.ServeHTTP(w, cloned)
	})
}
