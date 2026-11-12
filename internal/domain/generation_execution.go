package domain

import "time"

// GenerationExecutionRecord is encrypted independently of mutable projections.
// Its lifetime follows the generation job, including uncertain submissions.
type GenerationExecutionRecord struct {
	JobID         int64
	PayloadCipher []byte
	PayloadSHA256 string
	RecordedAt    time.Time
}
