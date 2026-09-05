package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
)

var ErrGenerationDraftConflict = errors.New("generation draft revision conflict")

const GenerationDraftRetention = 30 * 24 * time.Hour

func (s *Store) GenerationDraft(ctx context.Context, userID int64) (domain.GenerationDraftRow, error) {
	var row domain.GenerationDraftRow
	err := s.db.QueryRowContext(ctx, `SELECT user_id,revision,payload_cipher,updated_at,expires_at
		FROM generation_drafts WHERE user_id=$1 AND expires_at>now()`, userID).Scan(
		&row.UserID, &row.Revision, &row.PayloadCipher, &row.UpdatedAt, &row.ExpiresAt)
	return row, err
}

// Global sequence values also prevent an old tab from matching a draft that
// was deleted and recreated (the ABA problem).
func (s *Store) SaveGenerationDraft(ctx context.Context, userID, expectedRevision int64, cipher []byte) (domain.GenerationDraftRow, error) {
	var row domain.GenerationDraftRow
	var result *sql.Row
	if expectedRevision == 0 {
		result = s.db.QueryRowContext(ctx, `INSERT INTO generation_drafts(user_id,payload_cipher)
			VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET
			payload_cipher=EXCLUDED.payload_cipher,revision=nextval('generation_draft_revision_seq'),
			updated_at=now(),expires_at=now()+interval '30 days'
			WHERE generation_drafts.expires_at<=now()
			RETURNING user_id,revision,payload_cipher,updated_at,expires_at`, userID, cipher)
	} else {
		result = s.db.QueryRowContext(ctx, `UPDATE generation_drafts SET payload_cipher=$3,
			revision=nextval('generation_draft_revision_seq'),updated_at=now(),expires_at=now()+interval '30 days'
			WHERE user_id=$1 AND revision=$2 AND expires_at>now()
			RETURNING user_id,revision,payload_cipher,updated_at,expires_at`, userID, expectedRevision, cipher)
	}
	err := result.Scan(&row.UserID, &row.Revision, &row.PayloadCipher, &row.UpdatedAt, &row.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationDraftRow{}, ErrGenerationDraftConflict
	}
	return row, err
}

func (s *Store) DeleteGenerationDraft(ctx context.Context, userID, expectedRevision int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM generation_drafts WHERE user_id=$1 AND revision=$2 AND expires_at>now()`, userID, expectedRevision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrGenerationDraftConflict
	}
	return err
}

func (s *Store) DeleteExpiredGenerationDrafts(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM generation_drafts WHERE expires_at<=now() AND user_id IN (
		SELECT user_id FROM generation_drafts WHERE expires_at<=now() ORDER BY expires_at LIMIT 100)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ComfyInputAssetForUser(ctx context.Context, userID int64, id string) (domain.OwnedComfyInputAsset, error) {
	var row domain.OwnedComfyInputAsset
	err := s.db.QueryRowContext(ctx, `SELECT id,filename,subfolder,size_bytes,expires_at
		FROM comfy_input_assets WHERE user_id=$1 AND id=$2 AND state='stored' AND expires_at>now()`, userID, id).Scan(
		&row.ID, &row.Filename, &row.Subfolder, &row.SizeBytes, &row.ExpiresAt)
	return row, err
}

func (s *Store) ComfyInputAssetByPathForUser(ctx context.Context, userID int64, filename, subfolder string) (domain.OwnedComfyInputAsset, error) {
	var row domain.OwnedComfyInputAsset
	err := s.db.QueryRowContext(ctx, `SELECT id,filename,subfolder,size_bytes,expires_at
		FROM comfy_input_assets WHERE user_id=$1 AND filename=$2 AND subfolder=$3 AND state='stored' AND expires_at>now()
		ORDER BY updated_at DESC LIMIT 1`, userID, filename, subfolder).Scan(
		&row.ID, &row.Filename, &row.Subfolder, &row.SizeBytes, &row.ExpiresAt)
	return row, err
}
