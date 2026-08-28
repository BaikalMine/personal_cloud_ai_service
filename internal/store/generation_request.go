package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ClaimGenerationRequest reserves a browser-generated idempotency key. When
// the key already exists, callers must recover that original request instead
// of submitting another ComfyUI prompt.
func (s *Store) ClaimGenerationRequest(ctx context.Context, userID int64, requestID string) (existing bool, promptID string, err error) {
	requestID = strings.TrimSpace(requestID)
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO generation_requests (user_id,request_id) VALUES ($1,$2)
		ON CONFLICT (user_id,request_id) DO NOTHING
	`, userID, requestID)
	if err != nil {
		return false, "", err
	}
	created, err := result.RowsAffected()
	if err != nil {
		return false, "", err
	}
	if created > 0 {
		return false, "", nil
	}
	promptID, err = s.GenerationRequestPromptID(ctx, userID, requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", err
	}
	return true, promptID, err
}

func (s *Store) GenerationRequestPromptID(ctx context.Context, userID int64, requestID string) (string, error) {
	var promptID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT prompt_id FROM generation_requests WHERE user_id=$1 AND request_id=$2
	`, userID, strings.TrimSpace(requestID)).Scan(&promptID)
	if err != nil {
		return "", err
	}
	return promptID.String, nil
}

func (s *Store) BindGenerationRequestPrompt(ctx context.Context, userID int64, requestID, promptID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE generation_requests
		SET prompt_id=$3, updated_at=now()
		WHERE user_id=$1 AND request_id=$2 AND prompt_id IS NULL
	`, userID, strings.TrimSpace(requestID), strings.TrimSpace(promptID))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed > 0 {
		return err
	}
	existing, err := s.GenerationRequestPromptID(ctx, userID, requestID)
	if err != nil {
		return err
	}
	if existing != strings.TrimSpace(promptID) {
		return errors.New("generation request is already bound to another prompt")
	}
	return nil
}

func (s *Store) ReleaseGenerationRequest(ctx context.Context, userID int64, requestID string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM generation_requests WHERE user_id=$1 AND request_id=$2 AND prompt_id IS NULL
	`, userID, strings.TrimSpace(requestID))
	return err
}
