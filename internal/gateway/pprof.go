package gateway

import (
	"net/http"
	"net/http/pprof"
)

func (a *App) registerPprofRoutes(mux *http.ServeMux) {
	if a == nil || mux == nil || !a.cfg.PprofEnabled {
		return
	}
	private := func(handler http.Handler) http.Handler { return a.adminLANOnly(handler) }
	mux.Handle("/debug/pprof/", private(http.HandlerFunc(pprof.Index)))
	mux.Handle("/debug/pprof/cmdline", private(http.HandlerFunc(pprof.Cmdline)))
	mux.Handle("/debug/pprof/profile", private(http.HandlerFunc(pprof.Profile)))
	mux.Handle("/debug/pprof/symbol", private(http.HandlerFunc(pprof.Symbol)))
	mux.Handle("/debug/pprof/trace", private(http.HandlerFunc(pprof.Trace)))
}
