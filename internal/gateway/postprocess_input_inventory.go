package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func (a *App) handleAdminUnresolvedInputs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		writeJSON(w, 403, map[string]any{"error": "access_denied"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, 405, map[string]any{"error": "method_not_allowed"})
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	valid := err == nil
	for key, values := range query {
		if (key != "after" && key != "limit") || len(values) != 1 {
			valid = false
		}
	}
	limit := 50
	if values, ok := query["limit"]; ok {
		limit, err = strconv.Atoi(values[0])
		if err != nil || strconv.Itoa(limit) != values[0] || limit < 1 || limit > 100 {
			valid = false
		}
	}
	after := query.Get("after")
	if len(after) > 192 {
		valid = false
	}
	if !valid {
		writeJSON(w, 400, map[string]any{"error": "invalid_inventory_request"})
		return
	}
	if a.store == nil {
		writeJSON(w, 503, map[string]any{"error": "registry_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rows, err := a.store.AdminUnresolvedPostprocessInputs(ctx, after, limit)
	if err != nil {
		writeJSON(w, 503, map[string]any{"error": "registry_unavailable"})
		return
	}
	type item struct {
		ID         string    `json:"id"`
		UserID     *int64    `json:"user_id,string"`
		JobID      *int64    `json:"job_id,string"`
		Filename   string    `json:"filename"`
		Subfolder  string    `json:"subfolder"`
		SizeBytes  int64     `json:"size_bytes"`
		SHA256     string    `json:"sha256"`
		State      string    `json:"state"`
		CreatedAt  time.Time `json:"created_at"`
		CanCleanup bool      `json:"can_cleanup"`
	}
	items := make([]item, 0, len(rows))
	for _, row := range rows {
		items = append(items, item{ID: row.ID, UserID: row.UserID, JobID: row.JobID, Filename: row.Filename, Subfolder: row.Subfolder, SizeBytes: row.SizeBytes, SHA256: row.SHA256, State: row.State, CreatedAt: row.CreatedAt})
	}
	next := ""
	if len(rows) == limit {
		next = rows[len(rows)-1].ID
	}
	writeJSON(w, 200, map[string]any{"scope": "unresolved_input_registry", "items": items, "next_after": next})
}
