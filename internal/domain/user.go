package domain

import (
	"database/sql"
	"time"
)

type User struct {
	ID                               int64
	Username                         string
	Email                            sql.NullString
	Role                             string
	Disabled                         bool
	CanUseComfyUI                    bool
	CanUseOpenWebUI                  bool
	CanUseQuickGeneration            bool
	CanUseHarness                    bool
	CanGenerateTextToImage           bool
	CanGenerateImageToImage          bool
	CanGenerateVideo                 bool
	CanUseAdvancedGenerationSettings bool
	CanTrainImageLora                bool
	CanManageMining                  bool
	PauseMiningForQuickGeneration    bool
	QueuePriority                    bool
	GenerationDailyLimit             int
	GenerationTotalLimit             int64
	GenerationTotalUsed              int64
	VideoGenerationDailyLimit        int
	VideoGenerationTotalLimit        int64
	VideoGenerationTotalUsed         int64
	MaxVideoGenerationQuality        int
	FailedLoginCount                 int
	LockedUntil                      sql.NullTime
	AccountExpiresAt                 sql.NullTime
	CreatedAt                        time.Time
	LastLoginAt                      sql.NullTime
}

func (u User) CanUseQuickGenerationType(templateID string) bool {
	if u.Role == "admin" {
		return true
	}
	if !u.CanUseQuickGeneration {
		return false
	}
	switch templateID {
	case "text-to-image":
		return u.CanGenerateTextToImage
	case "image-to-image":
		return u.CanGenerateImageToImage
	case "minimax-h3-video", PostprocessVideoTemplate:
		return u.CanGenerateVideo
	case PostprocessImageTemplate:
		return u.CanGenerateTextToImage || u.CanGenerateImageToImage
	default:
		return false
	}
}

func (u User) CanAccessMining() bool {
	return u.Role == "admin" || u.CanManageMining
}

func (u User) IsLocked(now time.Time) bool {
	return u.LockedUntil.Valid && u.LockedUntil.Time.After(now)
}

func (u User) CanUseService(service string) bool {
	if u.Role == "admin" {
		return true
	}
	switch service {
	case "quick_generation":
		return u.CanUseQuickGeneration
	case "harness":
		return u.CanUseHarness
	case "comfyui", "openwebui":
		// Raw service interfaces are administrative, regardless of legacy grants.
		return false
	default:
		return false
	}
}

type UserRow struct {
	ID                               int64
	Username                         string
	Email                            string
	Role                             string
	Disabled                         bool
	CanUseComfyUI                    bool
	CanUseOpenWebUI                  bool
	CanUseQuickGeneration            bool
	CanUseHarness                    bool
	CanGenerateTextToImage           bool
	CanGenerateImageToImage          bool
	CanGenerateVideo                 bool
	CanUseAdvancedGenerationSettings bool
	CanTrainImageLora                bool
	CanManageMining                  bool
	PauseMiningForQuickGeneration    bool
	QueuePriority                    bool
	GenerationDailyLimit             int
	GenerationTotalLimit             int64
	GenerationTotalUsed              int64
	VideoGenerationDailyLimit        int
	VideoGenerationTotalLimit        int64
	VideoGenerationTotalUsed         int64
	MaxVideoGenerationQuality        int
	FailedLoginCount                 int
	LockedUntil                      sql.NullTime
	AccountExpiresAt                 sql.NullTime
	Locked                           bool
	CreatedAt                        time.Time
	LastLoginAt                      sql.NullTime
	Requests                         int64
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
