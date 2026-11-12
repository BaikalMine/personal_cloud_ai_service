package store

import (
	"encoding/json"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestInviteTemplateDecoderRejectsUnknownVersionsAndPrivileges(t *testing.T) {
	s := domain.InviteTemplateSettings{Version: 1, Access: domain.AccessSettings{Training: true, MaxVideoQuality: 720}, LinkLifetimeHours: 24, MaxUses: 1}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeInviteTemplate(raw)
	if err != nil || got != s {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	for _, bad := range []string{
		`null`, `[]`, `{}`, string(raw) + `{}`,
		strings.Replace(string(raw), `"version":1`, `"version":2`, 1),
		strings.Replace(string(raw), `"training":true`, `"training":true,"role":"admin"`, 1),
		strings.Replace(string(raw), `"training":true`, `"training":true,"manage_mining":true`, 1),
		strings.Replace(string(raw), `"max_video_quality":720`, `"max_video_quality":2160`, 1),
		strings.Repeat(" ", 8193),
	} {
		if _, err := decodeInviteTemplate([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
