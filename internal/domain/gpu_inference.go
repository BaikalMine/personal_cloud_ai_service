package domain

import "time"

const (
	MaxGPUInferenceRequestBytes = 96 << 20
	// Transport/storage envelope for multi-part video plans. Individual assistant
	// parsers still enforce their smaller mode-specific output limits.
	MaxGPUInferenceResponseBytes = 512 << 10
)

// A request ID is the GPU work ID, not an HTTP connection or worker attempt.
type GPUInference struct {
	ID                 string
	LeaseToken         string
	RequestHash        string
	RequestCipher      []byte
	TimeoutMS          int64
	State              string
	RunToken           string
	ExecutorID         string
	ExecutorGeneration int64
	ResponseStatus     int
	ResponseCipher     []byte
	CompletionCode     string
	CancelRequested    bool
	CreatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
	ResultExpiresAt    *time.Time
}

func (job GPUInference) Settled() bool {
	return job.State == "completed" || job.State == "not_dispatched" || job.State == "stopped"
}

type GPUInferenceExecutor struct {
	ID         string
	Generation int64
	Token      string
}

func GPUInferenceExternalID(id string) string { return "inference/" + id }
