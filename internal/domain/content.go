package domain

import "time"

type ContentEventRecord struct {
	UserID         int64
	Service        string
	Kind           string
	ExternalID     string
	Model          string
	PromptCipher   []byte
	ResponseCipher []byte
	MetadataCipher []byte
}

type ContentEventRow struct {
	ID             int64
	UserID         int64
	Username       string
	Service        string
	Kind           string
	ExternalID     string
	Model          string
	PromptCipher   []byte
	ResponseCipher []byte
	MetadataCipher []byte
	MediaCount     int64
	CreatedAt      time.Time
	ExpiresAt      time.Time
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

type UserGenerationMedia struct {
	ID           int64
	MediaType    string
	MIMEType     string
	OriginalName string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

type ContentMediaSummary struct {
	ID        int64
	EventID   int64
	MediaType string
}

type ComfyOutputOwnership struct {
	PromptID    string
	Filename    string
	Subfolder   string
	StorageType string
	MediaType   string
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

type UnhashedComfyMedia struct {
	ID            int64
	PayloadCipher []byte
}
