package gateway

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mediaprobe"
)

var (
	errPostprocessSourceBusy    = errors.New("postprocess source decoder is busy")
	errPostprocessSourceChanged = errors.New("postprocess source changed")
)

type preparedPostprocessSource struct {
	UserID      int64
	ParentJob   domain.GenerationJob
	Source      domain.GenerationOutputSource
	Measurement mediaprobe.DecodedMeasurement
	Facts       postprocessSourceFacts
	Media       *materializedContentMedia
}

func (source *preparedPostprocessSource) Close() error {
	if source == nil {
		return nil
	}
	return source.Media.Close()
}

func (a *App) loadPostprocessOutputSource(ctx context.Context, userID int64, jobID string, mediaID int64) (domain.GenerationJob, domain.GenerationOutputSource, error) {
	if a == nil || a.store == nil || a.contentCipher == nil || userID <= 0 || mediaID <= 0 || !validGenerationRequestID(jobID) {
		return domain.GenerationJob{}, domain.GenerationOutputSource{}, sql.ErrNoRows
	}
	source, err := a.loadGenerationOutputSource(ctx, userID, jobID, mediaID)
	if err != nil {
		return domain.GenerationJob{}, source, err
	}
	job, err := a.store.GenerationJobByPublicID(ctx, userID, jobID)
	if err != nil {
		return job, source, err
	}
	if job.State != domain.GenerationJobCompleted {
		return job, source, sql.ErrNoRows
	}
	fresh, err := a.store.UserByID(ctx, userID)
	if err != nil {
		return job, source, err
	}
	if fresh.Disabled || fresh.AccountExpiresAt.Valid && !fresh.AccountExpiresAt.Time.After(time.Now()) || !fresh.CanUseService("quick_generation") {
		return job, source, sql.ErrNoRows
	}
	if source.MediaType == "video" && !fresh.CanUseQuickGenerationType("minimax-h3-video") || source.MediaType == "image" && !fresh.CanUseQuickGenerationType("text-to-image") && !fresh.CanUseQuickGenerationType("image-to-image") {
		return job, source, sql.ErrNoRows
	}
	return job, source, nil
}

// preparePostprocessSource returns a private, decoded snapshot of this exact
// output, never the latest output for a prompt. The caller must Close it. A
// receipt/dispatcher must still recheck access and hash before actual upload/send.
func (a *App) preparePostprocessSource(ctx context.Context, userID int64, jobID string, mediaID int64) (result *preparedPostprocessSource, err error) {
	return a.preparePostprocessSourceWithDecoder(ctx, userID, jobID, mediaID, mediaprobe.Decode)
}

func (a *App) preparePostprocessSourceWithDecoder(ctx context.Context, userID int64, jobID string, mediaID int64, decode func(context.Context, string, string) (mediaprobe.DecodedMeasurement, error)) (result *preparedPostprocessSource, err error) {
	job, source, err := a.loadPostprocessOutputSource(ctx, userID, jobID, mediaID)
	if err != nil {
		return nil, err
	}
	if source.SizeBytes <= 0 || source.SizeBytes > mediaprobe.MaxInputBytes {
		return nil, mediaprobe.ErrLimit
	}
	a.postprocessDecodeOnce.Do(func() { a.postprocessDecodeSlots = make(chan struct{}, 1) })
	select {
	case a.postprocessDecodeSlots <- struct{}{}:
		defer func() { <-a.postprocessDecodeSlots }()
	default:
		return nil, errPostprocessSourceBusy
	}
	media, err := a.store.ContentMediaByIDForUser(ctx, mediaID, userID)
	if err != nil {
		return nil, err
	}
	if media.SizeBytes != source.SizeBytes || media.MIMEType != source.MIMEType || media.MediaType != source.MediaType {
		return nil, errPostprocessSourceChanged
	}
	materialized, err := a.materializeContentMedia(ctx, media)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = materialized.Close()
		}
	}()
	decoded, err := decode(ctx, materialized.path, source.MIMEType)
	if err != nil {
		return nil, err
	}
	if decoded.DecodeVersion != mediaprobe.FullDecodeVersion || decoded.Source != mediaprobe.FullDecodeSource || !decoded.Opaque || !decoded.NormalOrientation || !decoded.SquarePixels || decoded.SizeBytes != source.SizeBytes || decoded.MediaType != source.MediaType || source.ContentHash != "" && source.ContentHash != decoded.SHA256 {
		return nil, errPostprocessSourceChanged
	}
	currentJob, current, err := a.loadPostprocessOutputSource(ctx, userID, jobID, mediaID)
	if err != nil {
		return nil, err
	}
	if currentJob.ID != job.ID || current.SizeBytes != source.SizeBytes || current.ContentHash != source.ContentHash || current.MIMEType != source.MIMEType || current.MediaType != source.MediaType {
		return nil, errPostprocessSourceChanged
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	facts := postprocessSourceFacts{
		MediaType: decoded.MediaType, Width: decoded.Width, Height: decoded.Height, Frames: decoded.Frames,
		FPSNumerator: decoded.FPSNumerator, FPSDenominator: decoded.FPSDenominator,
		ConstantFrameRate: decoded.UniformCadence, SquarePixels: decoded.SquarePixels,
		OrientationNormal: decoded.NormalOrientation, Opaque: decoded.Opaque, DecodeVerified: true,
	}
	return &preparedPostprocessSource{UserID: userID, ParentJob: currentJob, Source: current, Measurement: decoded, Facts: facts, Media: materialized}, nil
}
