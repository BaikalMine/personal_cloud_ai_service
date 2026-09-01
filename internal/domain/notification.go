package domain

import "time"

type NotificationKind string

const (
	NotificationGenerationCompleted NotificationKind = "generation_completed"
	NotificationGenerationFailed    NotificationKind = "generation_failed"
)

func (kind NotificationKind) Valid() bool {
	return kind == NotificationGenerationCompleted || kind == NotificationGenerationFailed
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
