package domain

import (
	"fmt"
	"time"
)

// AccessSettings is a value snapshot, not a live link to an invitation template.
// Administrative roles and mining administration can never be granted by it.
type AccessSettings struct {
	ComfyUI          bool  `json:"comfyui"`
	Harness          bool  `json:"harness"`
	OpenWebUI        bool  `json:"openwebui"`
	QuickGeneration  bool  `json:"quick_generation"`
	TextToImage      bool  `json:"text_to_image"`
	ImageToImage     bool  `json:"image_to_image"`
	Video            bool  `json:"video"`
	AdvancedSettings bool  `json:"advanced_settings"`
	Training         bool  `json:"training"`
	PauseMining      bool  `json:"pause_mining"`
	QueuePriority    bool  `json:"queue_priority"`
	ImageDailyLimit  int   `json:"image_daily_limit"`
	ImageTotalLimit  int64 `json:"image_total_limit"`
	VideoDailyLimit  int   `json:"video_daily_limit"`
	VideoTotalLimit  int64 `json:"video_total_limit"`
	MaxVideoQuality  int   `json:"max_video_quality"`
}

func (s AccessSettings) Normalize() AccessSettings {
	if !s.QuickGeneration {
		s.TextToImage, s.ImageToImage, s.Video = false, false, false
		s.AdvancedSettings, s.PauseMining = false, false
	}
	if !s.QuickGeneration && !s.Training {
		s.QueuePriority = false
	}
	return s
}

func (s AccessSettings) Validate() error {
	if s != s.Normalize() {
		return fmt.Errorf("access settings contain inactive capabilities")
	}
	if s.QuickGeneration && !s.TextToImage && !s.ImageToImage && !s.Video {
		return fmt.Errorf("generation requires at least one scenario")
	}
	if s.ImageDailyLimit < 0 || s.ImageDailyLimit > 100000 || s.VideoDailyLimit < 0 || s.VideoDailyLimit > 100000 ||
		s.ImageTotalLimit < 0 || s.ImageTotalLimit > 10000000 || s.VideoTotalLimit < 0 || s.VideoTotalLimit > 10000000 {
		return fmt.Errorf("access limits are outside the supported range")
	}
	switch s.MaxVideoQuality {
	case 480, 720, 1080, 1440:
	default:
		return fmt.Errorf("unsupported base video quality")
	}
	return nil
}

func (u User) AccessSettings() AccessSettings {
	return AccessSettings{
		ComfyUI: u.CanUseComfyUI, OpenWebUI: u.CanUseOpenWebUI,
		Harness:         u.CanUseHarness,
		QuickGeneration: u.CanUseQuickGeneration, TextToImage: u.CanGenerateTextToImage,
		ImageToImage: u.CanGenerateImageToImage, Video: u.CanGenerateVideo,
		AdvancedSettings: u.CanUseAdvancedGenerationSettings, Training: u.CanTrainImageLora,
		PauseMining: u.PauseMiningForQuickGeneration, QueuePriority: u.QueuePriority,
		ImageDailyLimit: u.GenerationDailyLimit, ImageTotalLimit: u.GenerationTotalLimit,
		VideoDailyLimit: u.VideoGenerationDailyLimit, VideoTotalLimit: u.VideoGenerationTotalLimit,
		MaxVideoQuality: u.MaxVideoGenerationQuality,
	}
}

func (i InviteAccess) AccessSettings() AccessSettings {
	return AccessSettings{
		ComfyUI: i.GrantComfyUI, OpenWebUI: i.GrantOpenWebUI,
		Harness:         i.GrantHarness,
		QuickGeneration: i.GrantQuickGeneration, TextToImage: i.GrantTextToImage,
		ImageToImage: i.GrantImageToImage, Video: i.GrantVideo,
		AdvancedSettings: i.GrantAdvancedGenerationSettings, Training: i.GrantTrainImageLora,
		PauseMining: i.PauseMiningForQuickGeneration, QueuePriority: i.QueuePriority,
		ImageDailyLimit: i.GenerationDailyLimit, ImageTotalLimit: i.GenerationTotalLimit,
		VideoDailyLimit: i.VideoGenerationDailyLimit, VideoTotalLimit: i.VideoGenerationTotalLimit,
		MaxVideoQuality: i.MaxVideoGenerationQuality,
	}
}

// EffectiveAccess reports account-wide permissions. Workflow/model policy and
// current resource availability are separate gates and are not implied here.
type EffectiveAccess struct {
	Settings       AccessSettings
	State          string
	LoginLocked    bool
	ImageTotalUsed int64
	VideoTotalUsed int64
}

func (u User) EffectiveAccess(now time.Time) EffectiveAccess {
	result := EffectiveAccess{Settings: u.AccessSettings().Normalize(), State: "active",
		LoginLocked: u.IsLocked(now), ImageTotalUsed: u.GenerationTotalUsed, VideoTotalUsed: u.VideoGenerationTotalUsed}
	switch result.Settings.MaxVideoQuality {
	case 480, 720, 1080, 1440:
	default:
		result.Settings.MaxVideoQuality = 1440
	}
	if u.Role == "admin" {
		result.Settings = AccessSettings{ComfyUI: true, OpenWebUI: true, QuickGeneration: true, Harness: true,
			TextToImage: true, ImageToImage: true, Video: true, AdvancedSettings: true, Training: true,
			PauseMining: u.PauseMiningForQuickGeneration, QueuePriority: u.QueuePriority, MaxVideoQuality: 1440}
	}
	if u.Disabled {
		result.State = "disabled"
	} else if u.AccountExpiresAt.Valid && !u.AccountExpiresAt.Time.After(now) {
		result.State = "expired"
	}
	if result.State != "active" {
		result.Settings.ComfyUI, result.Settings.OpenWebUI = false, false
		result.Settings.Harness = false
		result.Settings.QuickGeneration, result.Settings.Training = false, false
		result.Settings = result.Settings.Normalize()
	}
	return result
}

type InviteTemplateSettings struct {
	Version                int            `json:"version"`
	Access                 AccessSettings `json:"access"`
	LinkLifetimeHours      int            `json:"link_lifetime_hours"`
	AccountLifetimeSeconds int64          `json:"account_lifetime_seconds"`
	MaxUses                int            `json:"max_uses"`
}

func (s InviteTemplateSettings) Validate() error {
	if s.Version != 1 {
		return fmt.Errorf("unsupported invitation template version")
	}
	if err := s.Access.Validate(); err != nil {
		return err
	}
	if !s.Access.ComfyUI && !s.Access.OpenWebUI && !s.Access.QuickGeneration && !s.Access.Training && !s.Access.Harness {
		return fmt.Errorf("invitation requires at least one capability")
	}
	if s.LinkLifetimeHours < 1 || s.LinkLifetimeHours > 2160 || s.MaxUses < 1 || s.MaxUses > 10000 ||
		s.AccountLifetimeSeconds < 0 || s.AccountLifetimeSeconds > 365*24*3600 ||
		(s.AccountLifetimeSeconds != 0 && (s.AccountLifetimeSeconds < 3600 || s.AccountLifetimeSeconds%3600 != 0)) {
		return fmt.Errorf("invalid invitation lifetimes or activation limit")
	}
	return nil
}

type InviteTemplate struct {
	ID        int64
	Name      string
	Revision  int64
	Settings  InviteTemplateSettings
	CreatedAt time.Time
	UpdatedAt time.Time
}
