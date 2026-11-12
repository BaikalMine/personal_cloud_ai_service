package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestParseAccessSettingsShared(t *testing.T) {
	for _, invitation := range []bool{true, false} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Form = url.Values{"queue_policy_version": {"2"}, "queue_priority": {"on"}, "pause_mining_for_quick_generation": {"on"}, "image_generation_daily_limit": {"12"}, "image_generation_total_limit": {"50"}, "video_generation_daily_limit": {"2"}, "video_generation_total_limit": {"8"}, "max_video_generation_quality": {"720"}}
		if invitation {
			r.Form.Set("grant_train_image_lora", "on")
		} else {
			r.Form.Set("can_train_image_lora", "on")
		}
		s, err := parseAccessSettings(r, invitation)
		if err != nil || !s.Training || !s.QueuePriority || s.PauseMining || s.ImageDailyLimit != 12 || s.VideoDailyLimit != 2 || s.ImageTotalLimit != 50 || s.VideoTotalLimit != 8 {
			t.Fatalf("invitation=%v settings=%+v err=%v", invitation, s, err)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Form = url.Values{"can_use_quick_generation": {"on"}, "can_use_advanced_generation_settings": {"on"}}
	s, err := parseAccessSettings(r, false)
	if err != nil || s.QuickGeneration || s.AdvancedSettings {
		t.Fatalf("empty user scenarios: %+v %v", s, err)
	}
	r.Form = url.Values{"grant_quick_generation": {"on"}}
	if _, err := parseAccessSettings(r, true); err == nil {
		t.Fatal("empty invitation scenarios accepted")
	}
}

func TestEffectiveAccessQualityMatchesRuntime(t *testing.T) {
	for _, role := range []string{"user", "admin"} {
		for _, quality := range []int{0, 480, 720, 1080, 1440, 2160} {
			u := &User{Role: role, MaxVideoGenerationQuality: quality}
			if got, want := u.EffectiveAccess(time.Now()).Settings.MaxVideoQuality, maxVideoGenerationQuality(u); got != want {
				t.Fatalf("role=%s quality=%d effective=%d runtime=%d", role, quality, got, want)
			}
		}
	}
}

func TestExplicitUnlimitedLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values url.Values
		valid  bool
		daily  int
		total  int64
	}{
		{"explicit unlimited", url.Values{"image_generation_daily_unlimited": {"on"}, "image_generation_total_unlimited": {"on"}}, true, 0, 0},
		{"bounded", url.Values{"image_generation_daily_limit": {"12"}, "image_generation_total_limit": {"50"}}, true, 12, 50},
		{"mixed", url.Values{"image_generation_daily_unlimited": {"on"}, "image_generation_total_limit": {"50"}}, true, 0, 50},
		{"missing", url.Values{}, false, 0, 0},
		{"zero ambiguous", url.Values{"image_generation_daily_limit": {"0"}, "image_generation_total_limit": {"0"}}, false, 0, 0},
		{"old hidden number", url.Values{"image_generation_daily_unlimited": {"on"}, "image_generation_total_unlimited": {"on"}, "image_generation_daily_limit": {"12"}}, true, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Form = tc.values
			r.Form.Set("limit_form_version", "2")
			daily, total, err := parseGenerationLimits(r, "image_generation", "изображений")
			if (err == nil) != tc.valid || (err == nil && (daily != tc.daily || total != tc.total)) {
				t.Fatalf("%d %d %v", daily, total, err)
			}
		})
	}
}
