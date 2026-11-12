package gateway

import (
	"fmt"
	"net/http"

	"ai-access-gateway/internal/domain"
)

func parseAccessSettings(r *http.Request, invitation bool) (domain.AccessSettings, error) {
	keys := []string{"can_use_quick_generation", "can_generate_text_to_image", "can_generate_image_to_image", "can_generate_video", "can_use_advanced_generation_settings", "can_train_image_lora"}
	if invitation {
		keys = []string{"grant_quick_generation", "grant_text_to_image", "grant_image_to_image", "grant_video", "grant_advanced_generation_settings", "grant_train_image_lora"}
	}
	on := func(index int) bool { return r.Form.Get(keys[index]) == "on" }
	s := domain.AccessSettings{QuickGeneration: on(0), TextToImage: on(1), ImageToImage: on(2), Video: on(3), AdvancedSettings: on(4), Training: on(5), PauseMining: r.Form.Get("pause_mining_for_quick_generation") == "on"}
	field := "can_use_harness"
	if invitation {
		field = "grant_harness"
	}
	s.Harness = r.Form.Get(field) == "on"
	if s.QuickGeneration && !s.TextToImage && !s.ImageToImage && !s.Video {
		if invitation || r.Form.Get("limit_form_version") == "2" {
			return s, fmt.Errorf("Для быстрой генерации выберите хотя бы один сценарий.")
		}
		s.QuickGeneration = false
	}
	s = s.Normalize()
	s.QueuePriority = parseQueuePriority(r, s.QuickGeneration || s.Training, s.PauseMining)
	var err error
	s.ImageDailyLimit, s.ImageTotalLimit, err = parseGenerationLimits(r, "image_generation", "изображений")
	if err != nil {
		return s, err
	}
	s.VideoDailyLimit, s.VideoTotalLimit, err = parseGenerationLimits(r, "video_generation", "видео")
	if err != nil {
		return s, err
	}
	s.MaxVideoQuality, err = parseMaxVideoGenerationQuality(r)
	if err != nil {
		return s, err
	}
	if invitation && !s.QuickGeneration && !s.Training && !s.Harness {
		return s, fmt.Errorf("Выберите хотя бы один тип доступа.")
	}
	return s, s.Validate()
}

func accessAuditDetails(s domain.AccessSettings) map[string]any {
	return map[string]any{
		"harness": s.Harness,
		"comfyui": s.ComfyUI, "openwebui": s.OpenWebUI, "quick_generation": s.QuickGeneration,
		"text_to_image": s.TextToImage, "image_to_image": s.ImageToImage, "video": s.Video,
		"advanced_generation_settings": s.AdvancedSettings, "train_image_lora": s.Training,
		"pause_mining_for_quick_generation": s.PauseMining, "queue_priority": s.QueuePriority,
		"image_generation_daily_limit": s.ImageDailyLimit, "image_generation_total_limit": s.ImageTotalLimit,
		"video_generation_daily_limit": s.VideoDailyLimit, "video_generation_total_limit": s.VideoTotalLimit,
		"max_video_generation_quality": s.MaxVideoQuality,
	}
}
