package domain

import (
	"database/sql"
	"testing"
	"time"
)

func TestUserCanUseService(t *testing.T) {
	user := User{CanUseComfyUI: true, CanUseOpenWebUI: false, CanUseQuickGeneration: true}
	if !user.CanUseService("comfyui") {
		t.Fatal("expected ComfyUI access")
	}
	if !user.CanUseService("quick_generation") {
		t.Fatal("expected quick generation access")
	}
	if user.CanUseService("openwebui") || user.CanUseService("unknown") {
		t.Fatal("unexpected service access")
	}
	admin := User{Role: "admin"}
	if !admin.CanUseService("comfyui") || !admin.CanUseService("openwebui") || !admin.CanUseService("quick_generation") {
		t.Fatal("administrator must have access to known services")
	}
}

func TestUserCanUseQuickGenerationType(t *testing.T) {
	user := User{CanUseQuickGeneration: true, CanGenerateTextToImage: true}
	if !user.CanUseQuickGenerationType("text-to-image") {
		t.Fatal("text-to-image must be allowed")
	}
	if user.CanUseQuickGenerationType("image-to-image") || user.CanUseQuickGenerationType("minimax-h3-video") {
		t.Fatal("disabled quick generation types must stay forbidden")
	}
	admin := User{Role: "admin"}
	for _, templateID := range []string{"text-to-image", "image-to-image", "minimax-h3-video"} {
		if !admin.CanUseQuickGenerationType(templateID) {
			t.Fatalf("administrator must access %s", templateID)
		}
	}
}

func TestUserIsLocked(t *testing.T) {
	now := time.Now()
	if (User{}).IsLocked(now) {
		t.Fatal("user without lock timestamp must not be locked")
	}
	locked := User{LockedUntil: sql.NullTime{Time: now.Add(time.Minute), Valid: true}}
	if !locked.IsLocked(now) {
		t.Fatal("future lock timestamp must lock the user")
	}
	locked.LockedUntil.Time = now.Add(-time.Second)
	if locked.IsLocked(now) {
		t.Fatal("expired lock timestamp must not lock the user")
	}
}
