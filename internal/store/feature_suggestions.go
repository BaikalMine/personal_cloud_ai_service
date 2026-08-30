package store

import (
	"context"
	"fmt"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

func (s *Store) CreateFeatureSuggestion(ctx context.Context, suggestion domain.FeatureSuggestionRecord, scans []domain.FeatureSuggestionScanRecord) (int64, []domain.FeatureSuggestionScan, error) {
	if len(scans) == 0 {
		return 0, nil, fmt.Errorf("at least one VirusTotal scan is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO feature_suggestions (user_id,username,title,description_cipher,links_cipher,json_name,json_cipher)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id
	`, suggestion.UserID, suggestion.Username, suggestion.Title, suggestion.DescriptionCipher, suggestion.LinksCipher, suggestion.JSONName, suggestion.JSONCipher).Scan(&id); err != nil {
		return 0, nil, err
	}
	created := make([]domain.FeatureSuggestionScan, 0, len(scans))
	for _, scan := range scans {
		var item domain.FeatureSuggestionScan
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO feature_suggestion_scans (suggestion_id,kind,source_name)
			VALUES ($1,$2,$3) RETURNING id,kind,source_name,status
		`, id, scan.Kind, scan.SourceName).Scan(&item.ID, &item.Kind, &item.SourceName, &item.Status); err != nil {
			return 0, nil, err
		}
		created = append(created, item)
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return id, created, nil
}

func (s *Store) SetFeatureSuggestionScanSubmitted(ctx context.Context, id int64, analysisID, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE feature_suggestion_scans
		SET analysis_id=$2,status=$3,error_message='',updated_at=now()
		WHERE id=$1
	`, id, analysisID, status)
	return err
}

func (s *Store) SetFeatureSuggestionScanError(ctx context.Context, id int64, message string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE feature_suggestion_scans
		SET status='error',error_message=$2,updated_at=now()
		WHERE id=$1
	`, id, message)
	return err
}

func (s *Store) SetFeatureSuggestionScanResult(ctx context.Context, id int64, status string, malicious, suspicious, harmless, undetected, timeout int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE feature_suggestion_scans
		SET status=$2,malicious=$3,suspicious=$4,harmless=$5,undetected=$6,timeout=$7,checked_at=CASE WHEN $2='completed' THEN now() ELSE checked_at END,error_message='',updated_at=now()
		WHERE id=$1
	`, id, status, malicious, suspicious, harmless, undetected, timeout)
	return err
}

func (s *Store) RefreshFeatureSuggestionStatus(ctx context.Context, suggestionID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE feature_suggestions
		SET status=CASE
			WHEN EXISTS (SELECT 1 FROM feature_suggestion_scans WHERE suggestion_id=$1 AND (malicious > 0 OR suspicious > 0)) THEN 'flagged'
			WHEN EXISTS (SELECT 1 FROM feature_suggestion_scans WHERE suggestion_id=$1 AND status='error') THEN 'error'
			WHEN EXISTS (SELECT 1 FROM feature_suggestion_scans WHERE suggestion_id=$1 AND status <> 'completed') THEN 'scanning'
			ELSE 'clean'
		END,updated_at=now()
		WHERE id=$1
	`, suggestionID)
	return err
}

func (s *Store) PendingFeatureSuggestionScans(ctx context.Context, limit int) ([]domain.FeatureSuggestionScan, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,kind,source_name,analysis_id,status,malicious,suspicious,harmless,undetected,timeout,error_message,checked_at
		FROM feature_suggestion_scans
		WHERE analysis_id <> '' AND status IN ('queued','in-progress')
		ORDER BY created_at ASC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.FeatureSuggestionScan, 0)
	for rows.Next() {
		var item domain.FeatureSuggestionScan
		if err := rows.Scan(&item.ID, &item.Kind, &item.SourceName, &item.AnalysisID, &item.Status, &item.Malicious, &item.Suspicious, &item.Harmless, &item.Undetected, &item.Timeout, &item.Error, &item.CheckedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) FeatureSuggestionIDForScan(ctx context.Context, scanID int64) (int64, error) {
	var suggestionID int64
	err := s.db.QueryRowContext(ctx, `SELECT suggestion_id FROM feature_suggestion_scans WHERE id=$1`, scanID).Scan(&suggestionID)
	return suggestionID, err
}

func (s *Store) ListFeatureSuggestions(ctx context.Context, limit int) ([]domain.FeatureSuggestionRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id,COALESCE(s.user_id,0),COALESCE(NULLIF(u.username,''),s.username,'Удалённый пользователь'),s.title,s.description_cipher,s.links_cipher,s.json_name,s.json_cipher,s.status,s.created_at,s.updated_at
		FROM feature_suggestions s
		LEFT JOIN users u ON u.id=s.user_id
		ORDER BY s.created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.FeatureSuggestionRow, 0)
	ids := make([]int64, 0)
	byID := make(map[int64]*domain.FeatureSuggestionRow)
	for rows.Next() {
		var item domain.FeatureSuggestionRow
		if err := rows.Scan(&item.ID, &item.UserID, &item.Username, &item.Title, &item.DescriptionCipher, &item.LinksCipher, &item.JSONName, &item.JSONCipher, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
		ids = append(ids, item.ID)
		byID[item.ID] = &items[len(items)-1]
	}
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return items, err
	}
	scanRows, err := s.db.QueryContext(ctx, `
		SELECT id,suggestion_id,kind,source_name,analysis_id,status,malicious,suspicious,harmless,undetected,timeout,error_message,checked_at
		FROM feature_suggestion_scans WHERE suggestion_id = ANY($1) ORDER BY id ASC
	`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer scanRows.Close()
	for scanRows.Next() {
		var item domain.FeatureSuggestionScan
		var suggestionID int64
		if err := scanRows.Scan(&item.ID, &suggestionID, &item.Kind, &item.SourceName, &item.AnalysisID, &item.Status, &item.Malicious, &item.Suspicious, &item.Harmless, &item.Undetected, &item.Timeout, &item.Error, &item.CheckedAt); err != nil {
			return nil, err
		}
		if suggestion := byID[suggestionID]; suggestion != nil {
			suggestion.Scans = append(suggestion.Scans, item)
		}
	}
	return items, scanRows.Err()
}

func (s *Store) FeatureSuggestionByID(ctx context.Context, id int64) (domain.FeatureSuggestionRow, error) {
	var item domain.FeatureSuggestionRow
	err := s.db.QueryRowContext(ctx, `
		SELECT id,COALESCE(user_id,0),username,title,description_cipher,links_cipher,json_name,json_cipher,status,created_at,updated_at
		FROM feature_suggestions WHERE id=$1
	`, id).Scan(&item.ID, &item.UserID, &item.Username, &item.Title, &item.DescriptionCipher, &item.LinksCipher, &item.JSONName, &item.JSONCipher, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}
