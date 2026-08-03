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
	a.render(w, r, "app", map[string]any{
		"Title":        "Главная",
		"Stats":        stats,
		"Activities":   activities,
		"Services":     a.serviceStatuses(r.Context()),
		"Mining":       a.miningOverview(r.Context(), false),
		"MiningStatus": r.URL.Query().Get("mining"),
	})
}
