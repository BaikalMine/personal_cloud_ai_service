package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/release"
)

func (a *App) handleAdminReleaseBinding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" || r.URL.RawPath != "" || r.URL.ForceQuery || a.cfg.RestoreDrillMode || a.cfg.ReleaseValidation != nil {
		http.Error(w, "release_binding_unavailable", http.StatusServiceUnavailable)
		return
	}
	pids, classes := r.Header.Values(release.RuntimeBindingPIDHeader), r.Header.Values(release.RuntimeBindingClassHeader)
	if len(pids) != 1 || len(classes) != 1 {
		http.Error(w, "release_binding_unavailable", http.StatusBadRequest)
		return
	}
	pid, pidErr := strconv.ParseInt(pids[0], 10, 64)
	class, classErr := strconv.ParseInt(classes[0], 10, 64)
	request := database.RuntimeBindingRequest{BackendPID: pid, ClassID: class}
	if pidErr != nil || classErr != nil || request.Validate() != nil {
		http.Error(w, "release_binding_unavailable", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	proof, err := a.store.ObserveRuntimeBinding(ctx, request)
	if err != nil {
		http.Error(w, "release_binding_unavailable", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(release.RuntimeBindingStatus{
		SchemaVersion: 1, Mode: "normal", Probe: request, Proof: proof, Gateway: buildinfo.NewReport(buildinfo.Gateway),
	})
}
