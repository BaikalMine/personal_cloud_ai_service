package gateway

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ai-access-gateway/internal/promptassistant"
)

const miniMaxLongPartSeconds = 15
const miniMaxLongMaximumParts = 20

func normalizeMiniMaxLong(input *generationForm) error {
	if input.VideoLongParts == 0 {
		input.VideoLongParts = 2
	}
	if input.VideoLongContext == 0 {
		input.VideoLongContext = 39
	}
	if input.VideoLongTransition == "" {
		input.VideoLongTransition = "continuous"
	}
	if !input.VideoLongEnabled {
		return nil
	}
	if input.VideoLongParts < 2 || input.VideoLongParts > miniMaxLongMaximumParts {
		return fmt.Errorf("длинное видео MiniMax H3: выберите от 2 до %d частей по 15 секунд", miniMaxLongMaximumParts)
	}
	if input.VideoLongContext != 22 && input.VideoLongContext != 39 {
		return errors.New("контекст продолжения MiniMax H3 должен составлять 22 или 39 кадров")
	}
	if input.VideoLongTransition != "continuous" && input.VideoLongTransition != "cinematic" {
		return errors.New("выберите плавное продолжение или киномонтаж")
	}
	if input.VideoLongTransition == "cinematic" && (strings.Contains(strings.ToUpper(input.Positive), "[STATIC_CAMERA]") || strings.Contains(strings.ToUpper(input.Positive), "[LOCKED_OFF]")) {
		return errors.New("киномонтаж меняет ракурс: уберите запрет смены камеры или выберите плавное продолжение")
	}
	if input.VideoRIFEEnabled || input.VideoRTXEnabled || input.VideoColorMatch || input.VideoSharpenEnabled {
		return errors.New("длинное видео: отключите RIFE, RTX, ColorMatch и резкость; сборка выполняется по частям без загрузки всего ролика в память")
	}
	input.VideoMode = miniMaxH3ReferenceMode
	input.VideoDurationSeconds = miniMaxLongPartSeconds
	input.VideoClearVRAM = false
	return validateMiniMaxLongTimeline(input.Positive, input.VideoLongParts)
}

func validateMiniMaxLongTimeline(prompt string, parts int) error {
	return promptassistant.ValidateLongVideoTimeline(prompt, parts)
}

// Reuse the normal H3 model/LoRA/memory graph, but let the pinned long-video
// sampler own bounded-memory continuation, per-part checkpoints and MP4 assembly.
func buildMiniMaxH3LongPrompt(input generationForm) (map[string]any, error) {
	base, err := buildMiniMaxH3Prompt(input)
	if err != nil {
		return nil, err
	}
	refs := base["7"].(map[string]any)["inputs"].(map[string]any)
	guider := base["13"].(map[string]any)["inputs"].(map[string]any)
	inputs := map[string]any{
		"model": guider["model"], "clip": refs["clip"], "vae": refs["vae"], "audio_vae": refs["audio_vae"],
		"prompt": input.Positive, "width": input.Width, "height": input.Height,
		"length":         input.VideoLongParts * miniMaxLongPartSeconds * miniMaxH3VideoFPS,
		"max_raw_frames": 362, "context_frames": strconv.Itoa(input.VideoLongContext), "noise_seed": input.Seed,
		"sampler": []any{"11", 0}, "sigmas": []any{"12", 0}, "ref_image_size": input.VideoReferenceSize,
		"cache_name": "gateway-minimax-long", "resume": false, "reroll_from_segment": -1, "crf": input.VideoOutputCRF,
		"transition_mode": input.VideoLongTransition,
	}
	for key, value := range refs {
		if strings.HasPrefix(key, "ref_") && strings.Contains(key, ".") {
			inputs[key] = value
		}
	}
	base["long_video"] = miniMaxH3Node("MiniMaxH3LongReferenceSampler", inputs)
	retained := map[string]any{}
	var visit func(string)
	visit = func(id string) {
		if _, ok := retained[id]; ok {
			return
		}
		node, ok := base[id].(map[string]any)
		if !ok {
			return
		}
		retained[id] = node
		for _, value := range node["inputs"].(map[string]any) {
			if link, ok := value.([]any); ok && len(link) == 2 {
				if upstream, ok := link[0].(string); ok {
					visit(upstream)
				}
			}
		}
	}
	visit("long_video")
	return retained, nil
}
