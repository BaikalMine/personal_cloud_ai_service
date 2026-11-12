package domain

import (
	"database/sql"
	"testing"
	"time"
)

func TestAccessSettingsValidation(t *testing.T) {
	base := AccessSettings{QuickGeneration: true, TextToImage: true, MaxVideoQuality: 720}
	for _, tt := range []struct {
		name  string
		edit  func(*AccessSettings)
		valid bool
	}{
		{"photo", func(*AccessSettings) {}, true},
		{"separate limits", func(s *AccessSettings) {
			s.ImageDailyLimit = 12
			s.ImageTotalLimit = 50
			s.VideoDailyLimit = 2
			s.VideoTotalLimit = 8
		}, true},
		{"training independent", func(s *AccessSettings) {
			s.QuickGeneration = false
			s.TextToImage = false
			s.Training = true
			s.QueuePriority = true
		}, true},
		{"no scenario", func(s *AccessSettings) { s.TextToImage = false }, false},
		{"inactive scenario", func(s *AccessSettings) { s.QuickGeneration = false }, false},
		{"daily overflow", func(s *AccessSettings) { s.ImageDailyLimit = 100001 }, false},
		{"negative total", func(s *AccessSettings) { s.VideoTotalLimit = -1 }, false},
		{"quality", func(s *AccessSettings) { s.MaxVideoQuality = 2160 }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := base
			tt.edit(&s)
			if err := s.Validate(); (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}

func TestEffectiveAccessUsesRuntimePermissionSemantics(t *testing.T) {
	now := time.Now()
	for mask := 0; mask < 32; mask++ {
		u := User{CanUseQuickGeneration: mask&1 != 0, CanGenerateTextToImage: mask&2 != 0, CanGenerateImageToImage: mask&4 != 0, CanGenerateVideo: mask&8 != 0, CanTrainImageLora: mask&16 != 0, MaxVideoGenerationQuality: 720}
		s := u.EffectiveAccess(now).Settings
		if s.TextToImage != u.CanUseQuickGenerationType("text-to-image") || s.ImageToImage != u.CanUseQuickGenerationType("image-to-image") || s.Video != u.CanUseQuickGenerationType("minimax-h3-video") || s.Training != u.CanTrainImageLora {
			t.Fatalf("permission mismatch for mask %d: %+v", mask, s)
		}
	}
	u := User{Role: "admin", QueuePriority: false, PauseMiningForQuickGeneration: true, GenerationDailyLimit: 1, MaxVideoGenerationQuality: 480}
	a := u.EffectiveAccess(now)
	if !a.Settings.Video || !a.Settings.Training || a.Settings.QueuePriority || !a.Settings.PauseMining || a.Settings.ImageDailyLimit != 0 || a.Settings.MaxVideoQuality != 1440 {
		t.Fatalf("admin: %+v", a)
	}
	u.Disabled = true
	if got := u.EffectiveAccess(now); got.State != "disabled" || got.Settings.Video || got.Settings.ComfyUI {
		t.Fatalf("disabled: %+v", got)
	}
	u.Disabled = false
	u.AccountExpiresAt = sql.NullTime{Time: now, Valid: true}
	if got := u.EffectiveAccess(now); got.State != "expired" || got.Settings.Training {
		t.Fatalf("expired: %+v", got)
	}
	u.AccountExpiresAt.Valid = false
	u.LockedUntil = sql.NullTime{Time: now.Add(time.Minute), Valid: true}
	if got := u.EffectiveAccess(now); !got.LoginLocked || !got.Settings.Training {
		t.Fatalf("login lock is not service revocation: %+v", got)
	}
}

func TestInviteTemplateLifetimesRemainSeparate(t *testing.T) {
	s := InviteTemplateSettings{Version: 1, Access: AccessSettings{Training: true, MaxVideoQuality: 720}, LinkLifetimeHours: 1, AccountLifetimeSeconds: 365 * 24 * 3600, MaxUses: 1}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*InviteTemplateSettings){
		func(s *InviteTemplateSettings) { s.Version = 2 }, func(s *InviteTemplateSettings) { s.LinkLifetimeHours = 0 },
		func(s *InviteTemplateSettings) { s.LinkLifetimeHours = 2161 }, func(s *InviteTemplateSettings) { s.AccountLifetimeSeconds = 3599 },
		func(s *InviteTemplateSettings) { s.AccountLifetimeSeconds = 3601 }, func(s *InviteTemplateSettings) { s.MaxUses = 10001 },
		func(s *InviteTemplateSettings) { s.Access.Training = false },
	} {
		copy := s
		edit(&copy)
		if copy.Validate() == nil {
			t.Fatalf("accepted invalid settings: %+v", copy)
		}
	}
}
