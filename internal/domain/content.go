package domain

import "time"

type ContentEventRecord struct {
	UserID          int64
	GenerationJobID *int64
	CorrelationID   string
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
	AuthorDeleted       bool
	GenerationJobID     *int64
	CorrelationID       string
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
	UpdatedAt           time.Time
	ExpiresAt           time.Time
}

type PromptAssistantRunRecord struct {
	ContentEventID   int64
	UserID           int64
	CorrelationID    string
	Mode             string
	Profile          string
	Model            string
	Status           string
	LatencyMS        int64
	PromptTokens     int
	CompletionTokens int
	TotalDurationMS  int64
	LoadDurationMS   int64
	EvalDurationMS   int64
	NumPredict       int
	TimeoutMS        int64
	KeepAlive        string
	ReferenceCount   int
	ErrorCode        string
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
	SizeBytes     int64
	StorageFormat string
}

type PendingSensitiveMedia struct {
	ID              int64
	EventID         int64
	GenerationJobID *int64
	CorrelationID   string
	MIMEType        string
	PayloadCipher   []byte
	SizeBytes       int64
	StorageFormat   string
}

type UserGenerationMedia struct {
	ID            int64
	MediaType     string
	MIMEType      string
	OriginalName  string
	ModelName     string
	SizeBytes     int64
	CreatedAt     time.Time
	ExpiresAt     time.Time
	Sensitive     bool
	VisualPending bool
	Pinned        bool
	Favorite      bool
}

type ContentMediaSummary struct {
	ID            int64
	EventID       int64
	MediaType     string
	VisualPending bool
	UpdatedAt     time.Time
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
	SizeBytes     int64
	StorageFormat string
}

type FeatureSuggestionRecord struct {
	UserID            int64
	Username          string
	Kind              string
	Title             string
	DescriptionCipher []byte
	LinksCipher       []byte
	JSONName          string
	JSONCipher        []byte
	JSONSizeBytes     int64
}

type FeatureSuggestionScanRecord struct {
	Kind        string
	SourceName  string
	SourceIndex int
}

type FeatureSuggestionScan struct {
	ID             int64
	SuggestionID   int64
	Kind           string
	SourceName     string
	SourceIndex    int
	AnalysisID     string
	Status         string
	Malicious      int
	Suspicious     int
	Harmless       int
	Undetected     int
	Timeout        int
	Error          string
	AttemptCount   int
	LeaseToken     string
	LeaseExpiresAt *time.Time
	CheckedAt      *time.Time
}

type FeatureSuggestionRow struct {
	ID                  int64
	UserID              int64
	Username            string
	AuthorDeleted       bool
	Kind                string
	Title               string
	DescriptionCipher   []byte
	LinksCipher         []byte
	JSONName            string
	JSONCipher          []byte
	JSONSizeBytes       int64
	Status              string
	ScanStatus          string
	ReviewCommentCipher []byte
	ReviewedBy          int64
	ReviewedByUsername  string
	ReviewerDeleted     bool
	SubmittedAt         *time.Time
	ReviewedAt          *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Scans               []FeatureSuggestionScan
}
