package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

var (
	ErrFeatureSuggestionStateConflict  = errors.New("feature suggestion state conflict")
	ErrFeatureSuggestionUnsafeDecision = errors.New("feature suggestion cannot be accepted before a clean scan")
)

const featureSuggestionSelect = `
	SELECT s.id,COALESCE(s.user_id,0),COALESCE(NULLIF(u.username,''),NULLIF(s.username,''),'Удалённый пользователь'),
	       s.user_id IS NULL,s.kind,s.title,s.description_cipher,s.links_cipher,s.json_name,s.json_cipher,s.json_size_bytes,
	       s.status,s.scan_status,s.review_comment_cipher,COALESCE(s.reviewed_by,0),
	       COALESCE(NULLIF(reviewer.username,''),s.reviewed_by_username,''),
	       s.reviewed_by IS NULL AND s.reviewed_by_username <> '',s.submitted_at,s.reviewed_at,s.created_at,s.updated_at
	FROM feature_suggestions s
	LEFT JOIN users u ON u.id=s.user_id
	LEFT JOIN users reviewer ON reviewer.id=s.reviewed_by
`

type featureSuggestionScanner interface {
	Scan(dest ...any) error
}

func scanFeatureSuggestion(scanner featureSuggestionScanner) (domain.FeatureSuggestionRow, error) {
	var item domain.FeatureSuggestionRow
	err := scanner.Scan(
		&item.ID, &item.UserID, &item.Username, &item.AuthorDeleted, &item.Kind, &item.Title,
		&item.DescriptionCipher, &item.LinksCipher, &item.JSONName, &item.JSONCipher, &item.JSONSizeBytes,
		&item.Status, &item.ScanStatus, &item.ReviewCommentCipher, &item.ReviewedBy,
		&item.ReviewedByUsername, &item.ReviewerDeleted, &item.SubmittedAt, &item.ReviewedAt,
		&item.CreatedAt, &item.UpdatedAt,
	)
	return item, err
}

func scanFeatureSuggestionScan(scanner featureSuggestionScanner) (domain.FeatureSuggestionScan, error) {
	var item domain.FeatureSuggestionScan
	err := scanner.Scan(
		&item.ID, &item.SuggestionID, &item.Kind, &item.SourceName, &item.SourceIndex,
		&item.AnalysisID, &item.Status, &item.Malicious, &item.Suspicious, &item.Harmless,
		&item.Undetected, &item.Timeout, &item.Error, &item.AttemptCount, &item.LeaseToken,
		&item.LeaseExpiresAt, &item.CheckedAt,
	)
	return item, err
}

func (s *Store) CreateFeatureSuggestionDraft(ctx context.Context, suggestion domain.FeatureSuggestionRecord) (domain.FeatureSuggestionRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO feature_suggestions (user_id,username,kind,title,description_cipher,links_cipher,json_name,json_cipher,json_size_bytes,status,scan_status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'draft','none') RETURNING id
	`, suggestion.UserID, suggestion.Username, suggestion.Kind, suggestion.Title, suggestion.DescriptionCipher, suggestion.LinksCipher, suggestion.JSONName, suggestion.JSONCipher, suggestion.JSONSizeBytes).Scan(&id); err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	item, err := scanFeatureSuggestion(tx.QueryRowContext(ctx, featureSuggestionSelect+` WHERE s.id=$1`, id))
	if err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	return item, nil
}

func (s *Store) UpdateFeatureSuggestionDraft(ctx context.Context, id, userID int64, suggestion domain.FeatureSuggestionRecord) (domain.FeatureSuggestionRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	defer tx.Rollback()
	var updatedID int64
	err = tx.QueryRowContext(ctx, `
		UPDATE feature_suggestions
		SET kind=$3,title=$4,description_cipher=$5,links_cipher=$6,json_name=$7,json_cipher=$8,json_size_bytes=$9,updated_at=now()
		WHERE id=$1 AND user_id=$2 AND status='draft'
		RETURNING id
	`, id, userID, suggestion.Kind, suggestion.Title, suggestion.DescriptionCipher, suggestion.LinksCipher, suggestion.JSONName, suggestion.JSONCipher, suggestion.JSONSizeBytes).Scan(&updatedID)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if lookupErr := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_suggestions WHERE id=$1 AND user_id=$2)`, id, userID).Scan(&exists); lookupErr != nil {
			return domain.FeatureSuggestionRow{}, lookupErr
		}
		if exists {
			return domain.FeatureSuggestionRow{}, ErrFeatureSuggestionStateConflict
		}
		return domain.FeatureSuggestionRow{}, sql.ErrNoRows
	}
	if err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	item, err := scanFeatureSuggestion(tx.QueryRowContext(ctx, featureSuggestionSelect+` WHERE s.id=$1`, updatedID))
	if err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	return item, nil
}

func (s *Store) DeleteFeatureSuggestionDraft(ctx context.Context, id, userID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM feature_suggestions WHERE id=$1 AND user_id=$2 AND status='draft'`, id, userID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) SubmitFeatureSuggestion(ctx context.Context, id, userID int64, scans []domain.FeatureSuggestionScanRecord) (domain.FeatureSuggestionRow, []domain.FeatureSuggestionScan, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.FeatureSuggestionRow{}, nil, err
	}
	defer tx.Rollback()

	var currentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM feature_suggestions WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, userID).Scan(&currentStatus); err != nil {
		return domain.FeatureSuggestionRow{}, nil, err
	}
	if currentStatus != "draft" {
		return domain.FeatureSuggestionRow{}, nil, ErrFeatureSuggestionStateConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM feature_suggestion_scans WHERE suggestion_id=$1`, id); err != nil {
		return domain.FeatureSuggestionRow{}, nil, err
	}

	created := make([]domain.FeatureSuggestionScan, 0, len(scans))
	for _, scan := range scans {
		item, scanErr := scanFeatureSuggestionScan(tx.QueryRowContext(ctx, `
			INSERT INTO feature_suggestion_scans (suggestion_id,kind,source_name,source_index)
			VALUES ($1,$2,$3,$4)
			RETURNING id,suggestion_id,kind,source_name,source_index,analysis_id,status,malicious,suspicious,harmless,undetected,timeout,error_message,attempt_count,lease_token,lease_expires_at,checked_at
		`, id, scan.Kind, scan.SourceName, scan.SourceIndex))
		if scanErr != nil {
			return domain.FeatureSuggestionRow{}, nil, scanErr
		}
		created = append(created, item)
	}

	status, scanStatus := "submitted", "queued"
	if len(created) == 0 {
		status, scanStatus = "review", "none"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE feature_suggestions
		SET status=$2,scan_status=$3,submitted_at=now(),review_comment_cipher='\x'::bytea,
		    reviewed_by=NULL,reviewed_by_username='',reviewed_at=NULL,updated_at=now()
		WHERE id=$1
	`, id, status, scanStatus); err != nil {
		return domain.FeatureSuggestionRow{}, nil, err
	}
	item, err := scanFeatureSuggestion(tx.QueryRowContext(ctx, featureSuggestionSelect+` WHERE s.id=$1`, id))
	if err != nil {
		return domain.FeatureSuggestionRow{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return domain.FeatureSuggestionRow{}, nil, err
	}
	return item, created, nil
}

func (s *Store) ListFeatureSuggestions(ctx context.Context, limit int) ([]domain.FeatureSuggestionRow, error) {
	return s.listFeatureSuggestions(ctx, featureSuggestionSelect+`
		WHERE s.status <> 'draft'
		ORDER BY s.created_at DESC,s.id DESC LIMIT $1
	`, limit)
}

func (s *Store) ListFeatureSuggestionsByUser(ctx context.Context, userID int64, limit int) ([]domain.FeatureSuggestionRow, error) {
	return s.listFeatureSuggestions(ctx, featureSuggestionSelect+`
		WHERE s.user_id=$1
		ORDER BY s.created_at DESC,s.id DESC LIMIT $2
	`, userID, limit)
}

func (s *Store) listFeatureSuggestions(ctx context.Context, query string, args ...any) ([]domain.FeatureSuggestionRow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.FeatureSuggestionRow, 0)
	ids := make([]int64, 0)
	byID := make(map[int64]int)
	for rows.Next() {
		item, scanErr := scanFeatureSuggestion(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		byID[item.ID] = len(items)
		ids = append(ids, item.ID)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return items, err
	}

	scanRows, err := s.db.QueryContext(ctx, `
		SELECT id,suggestion_id,kind,source_name,source_index,analysis_id,status,malicious,suspicious,harmless,undetected,timeout,error_message,attempt_count,lease_token,lease_expires_at,checked_at
		FROM feature_suggestion_scans WHERE suggestion_id=ANY($1) ORDER BY suggestion_id,kind,source_index,id
	`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer scanRows.Close()
	for scanRows.Next() {
		item, scanErr := scanFeatureSuggestionScan(scanRows)
		if scanErr != nil {
			return nil, scanErr
		}
		if index, ok := byID[item.SuggestionID]; ok {
			items[index].Scans = append(items[index].Scans, item)
		}
	}
	return items, scanRows.Err()
}

func (s *Store) FeatureSuggestionByID(ctx context.Context, id int64) (domain.FeatureSuggestionRow, error) {
	item, err := scanFeatureSuggestion(s.db.QueryRowContext(ctx, featureSuggestionSelect+` WHERE s.id=$1`, id))
	if err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	if err := s.loadFeatureSuggestionScans(ctx, &item); err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	return item, nil
}

func (s *Store) FeatureSuggestionByIDForUser(ctx context.Context, id, userID int64) (domain.FeatureSuggestionRow, error) {
	item, err := scanFeatureSuggestion(s.db.QueryRowContext(ctx, featureSuggestionSelect+` WHERE s.id=$1 AND s.user_id=$2`, id, userID))
	if err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	if err := s.loadFeatureSuggestionScans(ctx, &item); err != nil {
		return domain.FeatureSuggestionRow{}, err
	}
	return item, nil
}

func (s *Store) loadFeatureSuggestionScans(ctx context.Context, item *domain.FeatureSuggestionRow) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,suggestion_id,kind,source_name,source_index,analysis_id,status,malicious,suspicious,harmless,undetected,timeout,error_message,attempt_count,lease_token,lease_expires_at,checked_at
		FROM feature_suggestion_scans WHERE suggestion_id=$1 ORDER BY kind,source_index,id
	`, item.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		scan, scanErr := scanFeatureSuggestionScan(rows)
		if scanErr != nil {
			return scanErr
		}
		item.Scans = append(item.Scans, scan)
	}
	return rows.Err()
}

func (s *Store) FeatureSuggestionJSONForAdmin(ctx context.Context, id int64) (domain.FeatureSuggestionRow, error) {
	return scanFeatureSuggestion(s.db.QueryRowContext(ctx, featureSuggestionSelect+`
		WHERE s.id=$1 AND s.json_name <> '' AND s.status IN ('review','accepted')
		  AND EXISTS (
			SELECT 1 FROM feature_suggestion_scans scan
			WHERE scan.suggestion_id=s.id AND scan.kind='json' AND scan.source_index=0
			  AND scan.status='completed' AND scan.malicious=0 AND scan.suspicious=0
		  )
	`, id))
}

func (s *Store) ClaimFeatureSuggestionScans(ctx context.Context, limit int, leaseDuration time.Duration) ([]domain.FeatureSuggestionScan, error) {
	if limit <= 0 {
		return nil, nil
	}
	if limit > 24 {
		limit = 24
	}
	if leaseDuration < time.Second {
		leaseDuration = time.Minute
	}
	token, err := featureSuggestionLeaseToken()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		WITH candidates AS (
			SELECT scan.id
			FROM feature_suggestion_scans scan
			JOIN feature_suggestions suggestion ON suggestion.id=scan.suggestion_id
			WHERE suggestion.status IN ('submitted','scanning')
			  AND scan.status IN ('queued','in-progress')
			  AND (scan.lease_expires_at IS NULL OR scan.lease_expires_at <= now())
			  AND scan.attempt_count < 100
			ORDER BY scan.created_at,scan.id
			FOR UPDATE OF scan SKIP LOCKED
			LIMIT $1
		)
		UPDATE feature_suggestion_scans scan
		SET lease_token=$2,lease_expires_at=now()+($3::bigint*interval '1 second'),updated_at=now()
		FROM candidates WHERE scan.id=candidates.id
		RETURNING scan.id,scan.suggestion_id,scan.kind,scan.source_name,scan.source_index,scan.analysis_id,scan.status,
		          scan.malicious,scan.suspicious,scan.harmless,scan.undetected,scan.timeout,scan.error_message,
		          scan.attempt_count,scan.lease_token,scan.lease_expires_at,scan.checked_at
	`, limit, token, int64(leaseDuration.Seconds()))
	if err != nil {
		return nil, err
	}
	items := make([]domain.FeatureSuggestionScan, 0)
	suggestionIDs := make([]int64, 0)
	seenSuggestion := make(map[int64]struct{})
	for rows.Next() {
		item, scanErr := scanFeatureSuggestionScan(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		items = append(items, item)
		if _, seen := seenSuggestion[item.SuggestionID]; !seen {
			seenSuggestion[item.SuggestionID] = struct{}{}
			suggestionIDs = append(suggestionIDs, item.SuggestionID)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(suggestionIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE feature_suggestions SET status='scanning',scan_status='scanning',updated_at=now()
			WHERE id=ANY($1) AND status IN ('submitted','scanning')
		`, pq.Array(suggestionIDs)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) SetFeatureSuggestionScanResult(ctx context.Context, id int64, leaseToken, analysisID, status string, malicious, suspicious, harmless, undetected, timeout int, submitted bool) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE feature_suggestion_scans
		SET analysis_id=$3,status=$4,malicious=$5,suspicious=$6,harmless=$7,undetected=$8,timeout=$9,
		    error_message='',attempt_count=attempt_count+CASE WHEN $10 THEN 1 ELSE 0 END,
		    checked_at=CASE WHEN $4='completed' THEN now() ELSE checked_at END,
		    lease_token='',lease_expires_at=NULL,updated_at=now()
		WHERE id=$1 AND lease_token=$2
	`, id, leaseToken, analysisID, status, malicious, suspicious, harmless, undetected, timeout, submitted)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return ErrFeatureSuggestionStateConflict
	}
	return err
}

func (s *Store) SetFeatureSuggestionScanError(ctx context.Context, id int64, leaseToken, message string, attempted bool) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE feature_suggestion_scans
		SET status='error',error_message=$3,attempt_count=attempt_count+CASE WHEN $4 THEN 1 ELSE 0 END,
		    lease_token='',lease_expires_at=NULL,updated_at=now()
		WHERE id=$1 AND lease_token=$2
	`, id, leaseToken, message, attempted)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return ErrFeatureSuggestionStateConflict
	}
	return err
}

func (s *Store) RefreshFeatureSuggestionStatus(ctx context.Context, suggestionID int64) error {
	result, err := s.db.ExecContext(ctx, `
		WITH summary AS (
			SELECT count(*) AS total,
			       bool_or(malicious > 0 OR suspicious > 0) AS flagged,
			       bool_or(status='error') AS failed,
			       bool_and(status='completed') AS completed,
			       bool_and(status='queued' AND analysis_id='') AS untouched
			FROM feature_suggestion_scans WHERE suggestion_id=$1
		)
		UPDATE feature_suggestions suggestion
		SET scan_status=CASE
				WHEN summary.total=0 THEN 'none'
				WHEN summary.flagged THEN 'flagged'
				WHEN summary.failed THEN 'error'
				WHEN summary.completed THEN 'clean'
				WHEN summary.untouched THEN 'queued'
				ELSE 'scanning'
			END,
			status=CASE
				WHEN suggestion.status IN ('draft','accepted','rejected') THEN suggestion.status
				WHEN summary.total=0 OR summary.completed OR summary.failed THEN 'review'
				WHEN summary.untouched AND suggestion.status='submitted' THEN 'submitted'
				ELSE 'scanning'
			END,
			updated_at=now()
		FROM summary WHERE suggestion.id=$1
	`, suggestionID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return sql.ErrNoRows
	}
	return err
}

func (s *Store) RetryFeatureSuggestionScans(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM feature_suggestions WHERE id=$1 FOR UPDATE`, id).Scan(&status); err != nil {
		return err
	}
	var scanCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM feature_suggestion_scans WHERE suggestion_id=$1`, id).Scan(&scanCount); err != nil {
		return err
	}
	if status == "draft" || status == "accepted" || status == "rejected" || scanCount == 0 {
		return ErrFeatureSuggestionStateConflict
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE feature_suggestion_scans
		SET analysis_id='',status='queued',malicious=0,suspicious=0,harmless=0,undetected=0,timeout=0,
		    error_message='',checked_at=NULL,lease_token='',lease_expires_at=NULL,updated_at=now()
		WHERE suggestion_id=$1
	`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE feature_suggestions
		SET status='submitted',scan_status='queued',review_comment_cipher='\x'::bytea,reviewed_by=NULL,
		    reviewed_by_username='',reviewed_at=NULL,updated_at=now()
		WHERE id=$1
	`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetFeatureSuggestionDecision(ctx context.Context, id, adminID int64, adminUsername, decision string, commentCipher []byte) error {
	if decision != "accepted" && decision != "rejected" {
		return ErrFeatureSuggestionStateConflict
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE feature_suggestions
		SET status=$2,review_comment_cipher=$3,reviewed_by=$4,reviewed_by_username=$5,reviewed_at=now(),updated_at=now()
		WHERE id=$1 AND status='review' AND ($2='rejected' OR scan_status IN ('none','clean'))
	`, id, decision, commentCipher, adminID, adminUsername)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	var status, scanStatus string
	if lookupErr := s.db.QueryRowContext(ctx, `SELECT status,scan_status FROM feature_suggestions WHERE id=$1`, id).Scan(&status, &scanStatus); lookupErr != nil {
		return lookupErr
	}
	if decision == "accepted" && status == "review" && scanStatus != "none" && scanStatus != "clean" {
		return ErrFeatureSuggestionUnsafeDecision
	}
	return ErrFeatureSuggestionStateConflict
}

func featureSuggestionLeaseToken() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate feature suggestion lease: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
