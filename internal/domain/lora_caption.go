package domain

import "time"

const (
	LoraCaptionMaxPending  = 100
	LoraCaptionMaxJobs     = 500
	LoraCaptionMaxAttempts = 3
)

type LoraCaptionInput struct {
	TriggerWord        string           `json:"trigger_word"`
	ConceptType        string           `json:"concept_type"`
	Image              LoraDatasetImage `json:"image"`
	AssetHash          string           `json:"asset_hash"`
	Instruction        string           `json:"instruction"`
	InstructionVersion string           `json:"instruction_version"`
}

type LoraCaptionResult struct {
	Caption string `json:"caption"`
	Model   string `json:"model"`
	Warning string `json:"warning,omitempty"`
}

type LoraCaptionJob struct {
	ID                 string    `json:"job_id"`
	UserID             int64     `json:"-"`
	DatasetID          string    `json:"dataset_id,omitempty"`
	ImageID            string    `json:"image_id,omitempty"`
	AssetID            string    `json:"-"`
	RequestKey         string    `json:"-"`
	InstructionVersion string    `json:"instruction_version"`
	InputCipher        []byte    `json:"-"`
	ResultCipher       []byte    `json:"-"`
	State              string    `json:"state"`
	Status             string    `json:"status"`
	Error              string    `json:"error,omitempty"`
	Attempts           int       `json:"attempts"`
	RunToken           string    `json:"-"`
	CancelRequested    bool      `json:"cancel_requested"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	AvailableAt        time.Time `json:"available_at"`
}
