package domain

import "time"

// GenerationRecipeRow stores an encrypted reusable set of fast-generation
// fields. Image uploads are deliberately excluded from recipe payloads.
type GenerationRecipeRow struct {
	ID            int64
	Name          string
	TemplateID    string
	WorkflowID    string
	PayloadCipher []byte
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// GenerationVariantRow is a submitted quick-generation attempt. Its payload
// captures the resolved seed and user-visible settings for safe reuse later.
type GenerationVariantRow struct {
	ID             int64
	JobID          *int64
	JobPublicID    string
	RequestID      string
	ParentJobID    *int64
	UserID         int64
	PromptID       string
	TemplateID     string
	WorkflowID     string
	ModelName      string
	Seed           int64
	PayloadCipher  []byte
	State          string
	CreatedAt      time.Time
	FinishedAt     *time.Time
	StateChangedAt time.Time
	ErrorMessage   string
}

// GenerationAccessPolicy is an optional per-user allow-list. Empty lists mean
// that the corresponding catalog stays available; this preserves existing
// accounts until an administrator deliberately narrows their access.
type GenerationAccessPolicy struct {
	UserID         int64
	PresetIDs      []string
	ModelIDs       []string
	KreaLoraGroups []string
	FluxLoraGroups []string
}

type GenerationVariantMedia struct {
	ID                    int64
	PromptID              string
	MediaType             string
	MIMEType              string
	Filename              string
	SizeBytes             int64
	CreatedAt             time.Time
	ExpiresAt             time.Time
	Sensitive             bool
	VisualPending         bool
	Pinned                bool
	Favorite              bool
	GenerationJobID       *int64
	GenerationJobPublicID string
	Tags                  []string
	Collections           []GenerationMediaCollection
	ReferenceUses         []GenerationMediaReferenceUse
}

type GenerationMediaCollection struct {
	ID        int64
	Name      string
	ItemCount int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type GenerationMediaReferenceRecord struct {
	SourceMediaID   int64
	SourceMediaName string
	Number          int
	Role            string
}

type GenerationMediaReferenceUse struct {
	JobID       int64
	JobPublicID string
	TemplateID  string
	WorkflowID  string
	Number      int
	Role        string
	CreatedAt   time.Time
}
