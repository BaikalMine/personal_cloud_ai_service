package domain

import "time"

type InviteAccess struct {
	ID                   int64
	GrantComfyUI         bool
	GrantOpenWebUI       bool
	GrantQuickGeneration bool
	GrantTextToImage     bool
	GrantImageToImage    bool
	GrantVideo           bool
	GenerationDailyLimit int
	GenerationTotalLimit int64
}

type InviteRow struct {
	ID                   int64
	CreatedBy            string
	MaxUses              int
	UsedCount            int
	ExpiresAt            time.Time
	Revoked              bool
	GrantComfyUI         bool
	GrantOpenWebUI       bool
	GrantQuickGeneration bool
	GrantTextToImage     bool
	GrantImageToImage    bool
	GrantVideo           bool
	GenerationDailyLimit int
	GenerationTotalLimit int64
	Status               string
	CreatedAt            time.Time
}
