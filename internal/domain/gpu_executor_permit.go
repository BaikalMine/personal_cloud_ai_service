package domain

import "time"

const (
	GPUExecutorComfyUI = "comfyui"
	GPUExecutorOllama  = "ollama"
)

// An executor permit covers work admitted outside the Gateway task lifecycle,
// or binds a native executor to an already held Gateway GPU work item.
type GPUExecutorPermit struct {
	ID              string
	ResourceID      string
	Executor        string
	InstanceID      string
	WorkID          string
	State           string
	CompletionCode  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	OwnerID         string
	OwnerGeneration int64
}

func (p GPUExecutorPermit) Active() bool { return p.State == "reserved" || p.State == "running" }

type GPUExecutorPermitRequest struct {
	Executor        string `json:"executor,omitempty"`
	ID              string `json:"id"`
	InstanceID      string `json:"instance_id"`
	Token           string `json:"token"`
	WorkID          string `json:"work_id,omitempty"`
	WorkLease       string `json:"work_lease,omitempty"`
	OwnerID         string `json:"owner_id,omitempty"`
	OwnerGeneration int64  `json:"owner_generation,omitempty"`
	OwnerToken      string `json:"owner_token,omitempty"`
}

func (p GPUExecutorPermitRequest) ExecutorKind() string {
	if p.Executor == "" {
		return GPUExecutorComfyUI
	}
	return p.Executor
}

func (p GPUExecutorPermitRequest) OwnedExecutor() GPUInferenceExecutor {
	return GPUInferenceExecutor{ID: p.OwnerID, Generation: p.OwnerGeneration, Token: p.OwnerToken}
}
