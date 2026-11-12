package main

import (
	"ai-access-gateway/internal/buildinfo"
	"net/http"
)

func buildHandler(tokenHash [32]byte) http.Handler {
	build := buildinfo.Handler(buildinfo.SystemMonitor)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !authorized(r, tokenHash) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="system-monitor"`)
			http.Error(w, "authorization required", http.StatusUnauthorized)
			return
		}
		build.ServeHTTP(w, r)
	})
}
