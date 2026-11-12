package domain

import "time"

// Task is a read-only projection. State belongs to the source state machine.
// Actions still require the source endpoint's current-state and access checks.
type Task struct {
	Kind                string             `json:"kind"`
	ID                  string             `json:"id"`
	Name                string             `json:"name"`
	State               string             `json:"state"`
	Stage               string             `json:"stage,omitempty"`
	Message             string             `json:"message"`
	Progress            *int               `json:"progress,omitempty"`
	CancellationPending bool               `json:"cancellation_pending"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
	FinishedAt          *time.Time         `json:"finished_at,omitempty"`
	ExpiresAt           *time.Time         `json:"expires_at,omitempty"`
	BatchID             string             `json:"batch_id,omitempty"`
	BatchPosition       int                `json:"batch_position,omitempty"`
	DatasetID           string             `json:"dataset_id,omitempty"`
	ImageID             string             `json:"image_id,omitempty"`
	Preview             *TaskPreview       `json:"-"`
	SourceAvailable     bool               `json:"-"`
	ArtifactAvailable   bool               `json:"-"`
	Series              *TaskSeriesSummary `json:"series,omitempty"`
}

type TaskSeriesSummary struct {
	Total       int `json:"total"`
	Completed   int `json:"completed"`
	Failed      int `json:"failed"`
	Cancelled   int `json:"cancelled"`
	Unavailable int `json:"unavailable"`
}

// TaskPreview contains an authorized derivative source, never job payloads.
type TaskPreview struct {
	MediaID      int64
	AssetID      string
	SnapshotID   string
	SnapshotHash string
	MediaType    string
	TemplateID   string
	Sensitive    bool
	ExpiresAt    time.Time
}

type TaskFilter struct {
	Kind   string
	State  string
	Search string
	From   *time.Time
	Before *time.Time
}

// Permissions are supplied by the authenticated API adapter, never a query string.
type TaskScope struct {
	UserID     int64
	Generation bool
	Training   bool
}

type TaskPage struct {
	Tasks      []Task `json:"tasks"`
	Total      int64  `json:"total"`
	Revision   int64  `json:"revision"`
	NextCursor string `json:"next_cursor,omitempty"`
}
