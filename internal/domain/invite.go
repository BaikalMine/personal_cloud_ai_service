package domain

import "time"

type InviteAccess struct {
	ID             int64
	GrantComfyUI   bool
	GrantOpenWebUI bool
}

type InviteRow struct {
	ID             int64
	CreatedBy      string
	MaxUses        int
	UsedCount      int
	ExpiresAt      time.Time
	Revoked        bool
	GrantComfyUI   bool
	GrantOpenWebUI bool
	Status         string
	CreatedAt      time.Time
}
