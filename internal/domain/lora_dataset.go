package domain

import "time"

const (
	LoraDatasetMaxImages    = 100
	LoraDatasetMaxBytes     = int64(512 << 20)
	LoraDatasetUserMaxBytes = int64(2 << 30)
	LoraDatasetMaxCount     = 20
	LoraDatasetMaxSnapshots = 100
)

type LoraDatasetSettings struct {
	Name          string `json:"name"`
	OutputName    string `json:"output_name"`
	TriggerWord   string `json:"trigger_word"`
	ConceptType   string `json:"concept_type"`
	ProfileID     string `json:"profile_id"`
	Preset        string `json:"preset"`
	Resolution    int    `json:"resolution"`
	GlobalCaption string `json:"global_caption"`
}

type LoraDatasetImage struct {
	ID              string `json:"id"`
	AssetID         string `json:"asset_id"`
	Caption         string `json:"caption"`
	Excluded        bool   `json:"excluded"`
	CaptionRevision string `json:"caption_revision,omitempty"`
	CaptionJobID    string `json:"caption_job_id,omitempty"`
}

type LoraDatasetManifest struct {
	Version  int                 `json:"version"`
	Settings LoraDatasetSettings `json:"settings"`
	Images   []LoraDatasetImage  `json:"images"`
}

type LoraDatasetRow struct {
	ID             string    `json:"id"`
	UserID         int64     `json:"-"`
	Name           string    `json:"name"`
	Revision       int64     `json:"revision"`
	ManifestCipher []byte    `json:"-"`
	ImageCount     int       `json:"image_count"`
	SizeBytes      int64     `json:"size_bytes"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type LoraDatasetAsset struct {
	ID        string    `json:"id"`
	UserID    int64     `json:"-"`
	Name      string    `json:"name"`
	Hash      string    `json:"sha256"`
	MIMEType  string    `json:"mime_type"`
	SizeBytes int64     `json:"size_bytes"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	CreatedAt time.Time `json:"created_at"`
}

type LoraDatasetSnapshot struct {
	ID             string    `json:"id"`
	DatasetID      string    `json:"dataset_id"`
	UserID         int64     `json:"-"`
	Name           string    `json:"name"`
	Revision       int64     `json:"revision"`
	ManifestCipher []byte    `json:"-"`
	Hash           string    `json:"sha256"`
	ImageCount     int       `json:"image_count"`
	SizeBytes      int64     `json:"size_bytes"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}
