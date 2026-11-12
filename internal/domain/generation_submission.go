package domain

import "time"

// A compact submission receipt survives job/history retention. Closed means
// creation was fenced before any job or batch was published, not GPU cancellation.
type GenerationSubmission struct {
	UserID    int64
	Kind      string
	RequestID string
	PublicID  string
	Closed    bool
	CreatedAt time.Time
}
