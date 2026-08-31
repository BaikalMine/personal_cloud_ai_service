package domain

import "time"

type ContentEventRecord struct {
	UserID          int64
	Service         string
	Kind            string
	ExternalID      string
	Model           string
	GenerationState string
	PromptCipher    []byte
	ResponseCipher  []byte
	MetadataCipher  []byte
	Sensitive       bool
	ExpiresAt       time.Time
}

type ContentEventRow struct {
	ID                  int64
	UserID              int64
	Username            string
	Service             string
	Kind                string
	ExternalID          string
	Model               string
	GenerationState     string
	PromptCipher        []byte
	ResponseCipher      []byte
	MetadataCipher      []byte
	Sensitive           bool
	GeneratedMediaCount int64
	MediaExpiresAt      time.Time
	MediaCount          int64
	CreatedAt           time.Time
	ExpiresAt           time.Time
}

type ContentMediaRecord struct {
	EventID       int64
	MediaType     string
	MIMEType      string
	OriginalName  string
	Subfolder     string
	StorageType   string
	PayloadCipher []byte
	SizeBytes     int64
	ContentHash   string
	ExpiresAt     time.Time
}

type ContentMediaRow struct {
	ID            int64
	MediaType     string
	MIMEType      string
	OriginalName  string
	PayloadCipher []byte
}

type PendingSensitiveMedia struct {
	ID            int64
	EventID       int64
	MIMEType      string
	PayloadCipher []byte
}

type UserGenerationMedia struct {
	ID            int64
	MediaType     string
	MIMEType      string
	OriginalName  string
	ModelName     string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	Sensitive     bool
	VisualPending bool
}

type ContentMediaSummary struct {
	ID            int64
	EventID       int64
	MediaType     string
	VisualPending bool
}

type ComfyOutputOwnership struct {
	PromptID    string
	Filename    string
	Subfolder   string
	StorageType string
	MediaType   string
	ExpiresAt   time.Time
}

type ContentRetentionStats struct {
	EventCount      int64
	MediaCount      int64
	MediaBytes      int64
	NextEventExpiry *time.Time
	NextMediaExpiry *time.Time
}

type ComfyOutputArchiveCandidate struct {
	UserID      int64
	Filename    string
	Subfolder   string
	StorageType string
	MediaType   string
}

type ExpiredComfyMedia struct {
	ID           int64
	Filename     string
	Subfolder    string
	StorageType  string
	SizeBytes    int64
	ContentHash  string
	HasOwnership bool
}

type ExpiredComfyInputAsset struct {
	ID          string
	Filename    string
	Subfolder   string
	StorageType string
	SizeBytes   int64
	ContentHash string
	State       string
}

type ComfyOutputCleanupTombstone struct {
	ID          int64
	Filename    string
	Subfolder   string
	StorageType string
	SizeBytes   int64
	ContentHash string
}

type UnhashedComfyMedia struct {
	ID            int64
	PayloadCipher []byte
}

type FeatureSuggestionRecord struct {
	UserID            int64
	Username          string
	Title             string
	DescriptionCipher []byte
	LinksCipher       []byte
	JSONName          string
	JSONCipher        []byte
}

type FeatureSuggestionScanRecord struct {
	Kind       string
	SourceName string
}

type FeatureSuggestionScan struct {
	ID         int64
	Kind       string
	SourceName string
	AnalysisID string
	Status     string
	Malicious  int
	Suspicious int
	Harmless   int
	Undetected int
	Timeout    int
	Error      string
	CheckedAt  *time.Time
}

type FeatureSuggestionRow struct {
	ID                int64
	UserID            int64
	Username          string
	Title             string
	DescriptionCipher []byte
	LinksCipher       []byte
	JSONName          string
	JSONCipher        []byte
	Status            string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Scans             []FeatureSuggestionScan
}
