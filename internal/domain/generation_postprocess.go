package domain

import "time"

const (
	PostprocessImageTemplate = "postprocess-image-v1"
	PostprocessVideoTemplate = "postprocess-video-v1"
	PostprocessWorkflow      = "postprocess-v1"
)

// IDs remain decimal strings in persisted JSON, including values above 2^53.
// ArchiveSHA256 can be empty for legacy media; SHA256 is always measured.
type PostprocessSourceIdentity struct {
	JobID         int64  `json:"job_id,string"`
	JobPublicID   string `json:"job_public_id"`
	MediaID       int64  `json:"media_id,string"`
	MediaType     string `json:"media_type"`
	MIMEType      string `json:"mime_type"`
	SizeBytes     int64  `json:"size_bytes"`
	SHA256        string `json:"sha256"`
	ArchiveSHA256 string `json:"archive_sha256"`
}

type GenerationPostprocessReceipt struct {
	UserID             int64
	RequestID          string
	State              string
	Version            int
	ConfirmationSHA256 string
	PayloadCipher      []byte
	Sources            []PostprocessSourceIdentity
	Sensitive          bool
	JobID              *int64
	JobPublicID        string
	CreatedAt          time.Time
	ExpiresAt          time.Time
}

type GenerationPostprocessReference struct {
	MediaID     int64
	JobPublicID string
	Filename    string
	SizeBytes   int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
	Sensitive   bool
}

// PayloadCipher contains the versioned quote and dispatch payload encrypted by
// the gateway. The store deliberately cannot interpret or substitute that plan.
type CreateGenerationPostprocessQuoteParams struct {
	UserID             int64
	RequestID          string
	ConfirmationSHA256 string
	PayloadCipher      []byte
	Sources            []PostprocessSourceIdentity
	ExpiresAt          time.Time
}

// One upload attempt owns one private input name. CleanupBlocked survives job
// deletion when a lost upload response leaves the storage location uncertain.
type PostprocessInput struct {
	ID             string
	JobID          int64
	SourcePosition int
	DispatchToken  string
	Filename       string
	Subfolder      string
	SizeBytes      int64
	SHA256         string
	State          string
	CleanupBlocked bool
}
