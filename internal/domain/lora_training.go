package domain

import "time"

type LoraTrainingState string

const (
	LoraTrainingQueued     LoraTrainingState = "queued"
	LoraTrainingUploading  LoraTrainingState = "uploading"
	LoraTrainingPreparing  LoraTrainingState = "preparing"
	LoraTrainingCaching    LoraTrainingState = "caching"
	LoraTrainingRunning    LoraTrainingState = "running"
	LoraTrainingInstalling LoraTrainingState = "installing"
	LoraTrainingCompleted  LoraTrainingState = "completed"
	LoraTrainingFailed     LoraTrainingState = "failed"
	LoraTrainingCancelled  LoraTrainingState = "cancelled"
)

func (state LoraTrainingState) Valid() bool {
	switch state {
	case LoraTrainingQueued, LoraTrainingUploading, LoraTrainingPreparing, LoraTrainingCaching,
		LoraTrainingRunning, LoraTrainingInstalling, LoraTrainingCompleted, LoraTrainingFailed, LoraTrainingCancelled:
		return true
	default:
		return false
	}
}

func (state LoraTrainingState) Terminal() bool {
	return state == LoraTrainingCompleted || state == LoraTrainingFailed || state == LoraTrainingCancelled
}

func (state LoraTrainingState) Cancellable() bool {
	return state.Valid() && !state.Terminal()
}

type LoraTrainingJob struct {
	ID                      int64
	PublicID                string
	UserID                  *int64
	UsernameSnapshot        string
	RequestID               string
	ProfileID               string
	Family                  string
	BaseModel               string
	Name                    string
	OutputName              string
	TriggerWord             string
	ConceptType             string
	Preset                  string
	Resolution              int
	MaxTrainSteps           int
	NetworkDim              int
	NetworkAlpha            int
	LearningRate            float64
	Seed                    int64
	SampleCount             int
	DatasetBytes            int64
	DatasetPath             string
	DatasetSnapshotID       string
	DatasetSnapshotHash     string
	State                   LoraTrainingState
	Stage                   string
	Progress                int
	Message                 string
	ErrorMessage            string
	AgentJobID              string
	ArtifactName            string
	ArtifactBytes           int64
	CancellationRequestedAt *time.Time
	StartedAt               *time.Time
	FinishedAt              *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type CreateLoraTrainingJobParams struct {
	PublicID            string
	UserID              int64
	UsernameSnapshot    string
	RequestID           string
	ProfileID           string
	Family              string
	BaseModel           string
	Name                string
	OutputName          string
	TriggerWord         string
	ConceptType         string
	Preset              string
	Resolution          int
	MaxTrainSteps       int
	NetworkDim          int
	NetworkAlpha        int
	LearningRate        float64
	Seed                int64
	SampleCount         int
	DatasetBytes        int64
	DatasetPath         string
	DatasetSnapshotID   string
	DatasetSnapshotHash string
}
