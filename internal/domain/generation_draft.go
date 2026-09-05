package domain

import "time"

type GenerationDraftRow struct {
	UserID        int64
	Revision      int64
	PayloadCipher []byte
	UpdatedAt     time.Time
	ExpiresAt     time.Time
}

type OwnedComfyInputAsset struct {
	ID        string
	Filename  string
	Subfolder string
	SizeBytes int64
	ExpiresAt time.Time
}
