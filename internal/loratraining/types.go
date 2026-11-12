package loratraining

import (
	"time"

	"ai-access-gateway/internal/domain"
)

// MaxNumpySeed is the largest seed accepted by NumPy's legacy MT19937
// initializer, which is used by the local musubi training scripts.
const MaxNumpySeed int64 = 1<<32 - 1

type Profile struct {
	RepeatProtocol int    `json:"repeat_protocol,omitempty"`
	ProfileSHA256  string `json:"profile_sha256,omitempty"`
	ID             string `json:"id"`
	Family         string `json:"family"`
	Name           string `json:"name"`
	BaseModel      string `json:"base_model"`
	Description    string `json:"description"`
	Ready          bool   `json:"ready"`
	Detail         string `json:"detail,omitempty"`
}

type ProfilesResponse struct {
	Available bool      `json:"available"`
	Profiles  []Profile `json:"profiles"`
	Message   string    `json:"message,omitempty"`
}

// Activity is a point-in-time admission observation, not completion evidence
// for a previously submitted job. Unknown processes keep the agent uncertain.
type Activity struct {
	ProtocolVersion      int    `json:"protocol_version"`
	State                string `json:"state"`
	ActiveJobs           int    `json:"active_jobs"`
	PendingSubmissions   int    `json:"pending_submissions"`
	UnconfirmedJobs      int    `json:"unconfirmed_jobs"`
	StorageCleanupActive bool   `json:"storage_cleanup_active,omitempty"`
}

type JobSpec struct {
	Repeat       *domain.LoraTrainingRepeatConstraint `json:"repeat,omitempty"`
	GatewayJobID string                               `json:"gateway_job_id"`
	ProfileID    string                               `json:"profile_id"`
	Owner        string                               `json:"owner"`
	Name         string                               `json:"name"`
	OutputName   string                               `json:"output_name"`
	TriggerWord  string                               `json:"trigger_word"`
	ConceptType  string                               `json:"concept_type"`
	Resolution   int                                  `json:"resolution"`
	MaxSteps     int                                  `json:"max_steps"`
	NetworkDim   int                                  `json:"network_dim"`
	NetworkAlpha int                                  `json:"network_alpha"`
	LearningRate float64                              `json:"learning_rate"`
	Seed         int64                                `json:"seed"`
	SampleCount  int                                  `json:"sample_count"`
}

type JobStatus struct {
	Provenance           *domain.LoraTrainingProvenance `json:"provenance,omitempty"`
	ExecutionUnconfirmed bool                           `json:"execution_unconfirmed,omitempty"`
	ID                   string                         `json:"id"`
	GatewayJobID         string                         `json:"gateway_job_id"`
	ProfileID            string                         `json:"profile_id"`
	State                string                         `json:"state"`
	Stage                string                         `json:"stage"`
	Progress             int                            `json:"progress"`
	Message              string                         `json:"message,omitempty"`
	Error                string                         `json:"error,omitempty"`
	LogTail              []string                       `json:"log_tail,omitempty"`
	ArtifactName         string                         `json:"artifact_name,omitempty"`
	ArtifactBytes        int64                          `json:"artifact_bytes,omitempty"`
	CreatedAt            time.Time                      `json:"created_at"`
	StartedAt            time.Time                      `json:"started_at,omitempty"`
	FinishedAt           time.Time                      `json:"finished_at,omitempty"`
}

type SubmissionFenceResult struct {
	GatewayJobID string     `json:"gateway_job_id"`
	Fenced       bool       `json:"fenced"`
	Settled      bool       `json:"settled"`
	Job          *JobStatus `json:"job,omitempty"`
}

func (status JobStatus) Terminal() bool {
	return status.State == "completed" || status.State == "failed" || status.State == "cancelled"
}
