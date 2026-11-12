package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"ai-access-gateway/internal/updates"
)

func (a *App) handleAdminComfyFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "access_denied"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	query, err := updates.ParseComfyFileInventoryQuery(r.URL.RawQuery)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_inventory_request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	inventory, err := a.updates.ComfyFileInventory(ctx, query)
	if errors.Is(err, updates.ErrComfyInventoryChanged) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "inventory_changed"})
		return
	}
	if err != nil {
		state := "unavailable"
		if !a.updates.Configured() {
			state = "not_configured"
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "host_inventory_unavailable", "host_state": state})
		return
	}
	registered, err := a.store.ComfyFileRegistrations(ctx, query.Kind, inventory.Items)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "registry_unavailable"})
		return
	}
	type observation struct {
		updates.ComfyFileInventoryItem
		Registration string `json:"registration"`
		CanCleanup   bool   `json:"can_cleanup"`
	}
	items := make([]observation, 0, len(inventory.Items))
	for _, item := range inventory.Items {
		state := "unregistered_path"
		if registered[item.ID] {
			state = "registered_path"
		}
		items = append(items, observation{ComfyFileInventoryItem: item, Registration: state})
	}
	// DB classification is a later read snapshot, never a content/hash match.
	type classifiedInventory struct {
		updates.ComfyFileInventory
		Items []observation `json:"items"`
	}
	writeJSON(w, http.StatusOK, struct {
		Scope             string              `json:"scope"`
		Inventory         classifiedInventory `json:"inventory"`
		RegistryCheckedAt time.Time           `json:"registry_checked_at"`
	}{Scope: "host_metadata", Inventory: classifiedInventory{ComfyFileInventory: inventory, Items: items}, RegistryCheckedAt: time.Now().UTC()})
}
