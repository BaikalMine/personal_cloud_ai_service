package gateway

import (
	"errors"
	"math"
)

func validateAIHubFG(input generationForm) error {
	if !input.AIHubFGEnabled {
		return nil
	}
	if !input.isAIHub() || !miniMaxH3Allowed(input.AIHubMode, "photo", "video") {
		return errors.New("DLSS FG доступен только для видео AIHUB")
	}
	if input.AIHubFGMultiplier < 2 || input.AIHubFGMultiplier > 4 {
		return errors.New("DLSS FG: выберите 48, 72 или 96 FPS")
	}
	frames := 5 + int(math.Ceil(float64(input.VideoDurationSeconds*24-5)/17))*17
	if input.AIHubMatchDuration {
		frames = input.AIHubOutputFrames
		// Preparation resolves owned reference metadata after normalization.
		// The graph builder rejects an unresolved count before dispatch.
		if frames == 0 {
			return nil
		}
		if frames < 5 || frames > 720 {
			return errors.New("DLSS FG: сначала определите длительность видеореференса")
		}
	}
	w, h := aiHubBaseDimensions(input)
	scale := 1.0
	if input.VideoRTXEnabled {
		scale = input.VideoRTXScale
	}
	// The generated source still exists as IMAGE, but interpolated output is streamed.
	if float64(w)*float64(h)*float64(frames) > 1_000_000_000 {
		return errors.New("DLSS: исходное видео слишком велико для декодирования; уменьшите исходное разрешение или длительность")
	}
	if math.IsNaN(scale) || math.IsInf(scale, 0) || scale < 1 || float64(w)*float64(h)*scale*scale > 16_777_216 {
		return errors.New("DLSS: итоговый кадр слишком велик; уменьшите разрешение или масштаб RTX")
	}
	return nil
}
