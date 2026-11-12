package gateway

import (
	"net/http"
	"strconv"
	"time"
)

type postprocessReferenceView struct {
	MediaID   string    `json:"media_id"`
	JobID     string    `json:"job_id"`
	Filename  string    `json:"filename"`
	URL       string    `json:"url"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Sensitive bool      `json:"sensitive"`
}

func (a *App) handlePostprocessReferences(w http.ResponseWriter, r *http.Request) {
	v, ok := postprocessHTTPValues(w, r, http.MethodGet, "exclude", "before", "search")
	if !ok {
		return
	}
	var exclude, before int64
	for key, target := range map[string]*int64{"exclude": &exclude, "before": &before} {
		if value := v.Get(key); value != "" {
			var err error
			*target, err = postprocessExactID(value)
			if err != nil {
				writePostprocessHTTPError(w, err)
				return
			}
		}
	}
	if len(v.Get("search")) > 200 {
		writePostprocessHTTPError(w, errPostprocessQuoteInvalid)
		return
	}
	u, err := a.generationRepeatUser(r.Context(), r)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	items, next, err := a.store.ListGenerationPostprocessReferences(r.Context(), u.ID, exclude, before, v.Get("search"))
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	views := make([]postprocessReferenceView, 0, len(items))
	for _, item := range items {
		id := strconv.FormatInt(item.MediaID, 10)
		views = append(views, postprocessReferenceView{MediaID: id, JobID: item.JobPublicID, Filename: item.Filename, URL: "/generate/library/" + id,
			SizeBytes: item.SizeBytes, CreatedAt: item.CreatedAt, ExpiresAt: item.ExpiresAt, Sensitive: item.Sensitive})
	}
	nextID := ""
	if next > 0 {
		nextID = strconv.FormatInt(next, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views, "next": nextID})
}
