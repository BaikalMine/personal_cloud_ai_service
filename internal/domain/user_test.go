package domain

import (
	"database/sql"
	"testing"
	"time"
)

func TestUserCanUseService(t *testing.T) {
	user := User{CanUseComfyUI: true, CanUseOpenWebUI: false}
	if !user.CanUseService("comfyui") {
		t.Fatal("expected ComfyUI access")
	}
	if user.CanUseService("openwebui") || user.CanUseService("unknown") {
		t.Fatal("unexpected service access")
	}
	admin := User{Role: "admin"}
	if !admin.CanUseService("comfyui") || !admin.CanUseService("openwebui") {
		t.Fatal("administrator must have access to known services")
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
