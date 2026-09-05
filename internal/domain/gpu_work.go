package domain

import "time"

type GPUWorkKind string
type GPUWorkState string

const (
	GPUWorkGeneration    GPUWorkKind  = "generation"
	GPUWorkTraining      GPUWorkKind  = "lora_training"
	GPUWorkCaption       GPUWorkKind  = "lora_caption"
	GPUWorkAssistant     GPUWorkKind  = "prompt_assistant"
	GPUWorkWaiting       GPUWorkState = "waiting"
	GPUWorkHeld          GPUWorkState = "held"
	GPUWorkUncertain     GPUWorkState = "uncertain"
	GPUWorkReleased      GPUWorkState = "released"
	GPUWorkCancelled     GPUWorkState = "cancelled"
	GPUPrimaryResource                = "primary"
	GPUPriorityHeadStart              = 10 * time.Minute
	GPUIntentLifetime                 = 5 * time.Minute
	GPUMaxLeaseDuration               = 5 * time.Minute
)

func (kind GPUWorkKind) Valid() bool {
	return kind == GPUWorkGeneration || kind == GPUWorkTraining || kind == GPUWorkCaption || kind == GPUWorkAssistant
}

type GPUResource struct {
	ID          string
	Revision    int64
	Observation string
	Message     string
	ObservedAt  time.Time
	ValidUntil  time.Time
}

type GPUWork struct {
	ID                    string
	ResourceID            string
	Kind                  GPUWorkKind
	JobKey                string
	UserID                *int64
	Priority              bool
	State                 GPUWorkState
	Phase                 string
	LeaseToken            string
	LeaseUntil            *time.Time
	ExternalID            string
	CancellationRequested bool
	ReadyUntil            time.Time
	QueuedAt              time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
	HeldAt                *time.Time
	ReleasedAt            *time.Time
}

type GPUWorkRequest struct {
	ID         string
	ResourceID string
	Kind       GPUWorkKind
	JobKey     string
	UserID     int64
	Priority   bool
}

type GPUAdmission struct {
	Work             GPUWork
	Granted          bool
	WaitCode         string
	Position         int
	ResourceRevision int64
}

// Evidence is supplied only by the adapter that checked the real executor.
// A timeout, cancelled HTTP request or missing response is not completion proof.
type GPUReleaseEvidence struct {
	Code             string
	Detail           string
	ResourceRevision int64
}
