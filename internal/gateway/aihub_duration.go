package gateway

import (
	"context"
	"errors"
	"math"
	"strconv"

	"ai-access-gateway/internal/mediaprobe"
)

func aiHubReferenceFrames(seconds float64) (int, error) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 5.0/24 {
		return 0, errors.New("AIHUB: не удалось определить длину видео или оно короче 5 кадров")
	}
	if seconds > 30 {
		return 0, errors.New("AIHUB: режим длины по референсу поддерживает видео до 30 секунд; обрежьте исходник или задайте длительность вручную")
	}
	return int(math.Round(seconds * 24)), nil
}

func probeAIHubReferenceDuration(ctx context.Context, filename, mime string) (float64, error) {
	measurement, err := mediaprobe.Probe(ctx, filename, mime)
	if err != nil {
		return 0, errors.New("не удалось определить длительность видеореференса; проверьте файл или задайте длительность вручную")
	}
	seconds, err := strconv.ParseFloat(measurement.DurationSeconds, 64)
	if err != nil {
		return 0, errors.New("видеореференс не содержит корректной длительности")
	}
	if _, err := aiHubReferenceFrames(seconds); err != nil {
		return 0, err
	}
	return seconds, nil
}

func (a *App) resolveAIHubDuration(ctx context.Context, userID int64, input *generationForm) error {
	input.AIHubOutputFrames = 0
	if !input.AIHubMatchDuration {
		return nil
	}
	if input.AIHubMode != "video" {
		return errors.New("длина видеореференса доступна только в режиме видео по видео")
	}
	if err := a.validateGenerationVideo(input.InputVideo, userID); err != nil {
		return err
	}
	return a.withInputVideo(ctx, input.InputVideo, func(ctx context.Context, filename, mime string) error {
		seconds, err := probeAIHubReferenceDuration(ctx, filename, mime)
		if err != nil {
			return err
		}
		input.AIHubOutputFrames, err = aiHubReferenceFrames(seconds)
		input.VideoDurationSeconds = max(5, int(math.Ceil(seconds)))
		input.VideoReferenceStart = 0
		return err
	})
}
