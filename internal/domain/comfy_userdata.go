package domain

import "time"

type ComfyUserDataEntry struct {
	Path       string
	Size       int64
	CreatedAt  time.Time
	ModifiedAt time.Time
}
