package domain

import "time"

type InviteAccess struct {
	ID                              int64
	GrantComfyUI                    bool
	GrantOpenWebUI                  bool
	GrantQuickGeneration            bool
	GrantTextToImage                bool
	GrantImageToImage               bool
	GrantVideo                      bool
	GrantAdvancedGenerationSettings bool
	GrantTrainImageLora             bool
	PauseMiningForQuickGeneration   bool
	GenerationDailyLimit            int
	GenerationTotalLimit            int64
	VideoGenerationDailyLimit       int
	VideoGenerationTotalLimit       int64
	MaxVideoGenerationQuality       int
	AccountLifetimeSeconds          int64
}

type InviteRow struct {
	ID                              int64
	CreatedBy                       string
	MaxUses                         int
	UsedCount                       int
	ExpiresAt                       time.Time
	Revoked                         bool
	GrantComfyUI                    bool
	GrantOpenWebUI                  bool
	GrantQuickGeneration            bool
	GrantTextToImage                bool
	GrantImageToImage               bool
	GrantVideo                      bool
	GrantAdvancedGenerationSettings bool
	GrantTrainImageLora             bool
	PauseMiningForQuickGeneration   bool
	GenerationDailyLimit            int
	GenerationTotalLimit            int64
	VideoGenerationDailyLimit       int
	VideoGenerationTotalLimit       int64
	MaxVideoGenerationQuality       int
	AccountLifetimeSeconds          int64
	Status                          string
	CreatedAt                       time.Time
}
