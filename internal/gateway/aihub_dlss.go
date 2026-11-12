package gateway

import (
	"errors"
	"math"
)

func validateAIHubDLSS(input generationForm) error {
	if err := validateAIHubFG(input); err != nil {
		return err
	}
	if !input.AIHubDLSSEnabled {
		return nil
	}
	if math.IsNaN(input.AIHubDLSSStrength) || math.IsInf(input.AIHubDLSSStrength, 0) || input.AIHubDLSSStrength < 0.1 || input.AIHubDLSSStrength > 1 {
		return errors.New("DLSS: сила эффекта должна быть от 0,1 до 1")
	}
	w, h := aiHubBaseDimensions(input)
	scale := 1.0
	frames := 5 + int(math.Ceil(float64(input.VideoDurationSeconds*24-5)/17))*17
	if input.AIHubMatchDuration {
		frames = input.AIHubOutputFrames
	}
	if input.AIHubFGEnabled && input.AIHubMode == "carousel" {
		frames *= input.AIHubFGMultiplier
	}
	if input.AIHubMode == "carousel" {
		scale = input.AIHubLatentScale
		frames = max(1, (input.AIHubPhotoFrames-input.AIHubPhotoSkip+max(1, input.AIHubPhotoStride)-1)/max(1, input.AIHubPhotoStride))
	}
	if input.VideoRTXEnabled {
		scale *= input.VideoRTXScale
	}
	if float64(min(w, h))*scale < 64 || float64(max(w, h))*scale > 7680 || float64(min(w, h))*scale > 4320 {
		return errors.New("DLSS: неподдерживаемый размер кадра; выберите меньший размер или отключите DLSS")
	}
	if input.AIHubMode != "carousel" {
		if float64(w)*float64(h)*float64(frames) > 1_000_000_000 {
			return errors.New("DLSS: исходное видео слишком велико для декодирования; уменьшите исходное разрешение или длительность")
		}
		return nil
	}
	// The native node materializes a float32 RGB output batch (~6 GiB at this limit).
	if float64(w)*float64(h)*scale*scale*float64(frames) > 512_000_000 {
		return errors.New("DLSS: слишком большой пакет кадров; уменьшите разрешение или длительность")
	}
	return nil
}

func aiHubDLSSSettings(strength float64, carousel bool) map[string]any {
	settings := map[string]any{
		"upscaling_mode": "1x (DLAA / native)", "nr_preset": "Default", "nr_style": "Default",
		"nr_intensity": strength, "local_tone_strength": 1.0, "local_structure_strength": 0.5,
		"skin_structure_strength": 0.0, "automatic_mask": true, "dlss_model_preset": "Default",
		"motion": "auto", "scene_change_threshold": 0.15, "warmup_frames": 8, "runtime_dir": "",
	}
	if carousel {
		settings["nr_style"], settings["nr_intensity"] = "Natural", 1.15*strength
		settings["local_tone_strength"], settings["local_structure_strength"], settings["skin_structure_strength"] = 0.75, 0.7, 0.4
		settings["motion"], settings["scene_change_threshold"], settings["warmup_frames"] = "none", 0.24, 0
	}
	return settings
}
