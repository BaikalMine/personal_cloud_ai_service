package gateway

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/store"
)

type adminTrainingRow struct {
	loraTrainingJobView
	ProfileName  string
	ShowProgress bool
}

type adminTrainingView struct {
	Filter                                          store.AdminLoraTrainingFilter
	Total, Active, Completed, Failed, ArtifactBytes int64
	RangeStart, RangeEnd                            int64
	Now                                             time.Time
	Jobs                                            []adminTrainingRow
	Profiles                                        []loratraining.Profile
	ReadyProfiles                                   int
	AgentMessage                                    string
	PreviousURL, NextURL, RefreshURL, ReturnQuery   string
	Filtered                                        bool
}

func parseAdminTrainingFilter(raw string) (store.AdminLoraTrainingFilter, error) {
	f := store.AdminLoraTrainingFilter{}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return f, store.ErrAdminLoraTrainingFilter
	}
	for _, key := range []string{"q", "state", "family", "sort", "offset"} {
		if len(q[key]) > 1 {
			return f, store.ErrAdminLoraTrainingFilter
		}
	}
	f.Search, f.State, f.Family, f.Sort = q.Get("q"), q.Get("state"), q.Get("family"), q.Get("sort")
	if q.Get("offset") != "" {
		f.Offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil {
			return f, store.ErrAdminLoraTrainingFilter
		}
	}
	return f.Normalize()
}

func adminTrainingQuery(f store.AdminLoraTrainingFilter) string {
	q := url.Values{}
	if f.Search != "" {
		q.Set("q", f.Search)
	}
	if f.State != "" {
		q.Set("state", f.State)
	}
	if f.Family != "" {
		q.Set("family", f.Family)
	}
	if f.Sort != "" && f.Sort != "active" {
		q.Set("sort", f.Sort)
	}
	if f.Offset > 0 {
		q.Set("offset", strconv.Itoa(f.Offset))
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

func adminTrainingRedirect(r *http.Request, result string) string {
	f, err := parseAdminTrainingFilter(r.URL.RawQuery)
	if err != nil {
		f = store.AdminLoraTrainingFilter{}
	}
	u, _ := url.Parse("/admin/lora-training" + adminTrainingQuery(f))
	q := u.Query()
	q.Set(result, "1")
	u.RawQuery = q.Encode()
	return u.String()
}

func (a *App) loadAdminTraining(ctx context.Context, filter store.AdminLoraTrainingFilter) (adminTrainingView, error) {
	page, err := a.store.ListAdminLoraTrainingPage(ctx, filter)
	if err != nil {
		return adminTrainingView{}, err
	}
	profiles, message := a.loraTrainingProfiles(ctx)
	v := adminTrainingView{Filter: page.Filter, Total: page.Total, Active: page.Active, Completed: page.Completed,
		Failed: page.Failed, ArtifactBytes: page.ArtifactBytes, Now: page.Now, Profiles: profiles, AgentMessage: message,
		Jobs: make([]adminTrainingRow, 0, len(page.Jobs))}
	if len(page.Jobs) > 0 {
		v.RangeStart = int64(page.Filter.Offset + 1)
		v.RangeEnd = int64(page.Filter.Offset + len(page.Jobs))
	}
	v.Filtered = v.Filter.Search != "" || v.Filter.State != "" || v.Filter.Family != ""
	v.ReturnQuery = adminTrainingQuery(v.Filter)
	v.RefreshURL = "/admin/lora-training" + v.ReturnQuery
	if page.Filter.Offset > 0 {
		previous := page.Filter
		previous.Offset = max(0, previous.Offset-store.AdminLoraTrainingPageSize)
		v.PreviousURL = "/admin/lora-training" + adminTrainingQuery(previous)
	}
	if v.RangeEnd < v.Total {
		next := page.Filter
		next.Offset += store.AdminLoraTrainingPageSize
		v.NextURL = "/admin/lora-training" + adminTrainingQuery(next)
	}
	names := make(map[string]string)
	for _, profile := range profiles {
		names[profile.ID] = profile.Name
		if profile.Ready {
			v.ReadyProfiles++
		}
	}
	for _, job := range page.Jobs {
		row := adminTrainingRow{loraTrainingJobView: newLoraTrainingJobView(job), ProfileName: names[job.ProfileID],
			ShowProgress: !job.State.Terminal() && job.State != domain.LoraTrainingQueued}
		row.CanCancel = row.CanCancel && job.CancellationRequestedAt == nil
		row.Progress = min(100, max(0, row.Progress))
		if row.ProfileName == "" {
			row.ProfileName = job.ProfileID
		}
		v.Jobs = append(v.Jobs, row)
	}
	return v, nil
}

func (a *App) handleAdminLoraTraining(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if user := a.currentUser(r); user == nil || user.Role != "admin" {
		http.Error(w, "доступ закрыт", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	f, err := parseAdminTrainingFilter(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "неверные фильтры обучения", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	v, err := a.loadAdminTraining(ctx, f)
	if err != nil {
		http.Error(w, "не удалось загрузить задания обучения", http.StatusServiceUnavailable)
		return
	}
	data := map[string]any{"Title": "Обучение LoRA", "View": v, "CSRF": a.csrfToken(r),
		"Cancelled": r.URL.Query().Get("cancelled") == "1", "Deleted": r.URL.Query().Get("deleted") == "1"}
	if r.URL.Path == "/admin/lora-training/overview" {
		var fragment bytes.Buffer
		if err := a.tpl.ExecuteTemplate(&fragment, "admin_training_snapshot", data); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "training_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"snapshot_html": fragment.String()})
		return
	}
	a.render(w, r, "admin_lora_training", data)
}

func (a *App) handleAdminTrainingGPU(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if user := a.currentUser(r); user == nil || user.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access_denied"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		return
	}
	keys, err := parseTaskGPUKeys(q)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		return
	}
	ids := make([]string, len(keys))
	for i, key := range keys {
		if key.Kind != "lora_training" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
			return
		}
		ids[i] = key.ID
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	snapshot, err := a.store.AdminTrainingGPUSnapshots(ctx, ids)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "gpu_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
