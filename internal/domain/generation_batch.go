package domain

import "time"

type GenerationBatchMode string

const (
	GenerationBatchSeeds     GenerationBatchMode = "seeds"
	GenerationBatchParameter GenerationBatchMode = "parameter"
)

func (mode GenerationBatchMode) Valid() bool {
	return mode == GenerationBatchSeeds || mode == GenerationBatchParameter
}

// GenerationBatch groups a controlled set of durable generation jobs. Its
// state is derived from the child jobs so it cannot drift after a restart.
type GenerationBatch struct {
	ID                      int64
	PublicID                string
	UserID                  *int64
	UsernameSnapshot        string
	RequestID               string
	ParentBatchID           *int64
	ParentBatchPublicID     string
	SourceJobID             *int64
	SourceJobPublicID       string
	WinnerJobID             *int64
	WinnerJobPublicID       string
	TemplateID              string
	WorkflowID              string
	ModelName               string
	Mode                    GenerationBatchMode
	ParameterName           string
	ParameterValues         []string
	SeedLocked              bool
	TotalCount              int
	MaxParallel             int
	CancellationRequestedAt *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
	DraftCount              int
	ActiveCount             int
	CompletedCount          int
	FailedCount             int
	CancelledCount          int
	ExpiredCount            int
}

type CreateGenerationBatchJobParams struct {
	PublicID        string
	CorrelationID   string
	RequestID       string
	ParentJobID     *int64
	Position        int
	ExperimentValue string
	Prepared        PreparedGenerationJob
}

type CreateGenerationBatchParams struct {
	PublicID         string
	UserID           int64
	UsernameSnapshot string
	RequestID        string
	ParentBatchID    *int64
	SourceJobID      *int64
	TemplateID       string
	WorkflowID       string
	ModelName        string
	Mode             GenerationBatchMode
	ParameterName    string
	ParameterValues  []string
	SeedLocked       bool
	MaxParallel      int
	Jobs             []CreateGenerationBatchJobParams
}
