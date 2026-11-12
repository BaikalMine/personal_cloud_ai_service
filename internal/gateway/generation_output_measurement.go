package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mediaprobe"
)

func mediaMeasurementAAD(mediaID int64, hash string) []byte {
	return []byte(fmt.Sprintf("gateway:media-measurement:v1:%d:%s", mediaID, hash))
}

func (a *App) decodeMediaMeasurement(source domain.GenerationOutputSource) (mediaprobe.Measurement, error) {
	var measurement mediaprobe.Measurement
	plain, err := a.contentCipher.DecryptBytesWithAAD(source.MeasurementCipher, mediaMeasurementAAD(source.MediaID, source.MeasurementSHA256))
	if err != nil {
		return measurement, err
	}
	defer clear(plain)
	if len(plain) > 8192 {
		return measurement, errors.New("oversized media measurement")
	}
	if err := json.Unmarshal(plain, &measurement); err != nil {
		return measurement, err
	}
	if measurement.Version != 1 || measurement.Source != "completed_file_stream_metadata" || measurement.SHA256 != source.MeasurementSHA256 || measurement.SizeBytes != source.SizeBytes || measurement.MediaType != source.MediaType || measurement.MeasuredAt.IsZero() || measurement.Width <= 0 || measurement.Height <= 0 {
		return mediaprobe.Measurement{}, errors.New("media measurement identity mismatch")
	}
	return measurement, nil
}

func (a *App) generationOutputViews(ctx context.Context, userID int64, jobID string) ([]map[string]any, bool, error) {
	sources, err := a.store.GenerationOutputSources(ctx, userID, jobID, 0)
	if err != nil {
		return nil, false, err
	}
	truncated := len(sources) > 100
	if truncated {
		sources = sources[:100]
	}
	views := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		view := map[string]any{"media_id": strconv.FormatInt(source.MediaID, 10), "media_type": source.MediaType,
			"size_bytes": source.SizeBytes, "measurement_state": "not_recorded", "expires_at": source.ExpiresAt,
			"measurement_url": "/generate/jobs/output-measurement?" + url.Values{"job": {jobID}, "media": {strconv.FormatInt(source.MediaID, 10)}}.Encode()}
		if len(source.MeasurementCipher) > 0 {
			measurement, err := a.decodeMediaMeasurement(source)
			if err != nil {
				view["measurement_state"] = "unavailable"
			} else {
				view["measurement"], view["measurement_state"] = measurement, "measured"
			}
		}
		views = append(views, view)
	}
	return views, truncated, nil
}

func (a *App) loadGenerationOutputSource(ctx context.Context, userID int64, jobID string, mediaID int64) (domain.GenerationOutputSource, error) {
	var source domain.GenerationOutputSource
	user, err := a.store.UserByID(ctx, userID)
	if err != nil {
		return source, err
	}
	if user.Disabled || user.AccountExpiresAt.Valid && !user.AccountExpiresAt.Time.After(time.Now()) {
		return source, sql.ErrNoRows
	}
	job, err := a.store.GenerationJobByPublicID(ctx, userID, jobID)
	if err != nil {
		return source, err
	}
	if !user.CanUseQuickGenerationType(job.TemplateID) {
		return source, sql.ErrNoRows
	}
	sources, err := a.store.GenerationOutputSources(ctx, userID, jobID, mediaID)
	if err != nil {
		return source, err
	}
	if len(sources) != 1 {
		return source, sql.ErrNoRows
	}
	return sources[0], nil
}

func (a *App) produceMediaMeasurement(ctx context.Context, userID int64, source domain.GenerationOutputSource) (mediaprobe.Measurement, error) {
	var measurement mediaprobe.Measurement
	if source.SizeBytes <= 0 || source.SizeBytes > mediaprobe.MaxInputBytes {
		return measurement, mediaprobe.ErrLimit
	}
	media, err := a.store.ContentMediaByIDForUser(ctx, source.MediaID, userID)
	if err != nil {
		return measurement, err
	}
	if media.SizeBytes != source.SizeBytes || media.MIMEType != source.MIMEType || media.MediaType != source.MediaType {
		return measurement, mediaprobe.ErrInvalid
	}
	materialized, err := a.materializeContentMedia(ctx, media)
	if err != nil {
		return measurement, err
	}
	defer materialized.Close()
	measurement, err = mediaprobe.Probe(ctx, materialized.path, source.MIMEType)
	if err != nil {
		return measurement, err
	}
	if measurement.SizeBytes != source.SizeBytes || measurement.MediaType != source.MediaType || source.ContentHash != "" && source.ContentHash != measurement.SHA256 {
		return mediaprobe.Measurement{}, mediaprobe.ErrInvalid
	}
	return measurement, nil
}

func (a *App) handleGenerationOutputMeasurement(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	user := a.currentUser(r)
	if user == nil || user.ID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	mediaID, idErr := strconv.ParseInt(q.Get("media"), 10, 64)
	if err != nil || len(q) != 2 || len(q["media"]) != 1 || len(q["job"]) != 1 || idErr != nil || mediaID <= 0 || strconv.FormatInt(mediaID, 10) != q.Get("media") || !validGenerationRequestID(q.Get("job")) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_output_identity"})
		return
	}
	if a.store == nil || a.contentCipher == nil {
		a.mediaMeasurementError(w, errors.New("unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	jobID := q.Get("job")
	source, err := a.loadGenerationOutputSource(ctx, user.ID, jobID, mediaID)
	if err != nil {
		a.mediaMeasurementError(w, err)
		return
	}
	if len(source.MeasurementCipher) == 0 {
		a.mediaProbeOnce.Do(func() { a.mediaProbeSlots = make(chan struct{}, 2) })
		select {
		case a.mediaProbeSlots <- struct{}{}:
			defer func() { <-a.mediaProbeSlots }()
		default:
			w.Header().Set("Retry-After", "5")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "measurement_busy"})
			return
		}
		measurement, err := a.produceMediaMeasurement(ctx, user.ID, source)
		if err != nil {
			a.mediaMeasurementError(w, err)
			return
		}
		// Recheck access before persisting and again before returning cached data.
		current, err := a.loadGenerationOutputSource(ctx, user.ID, jobID, mediaID)
		if err != nil {
			a.mediaMeasurementError(w, err)
			return
		}
		if current.SizeBytes != source.SizeBytes || current.ContentHash != source.ContentHash || current.MIMEType != source.MIMEType {
			a.mediaMeasurementError(w, sql.ErrNoRows)
			return
		}
		plain, err := json.Marshal(measurement)
		if err != nil {
			a.mediaMeasurementError(w, err)
			return
		}
		cipher, err := a.contentCipher.EncryptBytesWithAAD(plain, mediaMeasurementAAD(mediaID, measurement.SHA256))
		clear(plain)
		if err != nil {
			a.mediaMeasurementError(w, err)
			return
		}
		if err := a.store.SaveMediaMeasurement(ctx, user.ID, jobID, domain.MediaMeasurementRecord{MediaID: mediaID, SHA256: measurement.SHA256, SizeBytes: measurement.SizeBytes, PayloadCipher: cipher}); err != nil {
			a.mediaMeasurementError(w, err)
			return
		}
	}
	source, err = a.loadGenerationOutputSource(ctx, user.ID, jobID, mediaID)
	if err != nil {
		a.mediaMeasurementError(w, err)
		return
	}
	measurement, err := a.decodeMediaMeasurement(source)
	if err != nil {
		a.mediaMeasurementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": 1, "job_id": jobID, "media_id": strconv.FormatInt(mediaID, 10), "measurement_state": "measured", "measurement": measurement})
}

func (a *App) mediaMeasurementError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "measurement_unavailable"
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status, code = http.StatusNotFound, "output_not_found"
	case errors.Is(err, mediaprobe.ErrUnsupported):
		status, code = http.StatusUnprocessableEntity, "measurement_unsupported"
	case errors.Is(err, mediaprobe.ErrInvalid):
		status, code = http.StatusUnprocessableEntity, "measurement_invalid"
	case errors.Is(err, mediaprobe.ErrLimit):
		status, code = http.StatusUnprocessableEntity, "measurement_limit"
	case errors.Is(err, errMediaMemoryBudget):
		status, code = http.StatusTooManyRequests, "measurement_busy"
		w.Header().Set("Retry-After", "5")
	}
	writeJSON(w, status, map[string]string{"error": code})
}
