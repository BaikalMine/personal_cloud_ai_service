package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestParseInviteAccountLifetime(t *testing.T) {
	tests := []struct {
		name  string
		form  url.Values
		want  int64
		valid bool
	}{
		{name: "permanent", form: url.Values{"account_type": {"permanent"}}, want: 0, valid: true},
		{name: "legacy permanent", form: url.Values{}, want: 0, valid: true},
		{name: "temporary one hour", form: url.Values{"account_type": {"temporary"}, "temporary_account_hours": {"1"}}, want: 60 * 60, valid: true},
		{name: "temporary week", form: url.Values{"account_type": {"temporary"}, "temporary_account_hours": {"168"}}, want: 7 * 24 * 60 * 60, valid: true},
		{name: "legacy temporary week", form: url.Values{"account_type": {"temporary"}, "temporary_account_days": {"7"}}, want: 7 * 24 * 60 * 60, valid: true},
		{name: "temporary missing duration", form: url.Values{"account_type": {"temporary"}}, valid: false},
		{name: "temporary excessive duration", form: url.Values{"account_type": {"temporary"}, "temporary_account_hours": {"8761"}}, valid: false},
		{name: "unknown account type", form: url.Values{"account_type": {"shared"}}, valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/admin/invites", nil)
			r.Form = tt.form
			got, err := parseInviteAccountLifetime(r)
			if (err == nil) != tt.valid {
				t.Fatalf("parseInviteAccountLifetime() error = %v, valid = %v", err, tt.valid)
			}
			if err == nil && got != tt.want {
				t.Fatalf("parseInviteAccountLifetime() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseInviteExpiryDuration(t *testing.T) {
	for _, rawHours := range []string{"1", "24", "2160"} {
		t.Run(rawHours, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/admin/invites", nil)
			r.Form = url.Values{"invite_ttl_hours": {rawHours}}
			if _, err := parseInviteExpiry(r); err != nil {
				t.Fatalf("parseInviteExpiry() error = %v", err)
			}
		})
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/invites", nil)
	r.Form = url.Values{"invite_ttl_hours": {"0"}}
	if _, err := parseInviteExpiry(r); err == nil {
		t.Fatal("parseInviteExpiry() accepted a zero-hour lifetime")
	}
}

func TestParseGenerationLimits(t *testing.T) {
	tests := []struct {
		name      string
		form      url.Values
		wantDaily int
		wantTotal int64
		valid     bool
	}{
		{name: "blank means unlimited", form: url.Values{}, valid: true},
		{name: "separate image limits", form: url.Values{"image_generation_daily_limit": {"12"}, "image_generation_total_limit": {"50"}}, wantDaily: 12, wantTotal: 50, valid: true},
		{name: "zero means unlimited", form: url.Values{"image_generation_daily_limit": {"0"}, "image_generation_total_limit": {"0"}}, valid: true},
		{name: "negative daily", form: url.Values{"image_generation_daily_limit": {"-1"}}, valid: false},
		{name: "invalid total", form: url.Values{"image_generation_total_limit": {"many"}}, valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/admin/invites", nil)
			r.Form = tt.form
			daily, total, err := parseGenerationLimits(r, "image_generation", "изображений")
			if (err == nil) != tt.valid {
				t.Fatalf("parseGenerationLimits() error = %v, valid = %v", err, tt.valid)
			}
			if err == nil && (daily != tt.wantDaily || total != tt.wantTotal) {
				t.Fatalf("parseGenerationLimits() = (%d, %d), want (%d, %d)", daily, total, tt.wantDaily, tt.wantTotal)
			}
		})
	}
}

func TestParseMaxVideoGenerationQuality(t *testing.T) {
	for _, quality := range []string{"480", "720", "1080", "1440"} {
		t.Run(quality, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/admin/invites", nil)
			r.Form = url.Values{"max_video_generation_quality": {quality}}
			got, err := parseMaxVideoGenerationQuality(r)
			if err != nil || got != mustAtoi(t, quality) {
				t.Fatalf("parseMaxVideoGenerationQuality() = %d, %v", got, err)
			}
		})
	}
	for _, quality := range []string{"360", "721", "2160", "invalid"} {
		t.Run("reject "+quality, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/admin/invites", nil)
			r.Form = url.Values{"max_video_generation_quality": {quality}}
			if _, err := parseMaxVideoGenerationQuality(r); err == nil {
				t.Fatalf("parseMaxVideoGenerationQuality() accepted %q", quality)
			}
		})
	}
}

func mustAtoi(t *testing.T, raw string) int {
	t.Helper()
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
