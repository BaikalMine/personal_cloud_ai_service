package gateway

import (
	"bytes"
	"context"
	"net/http"
	"time"
)

// The fragment and chart data share one refresh. Domain records (including
// mining lease credentials) are never serialized as an operations API.
func (a *App) handleAdminOperationsOverview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if user := a.currentUser(r); user == nil || user.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access_denied"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	system, err := a.systemOverview(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "overview_unavailable"})
		return
	}
	operations, err := a.loadAdminOperations(ctx, system)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "overview_unavailable"})
		return
	}
	var fragment bytes.Buffer
	if err := a.tpl.ExecuteTemplate(&fragment, "admin_operations_snapshot", map[string]any{"Operations": operations, "System": system}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "overview_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot_html": fragment.String(), "system": system})
}
