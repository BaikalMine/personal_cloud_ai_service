package gateway

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mediaprobe"
	"ai-access-gateway/internal/store"
)

const postprocessHTTPMaxBytes = 8 << 10

// Keep the slot until both decoded source spools have been closed, including
// while waiting for node/component inspection. Retrieving a receipt is free.
func (a *App) acquirePostprocessQuote() (func(), bool) {
	a.postprocessQuoteMu.Lock()
	defer a.postprocessQuoteMu.Unlock()
	if a.postprocessQuoteBusy {
		return nil, false
	}
	a.postprocessQuoteBusy = true
	var once sync.Once
	return func() {
		once.Do(func() {
			a.postprocessQuoteMu.Lock()
			a.postprocessQuoteBusy = false
			a.postprocessQuoteMu.Unlock()
		})
	}, true
}

func (a *App) allowNewPostprocessQuote(now time.Time) bool {
	a.postprocessQuoteMu.Lock()
	defer a.postprocessQuoteMu.Unlock()
	if now.Before(a.postprocessQuoteNext) {
		return false
	}
	a.postprocessQuoteNext = now.Add(3 * time.Second)
	return true
}

func (a *App) registerPostprocessQuoteRoutes(mux *http.ServeMux, quick func(http.Handler) http.Handler) {
	mux.Handle("/generate/postprocess/quote", quick(http.HandlerFunc(a.handlePostprocessQuote)))
	mux.Handle("/generate/postprocess/status", quick(http.HandlerFunc(a.handlePostprocessQuoteStatus)))
	mux.Handle("/generate/postprocess/discard", quick(http.HandlerFunc(a.handlePostprocessQuoteDiscard)))
	mux.Handle("/generate/postprocess/confirm", quick(http.HandlerFunc(a.handlePostprocessConfirm)))
	mux.Handle("/generate/postprocess/options", quick(http.HandlerFunc(a.handlePostprocessOptions)))
	mux.Handle("/generate/postprocess/references", quick(http.HandlerFunc(a.handlePostprocessReferences)))
}

// Do not merge query and body fields, accept repeated keys, or let ParseForm
// silently ignore a malformed/oversized body during CSRF validation.
func postprocessHTTPValues(w http.ResponseWriter, r *http.Request, method string, fields ...string) (url.Values, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeGenerationError(w, http.StatusMethodNotAllowed, "Метод не поддерживается")
		return nil, false
	}
	var values url.Values
	var err error
	if method == http.MethodGet {
		if len(r.URL.RawQuery) > postprocessHTTPMaxBytes {
			err = errPostprocessQuoteInvalid
		} else {
			values, err = url.ParseQuery(r.URL.RawQuery)
		}
	} else {
		kind, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if parseErr != nil || kind != "application/x-www-form-urlencoded" || r.URL.RawQuery != "" {
			err = errPostprocessQuoteInvalid
		} else {
			r.Body = http.MaxBytesReader(w, r.Body, postprocessHTTPMaxBytes)
			err = r.ParseForm()
			values = r.PostForm
		}
	}
	allowed := map[string]bool{}
	for _, field := range fields {
		allowed[field] = true
	}
	if method == http.MethodPost {
		allowed["csrf"] = true
	}
	for key, items := range values {
		if !allowed[key] || len(items) != 1 {
			err = errPostprocessQuoteInvalid
		}
	}
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, "Некорректный запрос постобработки")
		return nil, false
	}
	return values, true
}

func postprocessExactID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, errPostprocessQuoteInvalid
	}
	return id, nil
}

func parsePostprocessQuoteRequest(values url.Values) (postprocessQuoteRequest, error) {
	r := postprocessQuoteRequest{RequestID: values.Get("request_id"), SourceJobID: values.Get("source_job_id"), ReferenceJobID: values.Get("reference_job_id")}
	if len(r.RequestID) < 16 || !validGenerationRequestID(r.RequestID) || !validGenerationRequestID(r.SourceJobID) {
		return r, errPostprocessQuoteInvalid
	}
	var err error
	r.SourceMediaID, err = postprocessExactID(values.Get("source_media_id"))
	if err != nil {
		return r, err
	}
	for _, item := range []struct {
		key      string
		target   *int
		min, max int
	}{
		{"width", &r.Options.Width, postprocessMinSide, postprocessMaxSide},
		{"height", &r.Options.Height, postprocessMinSide, postprocessMaxSide},
		{"interpolate", &r.Options.Interpolate, 1, 4},
	} {
		n, e := postprocessExactID(values.Get(item.key))
		if e != nil || n < int64(item.min) || n > int64(item.max) {
			return r, errPostprocessQuoteInvalid
		}
		*item.target = int(n)
	}
	r.Options.RTXQuality = values.Get("rtx_quality")
	switch r.Options.RTXQuality {
	case "", "LOW", "MEDIUM", "HIGH", "ULTRA":
	default:
		return r, errPostprocessQuoteInvalid
	}
	r.Options.RIFECheckpoint = values.Get("rife_checkpoint")
	if r.Options.Interpolate == 1 {
		if r.Options.RIFECheckpoint != "" {
			return r, errPostprocessQuoteInvalid
		}
	} else {
		switch r.Options.RIFECheckpoint {
		case "rife47.pth", "rife49.pth", "rife417.pth", "rife426.pth":
		default:
			return r, errPostprocessQuoteInvalid
		}
	}
	r.Options.ColorMethod = values.Get("color_method")
	if r.Options.ColorMethod == "" {
		if r.ReferenceJobID != "" || values.Get("reference_media_id") != "" || values.Get("color_strength") != "" {
			return r, errPostprocessQuoteInvalid
		}
	} else {
		if r.Options.ColorMethod != "adain" && r.Options.ColorMethod != "mean_std" || !validGenerationRequestID(r.ReferenceJobID) {
			return r, errPostprocessQuoteInvalid
		}
		r.ReferenceMediaID, err = postprocessExactID(values.Get("reference_media_id"))
		if err != nil || r.ReferenceMediaID == r.SourceMediaID {
			return r, errPostprocessQuoteInvalid
		}
		r.Options.ColorStrength, err = strconv.ParseFloat(values.Get("color_strength"), 64)
		if err != nil || math.IsNaN(r.Options.ColorStrength) || math.IsInf(r.Options.ColorStrength, 0) || r.Options.ColorStrength <= 0 || r.Options.ColorStrength > 1 {
			return r, errPostprocessQuoteInvalid
		}
	}
	return r, nil
}

func writePostprocessHTTPError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusServiceUnavailable, "unavailable", "Подготовка постобработки временно недоступна"
	switch {
	case errors.Is(err, errPostprocessSourceBusy):
		status, code, message = http.StatusTooManyRequests, "busy", "Подготовка занята. Повторите запрос позже"
		w.Header().Set("Retry-After", "3")
	case errors.Is(err, store.ErrPostprocessQuoteLimit):
		status, code, message = http.StatusTooManyRequests, "pending_limit", "Уже подготовлены четыре расчёта. Подтвердите или отмените один из них"
		w.Header().Set("Retry-After", "300")
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, store.ErrPostprocessSource), errors.Is(err, errPostprocessSourceChanged):
		status, code, message = http.StatusNotFound, "source_unavailable", "Исходный файл или подтверждение недоступны"
	case errors.Is(err, errGenerationRepeatAccess), errors.Is(err, store.ErrQuickGenerationForbidden):
		status, code, message = http.StatusForbidden, "forbidden", "Доступ к постобработке закрыт"
	case errors.Is(err, store.ErrPostprocessExpired):
		status, code, message = http.StatusGone, "expired", "Срок подтверждения истёк. Подготовьте новый расчёт"
	case errors.Is(err, store.ErrPostprocessConflict):
		status, code, message = http.StatusConflict, "conflict", "Запрос уже изменён или отменён"
	case errors.Is(err, errPostprocessQuoteInvalid), errors.Is(err, mediaprobe.ErrLimit), errors.Is(err, mediaprobe.ErrInvalid), errors.Is(err, mediaprobe.ErrUnsupported):
		status, code, message = http.StatusUnprocessableEntity, "unsupported", "Параметры или исходный файл не подходят для постобработки"
	}
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

func (a *App) writePostprocessQuoteReceipt(w http.ResponseWriter, ctx context.Context, user *User, receipt domain.GenerationPostprocessReceipt) {
	if user == nil || receipt.UserID != user.ID {
		writePostprocessHTTPError(w, errGenerationRepeatAccess)
		return
	}
	result := map[string]any{"request_id": receipt.RequestID, "state": receipt.State, "accepted": receipt.State == "accepted"}
	if receipt.State == "cancelled" {
		writeJSON(w, http.StatusOK, result)
		return
	}
	q, err := a.decodePostprocessQuote(receipt)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	if receipt.UserID != user.ID || !user.CanUseQuickGenerationType(q.TemplateID) {
		writePostprocessHTTPError(w, errGenerationRepeatAccess)
		return
	}
	if receipt.State == "accepted" {
		// Read only: status/discard must never publish work without confirmation.
		result["job_id"], result["removed"] = receipt.JobPublicID, receipt.JobID == nil
		if receipt.JobID != nil {
			job, err := a.store.GenerationJobByPublicID(ctx, user.ID, receipt.JobPublicID)
			if errors.Is(err, sql.ErrNoRows) {
				result["removed"] = true
			} else if err != nil {
				writePostprocessHTTPError(w, err)
				return
			} else {
				if job.ID != *receipt.JobID || job.RequestID != receipt.RequestID || !isPostprocessJob(job) {
					writePostprocessHTTPError(w, store.ErrPostprocessConflict)
					return
				}
				views, err := a.generationJobViews(ctx, []domain.GenerationJob{job}, user.ID)
				if err != nil {
					writePostprocessHTTPError(w, err)
					return
				}
				result["job"] = views[0]
				result["publication_pending"] = job.State == domain.GenerationJobWaitingForResources && job.DispatchQueuedAt == nil && job.DispatchClosedAt == nil && job.CancellationRequestedAt == nil && job.SubmissionStartedAt == nil && job.ResourcesReleasedAt == nil && job.PromptID == ""
			}
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if receipt.State != "quoted" {
		writePostprocessHTTPError(w, store.ErrPostprocessConflict)
		return
	}
	if !q.ExpiresAt.After(time.Now()) {
		writePostprocessHTTPError(w, store.ErrPostprocessExpired)
		return
	}
	sources := make([]map[string]any, 0, len(q.Sources))
	for _, item := range q.Sources {
		identity := item.Identity
		job, current, err := a.loadPostprocessOutputSource(ctx, user.ID, identity.JobPublicID, identity.MediaID)
		if err != nil {
			writePostprocessHTTPError(w, err)
			return
		}
		if job.ID != identity.JobID || current.SizeBytes != identity.SizeBytes || current.ContentHash != identity.ArchiveSHA256 || current.MediaType != identity.MediaType || current.MIMEType != identity.MIMEType {
			writePostprocessHTTPError(w, store.ErrPostprocessSource)
			return
		}
		sources = append(sources, map[string]any{"job_id": identity.JobPublicID, "media_id": strconv.FormatInt(identity.MediaID, 10), "media_type": identity.MediaType,
			"sha256": identity.SHA256, "size_bytes": identity.SizeBytes, "width": item.Measurement.Width, "height": item.Measurement.Height,
			"frames": item.Measurement.Frames, "fps_numerator": item.Measurement.FPSNumerator, "fps_denominator": item.Measurement.FPSDenominator})
	}
	if !q.ExpiresAt.After(time.Now()) {
		writePostprocessHTTPError(w, store.ErrPostprocessExpired)
		return
	}
	result["confirmation"] = receipt.ConfirmationSHA256
	result["expires_at"], result["cost"], result["plan"], result["sources"] = q.ExpiresAt, q.Cost, q.Plan, sources
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handlePostprocessQuote(w http.ResponseWriter, r *http.Request) {
	v, ok := postprocessHTTPValues(w, r, http.MethodPost, "request_id", "source_job_id", "source_media_id", "reference_job_id", "reference_media_id", "width", "height", "interpolate", "rtx_quality", "rife_checkpoint", "color_method", "color_strength")
	if !ok {
		return
	}
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "Проверка безопасности не пройдена")
		return
	}
	request, err := parsePostprocessQuoteRequest(v)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	user, err := a.generationRepeatUser(r.Context(), r)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	_, err = a.store.GenerationPostprocessByRequest(r.Context(), user.ID, request.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		if !a.allowNewPostprocessQuote(time.Now()) {
			writePostprocessHTTPError(w, errPostprocessSourceBusy)
			return
		}
	} else if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	receipt, _, err := a.createPostprocessQuote(r.Context(), user.ID, request)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	a.writePostprocessQuoteReceipt(w, r.Context(), user, receipt)
}

func (a *App) postprocessReceiptRequest(w http.ResponseWriter, r *http.Request, method string) (*User, string, bool) {
	v, ok := postprocessHTTPValues(w, r, method, "request_id")
	if !ok {
		return nil, "", false
	}
	if method == http.MethodPost && !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "Проверка безопасности не пройдена")
		return nil, "", false
	}
	id := v.Get("request_id")
	if len(id) < 16 || !validGenerationRequestID(id) {
		writePostprocessHTTPError(w, errPostprocessQuoteInvalid)
		return nil, "", false
	}
	user, err := a.generationRepeatUser(r.Context(), r)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return nil, "", false
	}
	return user, id, true
}

func (a *App) handlePostprocessQuoteStatus(w http.ResponseWriter, r *http.Request) {
	user, id, ok := a.postprocessReceiptRequest(w, r, http.MethodGet)
	if !ok {
		return
	}
	receipt, err := a.store.GenerationPostprocessByRequest(r.Context(), user.ID, id)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	a.writePostprocessQuoteReceipt(w, r.Context(), user, receipt)
}

func (a *App) handlePostprocessQuoteDiscard(w http.ResponseWriter, r *http.Request) {
	user, id, ok := a.postprocessReceiptRequest(w, r, http.MethodPost)
	if !ok {
		return
	}
	receipt, err := a.store.DiscardGenerationPostprocess(r.Context(), user.ID, id)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	a.writePostprocessQuoteReceipt(w, r.Context(), user, receipt)
}
