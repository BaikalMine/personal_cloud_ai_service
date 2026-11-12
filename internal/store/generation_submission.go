package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"ai-access-gateway/internal/domain"
)

var ErrGenerationSubmissionClosed = errors.New("generation submission was closed")
var ErrGenerationSubmissionExpired = errors.New("generation submission history expired")
var ErrGenerationSubmissionUncertain = errors.New("generation submission binding is unconfirmed")

const generationSubmissionColumns = `user_id,kind,request_id,public_id,closed,created_at`

func scanGenerationSubmission(row generationJobScanner) (domain.GenerationSubmission, error) {
	var receipt domain.GenerationSubmission
	err := row.Scan(&receipt.UserID, &receipt.Kind, &receipt.RequestID, &receipt.PublicID, &receipt.Closed, &receipt.CreatedAt)
	return receipt, err
}

// Creation and closure take this lock before any job/batch locks. The compact
// row has no cascading job FK: pruning history must never reopen a request ID.
func lockGenerationSubmission(ctx context.Context, tx *sql.Tx, userID int64, kind, requestID string) (domain.GenerationSubmission, error) {
	if userID <= 0 || kind != "single" && kind != "batch" || strings.TrimSpace(requestID) == "" {
		return domain.GenerationSubmission{}, ErrGenerationSubmissionUncertain
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_submissions(user_id,kind,request_id)
		VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, userID, kind, requestID); err != nil {
		return domain.GenerationSubmission{}, err
	}
	return scanGenerationSubmission(tx.QueryRowContext(ctx, `SELECT `+generationSubmissionColumns+`
		FROM generation_submissions WHERE user_id=$1 AND kind=$2 AND request_id=$3 FOR UPDATE`, userID, kind, requestID))
}

func bindGenerationSubmission(ctx context.Context, tx *sql.Tx, receipt domain.GenerationSubmission, publicID string) error {
	if receipt.Closed || publicID == "" || receipt.PublicID != "" && receipt.PublicID != publicID {
		return ErrGenerationSubmissionUncertain
	}
	_, err := tx.ExecContext(ctx, `UPDATE generation_submissions SET public_id=$4
		WHERE user_id=$1 AND kind=$2 AND request_id=$3 AND NOT closed`, receipt.UserID, receipt.Kind, receipt.RequestID, publicID)
	return err
}

func (s *Store) GenerationSubmissionByRequest(ctx context.Context, userID int64, kind, requestID string) (domain.GenerationSubmission, error) {
	return scanGenerationSubmission(s.db.QueryRowContext(ctx, `SELECT `+generationSubmissionColumns+`
		FROM generation_submissions WHERE user_id=$1 AND kind=$2 AND request_id=$3`, userID, kind, requestID))
}

// Closing an absent request is a durable tombstone, serialized with its original
// POST. A bound receipt is returned unchanged; the caller must use the existing
// job/batch cancellation protocol, never claim that the executor was stopped.
func (s *Store) CloseGenerationSubmission(ctx context.Context, userID int64, kind, requestID string) (domain.GenerationSubmission, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationSubmission{}, err
	}
	defer tx.Rollback()
	receipt, err := lockGenerationSubmission(ctx, tx, userID, kind, requestID)
	if err != nil {
		return receipt, err
	}
	if receipt.Closed || receipt.PublicID != "" {
		return receipt, tx.Commit()
	}
	query := `SELECT public_id FROM generation_jobs WHERE user_id=$1 AND request_id=$2`
	if kind == "batch" {
		query = `SELECT public_id FROM generation_batches WHERE user_id=$1 AND request_id=$2`
	}
	err = tx.QueryRowContext(ctx, query, userID, requestID).Scan(&receipt.PublicID)
	if err == nil {
		if err := bindGenerationSubmission(ctx, tx, receipt, receipt.PublicID); err != nil {
			return receipt, err
		}
		return receipt, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return receipt, err
	}
	if kind == "single" {
		var legacy bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM generation_requests WHERE user_id=$1 AND request_id=$2)`, userID, requestID).Scan(&legacy); err != nil {
			return receipt, err
		}
		if legacy {
			return receipt, ErrGenerationSubmissionUncertain
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_submissions SET closed=true WHERE user_id=$1 AND kind=$2 AND request_id=$3`, userID, kind, requestID); err != nil {
		return receipt, err
	}
	receipt.Closed = true
	return receipt, tx.Commit()
}
