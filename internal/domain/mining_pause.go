package domain

import "time"

// QuickGenerationMiningLease keeps the miner paused until the related GPU
// work is confirmed complete, including local inference without a ComfyUI ID.
type QuickGenerationMiningLease struct {
	ID                string
	CorrelationID     string
	PromptID          string
	GenerationJobID   int64
	LoraTrainingJobID int64
	UserID            int64
	MinerID           int64
	ScriptPath        string
	ProcessName       string
	ResumeMining      bool
	ResumeReady       bool
	CreatedAt         time.Time
}
