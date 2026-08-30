package gateway

import "net/http"

func (a *App) handleApp(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	stats, err := a.store.UserStats(r.Context(), user.ID, 7)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	activities, err := a.store.LatestActivity(r.Context(), user.ID, 80)
	if err != nil {
		http.Error(w, "ошибка базы данных", http.StatusInternalServerError)
		return
	}
	activities = prepareUserActivities(activities, 12)
	canAccessMining := user.CanAccessMining()
	mining := MiningOverview{}
	if canAccessMining {
		mining = a.miningOverview(r.Context(), false, user.Role == "admin")
	}
	a.render(w, r, "app", map[string]any{
		"Title":                "Главная",
		"Stats":                stats,
		"Activities":           activities,
		"Services":             a.serviceStatuses(r.Context()),
		"Mining":               mining,
		"CanAccessMining":      canAccessMining,
		"CanViewMiningDetails": user.Role == "admin",
		"MiningStatus":         r.URL.Query().Get("mining"),
		"PriorityStatus":       r.URL.Query().Get("priority"),
	})
}
