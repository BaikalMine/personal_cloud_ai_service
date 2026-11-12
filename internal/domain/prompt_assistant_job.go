package domain

import "time"

type PromptAssistantJob struct {
	ID, Mode, RequestHash, BodyHash string
	ModelID                         string
	UserID                          int64
	RequestCipher, MetadataCipher   []byte
	ResultCipher, RuntimeCipher     []byte
	State, Status, RunToken         string
	CancelRequested                 bool
	InferenceWorkID                 string
	ContentEventID                  int64
	CreatedAt, UpdatedAt            time.Time
	AvailableAt, ExpiresAt          time.Time
}

func (job PromptAssistantJob) Terminal() bool {
	return job.State == "completed" || job.State == "failed" || job.State == "cancelled"
}

func PromptAssistantGPUKey(id string) string { return "assistant/" + id }
