package store

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"ai-access-gateway/internal/domain"
)

func (s *Store) BeginGenerationJobSubmissionWithExecution(ctx context.Context, jobID int64, token string, execution domain.GenerationExecutionRecord) error {
	digest, err := hex.DecodeString(execution.PayloadSHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(execution.PayloadSHA256) != execution.PayloadSHA256 ||
		len(execution.PayloadCipher) == 0 || len(execution.PayloadCipher) > 2<<20 || execution.JobID != jobID {
		return errors.New("invalid generation execution evidence")
	}
	return s.beginGenerationJobSubmission(ctx, jobID, token, &execution)
}

func (s *Store) GenerationExecutionForJob(ctx context.Context, userID int64, publicID string) (domain.GenerationExecutionRecord, error) {
	var record domain.GenerationExecutionRecord
	err := s.db.QueryRowContext(ctx, `SELECT e.job_id,e.payload_cipher,e.payload_sha256,e.recorded_at
		FROM generation_job_executions e JOIN generation_jobs j ON j.id=e.job_id
		WHERE j.user_id=$1 AND j.public_id=$2`, userID, publicID).
		Scan(&record.JobID, &record.PayloadCipher, &record.PayloadSHA256, &record.RecordedAt)
	return record, err
}
