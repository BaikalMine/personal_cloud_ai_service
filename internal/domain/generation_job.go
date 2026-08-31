package domain

import "time"

type GenerationJobState string

const (
	GenerationJobDraft               GenerationJobState = "draft"
	GenerationJobPreparing           GenerationJobState = "preparing"
	GenerationJobUploading           GenerationJobState = "uploading"
	GenerationJobWaitingForResources GenerationJobState = "waiting_for_resources"
	GenerationJobQueued              GenerationJobState = "queued"
	GenerationJobRunning             GenerationJobState = "running"
	GenerationJobPostprocessing      GenerationJobState = "postprocessing"
	GenerationJobArchiving           GenerationJobState = "archiving"
	GenerationJobCompleted           GenerationJobState = "completed"
	GenerationJobFailed              GenerationJobState = "failed"
	GenerationJobCancelled           GenerationJobState = "cancelled"
	GenerationJobExpired             GenerationJobState = "expired"
)

var generationJobTransitions = map[GenerationJobState]map[GenerationJobState]struct{}{
	GenerationJobDraft: {
		GenerationJobPreparing: {}, GenerationJobFailed: {}, GenerationJobCancelled: {}, GenerationJobExpired: {},
	},
	GenerationJobPreparing: {
		GenerationJobUploading: {}, GenerationJobWaitingForResources: {}, GenerationJobFailed: {}, GenerationJobCancelled: {}, GenerationJobExpired: {},
	},
	GenerationJobUploading: {
		GenerationJobWaitingForResources: {}, GenerationJobFailed: {}, GenerationJobCancelled: {}, GenerationJobExpired: {},
	},
	GenerationJobWaitingForResources: {
		GenerationJobQueued: {}, GenerationJobFailed: {}, GenerationJobCancelled: {}, GenerationJobExpired: {},
	},
	GenerationJobQueued: {
		GenerationJobRunning: {}, GenerationJobPostprocessing: {}, GenerationJobFailed: {}, GenerationJobCancelled: {}, GenerationJobExpired: {},
	},
	GenerationJobRunning: {
		GenerationJobPostprocessing: {}, GenerationJobFailed: {}, GenerationJobCancelled: {}, GenerationJobExpired: {},
	},
	GenerationJobPostprocessing: {
		GenerationJobArchiving: {}, GenerationJobFailed: {}, GenerationJobExpired: {},
	},
	GenerationJobArchiving: {
		GenerationJobCompleted: {}, GenerationJobFailed: {}, GenerationJobExpired: {},
	},
}

func (state GenerationJobState) Valid() bool {
	switch state {
	case GenerationJobDraft, GenerationJobPreparing, GenerationJobUploading, GenerationJobWaitingForResources,
		GenerationJobQueued, GenerationJobRunning, GenerationJobPostprocessing, GenerationJobArchiving,
		GenerationJobCompleted, GenerationJobFailed, GenerationJobCancelled, GenerationJobExpired:
		return true
	default:
		return false
	}
}

func (state GenerationJobState) Terminal() bool {
	return state == GenerationJobCompleted || state == GenerationJobFailed || state == GenerationJobCancelled || state == GenerationJobExpired
}

func (state GenerationJobState) Cancellable() bool {
	switch state {
	case GenerationJobDraft, GenerationJobPreparing, GenerationJobUploading, GenerationJobWaitingForResources, GenerationJobQueued, GenerationJobRunning:
		return true
	default:
		return false
	}
}

func CanTransitionGenerationJob(from, to GenerationJobState) bool {
	if from == to {
		return from.Valid()
	}
	next, ok := generationJobTransitions[from]
	if !ok {
		return false
	}
	_, ok = next[to]
	return ok
}

type GenerationJob struct {
	ID                  int64
	PublicID            string
	UserID              *int64
	UsernameSnapshot    string
	RequestID           string
	ParentJobID         *int64
	PromptID            string
	TemplateID          string
	WorkflowID          string
	ModelName           string
	Seed                int64
	PayloadCipher       []byte
	State               GenerationJobState
	StatusMessage       string
	ErrorCode           string
	ErrorMessage        string
	Attempt             int
	Dependencies        []string
	InputCount          int
	StateChangedAt      time.Time
	StartedAt           *time.Time
	FinishedAt          *time.Time
	ResourcesReleasedAt *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type GenerationJobTransition struct {
	ID           int64
	JobID        int64
	FromState    GenerationJobState
	ToState      GenerationJobState
	Message      string
	ErrorCode    string
	ErrorMessage string
	Attempt      int
	CreatedAt    time.Time
}

type CreateGenerationJobParams struct {
	PublicID         string
	UserID           int64
	UsernameSnapshot string
	RequestID        string
	ParentJobID      *int64
}

type PreparedGenerationJob struct {
	TemplateID    string
	WorkflowID    string
	ModelName     string
	Seed          int64
	PayloadCipher []byte
	Dependencies  []string
	InputCount    int
}

type GenerationJobTransitionParams struct {
	State        GenerationJobState
	Message      string
	ErrorCode    string
	ErrorMessage string
	Attempt      int
}
