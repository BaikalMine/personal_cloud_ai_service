package domain

import (
	"database/sql"
	"testing"
	"time"
)

func TestHarnessOnlyAccess(t *testing.T) {
	settings := AccessSettings{Harness: true, MaxVideoQuality: 720}
	invitation := InviteTemplateSettings{Version: 1, Access: settings, LinkLifetimeHours: 24, MaxUses: 1}
	if err := invitation.Validate(); err != nil {
		t.Fatal(err)
	}
	user := User{Role: "user", CanUseHarness: true}
	if !user.CanUseService("harness") || user.CanUseService("quick_generation") || user.CanUseService("comfyui") || user.CanUseService("openwebui") || user.CanManageMining {
		t.Fatal("Harness grant leaked other access")
	}
	now := time.Now()
	if !user.EffectiveAccess(now).Settings.Harness {
		t.Fatal("Harness grant missing")
	}
	user.Disabled = true
	if user.EffectiveAccess(now).Settings.Harness {
		t.Fatal("disabled user retained Harness")
	}
	user.Disabled = false
	user.AccountExpiresAt = sql.NullTime{Time: now, Valid: true}
	if user.EffectiveAccess(now).Settings.Harness {
		t.Fatal("expired user retained Harness")
	}
}
