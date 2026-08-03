package domain

import (
	"database/sql"
	"time"
)

type User struct {
	ID               int64
	Username         string
	Email            sql.NullString
	Role             string
	Disabled         bool
	CanUseComfyUI    bool
	CanUseOpenWebUI  bool
	FailedLoginCount int
	LockedUntil      sql.NullTime
	CreatedAt        time.Time
	LastLoginAt      sql.NullTime
}

func (u User) IsLocked(now time.Time) bool {
	return u.LockedUntil.Valid && u.LockedUntil.Time.After(now)
}

func (u User) CanUseService(service string) bool {
	if u.Role == "admin" {
		return true
	}
	switch service {
	case "comfyui":
		return u.CanUseComfyUI
	case "openwebui":
		return u.CanUseOpenWebUI
	default:
		return false
	}
}

type UserRow struct {
	ID               int64
	Username         string
	Email            string
	Role             string
	Disabled         bool
	CanUseComfyUI    bool
	CanUseOpenWebUI  bool
	FailedLoginCount int
	LockedUntil      sql.NullTime
	Locked           bool
	CreatedAt        time.Time
	LastLoginAt      sql.NullTime
	Requests         int64
}

type AccountSession struct {
	ID         int64
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	IP         string
	UserAgent  string
	Current    bool
}
