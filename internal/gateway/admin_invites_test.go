package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
