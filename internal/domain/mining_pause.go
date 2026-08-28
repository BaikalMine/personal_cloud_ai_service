package domain

import "time"

// QuickGenerationMiningLease keeps the miner paused while a priority-pool
// generation remains queued or running in ComfyUI.
type QuickGenerationMiningLease struct {
	ID           string
	PromptID     string
	UserID       int64
	MinerID      int64
	ScriptPath   string
	ProcessName  string
	ResumeMining bool
	CreatedAt    time.Time
}
