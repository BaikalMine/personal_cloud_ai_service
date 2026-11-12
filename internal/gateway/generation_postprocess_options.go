package gateway

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"ai-access-gateway/internal/store"
)

type postprocessOperationChoice struct {
	Options         postprocessOptions `json:"options"`
	RequiresUpscale bool               `json:"requires_upscale"`
}

type postprocessAvailableOptions struct {
	Version       int                          `json:"version"`
	Source        postprocessSourceView        `json:"source"`
	Upscale       []postprocessOperationChoice `json:"upscale"`
	Interpolation []postprocessOperationChoice `json:"interpolation"`
	Color         []postprocessOperationChoice `json:"color"`
	Reasons       map[string]string            `json:"reasons"`
	CheckedAt     time.Time                    `json:"checked_at"`
}

func (a *App) allowPostprocessOptions(now time.Time) bool {
	a.postprocessQuoteMu.Lock()
	defer a.postprocessQuoteMu.Unlock()
	if now.Before(a.postprocessOptionsNext) {
		return false
	}
	a.postprocessOptionsNext = now.Add(3 * time.Second)
	return true
}

func postprocessOperationGraph(facts postprocessSourceFacts, options postprocessOptions, namespace string, catalog comfySchemaCatalog) (map[string]any, error) {
	ext := ".png"
	if facts.MediaType == "video" {
		ext = ".mp4"
	}
	reference := ""
	if options.ColorMethod != "" {
		reference = namespace + "/operation-reference.png"
	}
	graph, _, err := buildPostprocessPrompt(facts, options, namespace+"/operation-source"+ext, reference, namespace, catalog)
	return graph, err
}

// Offer bounded, exact size pairs accepted by the same planner and live schema
// used at quote/dispatch. They are choices, not evidence of a completed output.
func postprocessUpscaleChoices(facts postprocessSourceFacts, namespace string, catalog comfySchemaCatalog) []postprocessOperationChoice {
	result := []postprocessOperationChoice{}
	if _, exists := catalog.Nodes["RTXVideoSuperResolution"]; !exists || facts.Width < postprocessMinSide || facts.Height < postprocessMinSide || facts.Width > postprocessMaxSide || facts.Height > postprocessMaxSide {
		return result
	}
	choicesAt := func(width int) []postprocessOperationChoice {
		height := max((facts.Height+7)/8*8, int(math.Round(float64(width)*float64(facts.Height)/float64(facts.Width)/8))*8)
		items := []postprocessOperationChoice{}
		if width == facts.Width && height == facts.Height {
			return items
		}
		for _, quality := range []string{"HIGH", "MEDIUM", "LOW", "ULTRA"} {
			o := postprocessOptions{Width: width, Height: height, Interpolate: 1, RTXQuality: quality}
			if _, err := postprocessOperationGraph(facts, o, namespace, catalog); err == nil {
				items = append(items, postprocessOperationChoice{Options: o})
			}
		}
		return items
	}
	minWidth, maxWidth := (facts.Width+7)/8*8, min(facts.Width*2, postprocessMaxSide)/8*8
	seen := map[int]bool{}
	add := func(width int) bool {
		if width < minWidth || width > maxWidth || seen[width] {
			return false
		}
		seen[width] = true
		items := choicesAt(width)
		result = append(result, items...)
		return len(items) > 0
	}
	for width := minWidth; width <= maxWidth; width += 8 {
		if add(width) {
			break
		}
	}
	for _, scale := range []float64{1.25, 1.5, 2} {
		add(int(math.Round(float64(facts.Width)*scale/8)) * 8)
	}
	for width := maxWidth; width >= minWidth; width -= 8 {
		if seen[width] {
			found := false
			for _, item := range result {
				if item.Options.Width == width {
					found = true
					break
				}
			}
			if found {
				break
			}
			continue
		}
		if add(width) {
			break
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Options.Width < result[j].Options.Width })
	return result
}

func postprocessDependentChoice(facts postprocessSourceFacts, base postprocessOptions, upscale []postprocessOperationChoice, namespace string, catalog comfySchemaCatalog) (postprocessOperationChoice, map[string]any, bool) {
	if graph, err := postprocessOperationGraph(facts, base, namespace, catalog); err == nil {
		return postprocessOperationChoice{Options: base}, graph, true
	}
	for _, size := range upscale {
		o := base
		o.Width, o.Height, o.RTXQuality = size.Options.Width, size.Options.Height, size.Options.RTXQuality
		if graph, err := postprocessOperationGraph(facts, o, namespace, catalog); err == nil {
			return postprocessOperationChoice{Options: o, RequiresUpscale: true}, graph, true
		}
	}
	return postprocessOperationChoice{}, nil, false
}

func (a *App) postprocessOptionsForSource(ctx context.Context, userID int64, jobID string, mediaID int64) (postprocessAvailableOptions, error) {
	result := postprocessAvailableOptions{Version: 1, Upscale: []postprocessOperationChoice{}, Interpolation: []postprocessOperationChoice{}, Color: []postprocessOperationChoice{},
		Reasons: map[string]string{"upscale": "Нет совместимого увеличения для этого файла", "interpolation": "Нет совместимой установленной модели интерполяции", "color": "Перенос цвета для этого файла недоступен"}}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	release, ok := a.acquirePostprocessQuote()
	if !ok {
		return result, errPostprocessSourceBusy
	}
	defer release()
	source, err := a.preparePostprocessSource(ctx, userID, jobID, mediaID)
	if err != nil {
		return result, err
	}
	identity := postprocessQuoteIdentity(source)
	measurement := source.Measurement
	if err := source.Close(); err != nil {
		return result, err
	}
	facts := postprocessMeasuredFacts(measurement)
	snapshot, err := a.comfyObjectInfo(ctx, true)
	if err != nil || snapshot.Source != comfyObjectInfoLive {
		return result, errPostprocessDispatchWaiting
	}
	namespace := comfyUploadNamespace(a.comfyClientID(userID))
	result.Upscale = postprocessUpscaleChoices(facts, namespace, snapshot.Schema)
	if len(result.Upscale) > 0 {
		delete(result.Reasons, "upscale")
	}
	for _, method := range []string{"adain", "mean_std"} {
		o := postprocessOptions{Width: facts.Width, Height: facts.Height, Interpolate: 1, ColorMethod: method, ColorStrength: 0.7}
		if choice, _, ok := postprocessDependentChoice(facts, o, result.Upscale, namespace, snapshot.Schema); ok {
			result.Color = append(result.Color, choice)
		}
	}
	if len(result.Color) > 0 {
		delete(result.Reasons, "color")
	}
	if facts.MediaType == "video" && a.loraTraining.Configured() {
		for _, checkpoint := range []string{"rife47.pth", "rife49.pth", "rife417.pth", "rife426.pth"} {
			choices := []postprocessOperationChoice{}
			var inspectGraph map[string]any
			for multiplier := 2; multiplier <= 4; multiplier++ {
				o := postprocessOptions{Width: facts.Width, Height: facts.Height, Interpolate: multiplier, RIFECheckpoint: checkpoint}
				if choice, graph, ok := postprocessDependentChoice(facts, o, result.Upscale, namespace, snapshot.Schema); ok {
					choices = append(choices, choice)
					inspectGraph = graph
				}
			}
			if len(choices) == 0 {
				continue
			}
			requestID := newRequestID()
			request, err := generationComponentRequest(requestID, inspectGraph)
			if err != nil {
				continue
			}
			response, err := a.loraTraining.InspectGenerationComponents(ctx, request)
			if err != nil || validatePostprocessComponents(requestID, inspectGraph, &response, time.Now()) != nil {
				continue
			}
			result.Interpolation = append(result.Interpolation, choices...)
		}
	} else if facts.MediaType != "video" {
		result.Reasons["interpolation"] = "Только для видео"
	} else {
		result.Reasons["interpolation"] = "Проверка модели интерполяции недоступна"
	}
	if len(result.Interpolation) > 0 {
		delete(result.Reasons, "interpolation")
	}
	parent, current, err := a.loadPostprocessOutputSource(ctx, userID, jobID, mediaID)
	if err != nil {
		return result, err
	}
	if parent.ID != identity.JobID || current.ContentHash != identity.ArchiveSHA256 || current.SizeBytes != identity.SizeBytes || current.MediaType != identity.MediaType || current.MIMEType != identity.MIMEType {
		return result, store.ErrPostprocessSource
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Source = postprocessSourceView{JobID: jobID, MediaID: strconv.FormatInt(mediaID, 10), Measurement: measurement, Availability: "available"}
	result.CheckedAt = time.Now().UTC()
	return result, nil
}

func (a *App) handlePostprocessOptions(w http.ResponseWriter, r *http.Request) {
	v, ok := postprocessHTTPValues(w, r, http.MethodPost, "source_job_id", "source_media_id")
	if !ok {
		return
	}
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "Проверка безопасности не пройдена")
		return
	}
	jobID := v.Get("source_job_id")
	mediaID, err := postprocessExactID(v.Get("source_media_id"))
	if err != nil || !validGenerationRequestID(jobID) {
		writePostprocessHTTPError(w, errPostprocessQuoteInvalid)
		return
	}
	user, err := a.generationRepeatUser(r.Context(), r)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	if !a.allowPostprocessOptions(time.Now()) {
		writePostprocessHTTPError(w, errPostprocessSourceBusy)
		return
	}
	result, err := a.postprocessOptionsForSource(r.Context(), user.ID, jobID, mediaID)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
