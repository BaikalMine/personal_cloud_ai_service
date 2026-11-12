package domain

import "time"

type NotificationKind string

const (
	NotificationGenerationCompleted    NotificationKind = "generation_completed"
	NotificationGenerationFailed       NotificationKind = "generation_failed"
	NotificationTrainingCompleted      NotificationKind = "lora_training_completed"
	NotificationTrainingFailed         NotificationKind = "lora_training_failed"
	NotificationCaptionSeriesCompleted NotificationKind = "lora_caption_series_completed"
	NotificationCaptionSeriesFailed    NotificationKind = "lora_caption_series_failed"
	NotificationCaptionSeriesCancelled NotificationKind = "lora_caption_series_cancelled"
)

func (kind NotificationKind) Valid() bool {
	switch kind {
	case NotificationGenerationCompleted, NotificationGenerationFailed, NotificationTrainingCompleted, NotificationTrainingFailed,
		NotificationCaptionSeriesCompleted, NotificationCaptionSeriesFailed, NotificationCaptionSeriesCancelled:
		return true
	}
	return false
}

type UserNotificationPreferences struct {
	InAppEnabled   bool
	SuccessEnabled bool
	BrowserEnabled bool
	UpdatedAt      time.Time
}

type UserNotification struct {
	ID                    int64
	UserID                int64
	GenerationJobID       int64
	GenerationJobPublicID string
	SourceKind            string
	SourceID              string
	Kind                  NotificationKind
	Title                 string
	Message               string
	Href                  string
	ReadAt                *time.Time
	CreatedAt             time.Time
}

type UserNotificationSummary struct {
	Revision    int64
	UnreadCount int
	ActiveCount int
	Preferences UserNotificationPreferences
}
