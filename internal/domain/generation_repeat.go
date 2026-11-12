package domain

import "time"

type GenerationRepeatReceipt struct {
	UserID                int64
	RequestID             string
	SourcePublicID        string
	SourceExecutionSHA256 string
	ConfirmationSHA256    string
	JobID                 *int64
	JobPublicID           string
	Cancelled             bool
	CreatedAt             time.Time
}

type CreateGenerationRepeatParams struct {
	Job                   CreateGenerationJobParams
	Prepared              PreparedGenerationJob
	SourcePublicID        string
	SourcePayloadCipher   []byte
	SourceExecutionSHA256 string
	ConfirmationSHA256    string
}
