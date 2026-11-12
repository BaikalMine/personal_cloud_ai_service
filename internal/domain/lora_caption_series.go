package domain

import "time"

type LoraCaptionSeries struct {
	ID               string                    `json:"id"`
	DatasetID        string                    `json:"dataset_id,omitempty"`
	Name             string                    `json:"name"`
	State            string                    `json:"state"`
	TotalCount       int                       `json:"total_count"`
	CompletedCount   int                       `json:"completed_count"`
	FailedCount      int                       `json:"failed_count"`
	CancelledCount   int                       `json:"cancelled_count"`
	UnavailableCount int                       `json:"unavailable_count"`
	CreatedAt        time.Time                 `json:"created_at"`
	UpdatedAt        time.Time                 `json:"updated_at"`
	FinishedAt       *time.Time                `json:"finished_at,omitempty"`
	ExpiresAt        time.Time                 `json:"expires_at"`
	Members          []LoraCaptionSeriesMember `json:"members"`
}

type LoraCaptionSeriesMember struct {
	JobID     string `json:"job_id"`
	ImageID   string `json:"image_id"`
	Position  int    `json:"position"`
	State     string `json:"state"`
	Available bool   `json:"available"`
}
