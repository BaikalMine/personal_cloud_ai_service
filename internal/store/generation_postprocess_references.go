package store

import (
	"context"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

// The cursor advances over scanned metadata, including rows excluded by current
// family permissions. It never substitutes a newer media item for a selected ID.
func (s *Store) ListGenerationPostprocessReferences(ctx context.Context, userID, excludeID, beforeID int64, search string) ([]domain.GenerationPostprocessReference, int64, error) {
	if userID <= 0 || excludeID < 0 || beforeID < 0 || len(search) > 200 {
		return nil, 0, ErrPostprocessConflict
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	if u.Disabled || u.AccountExpiresAt.Valid && !u.AccountExpiresAt.Time.After(time.Now()) || !u.CanUseService("quick_generation") || !u.CanUseQuickGenerationType(domain.PostprocessImageTemplate) {
		return nil, 0, ErrQuickGenerationForbidden
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT ON (m.id) m.id,j.public_id,j.template_id,m.original_name,m.size_bytes,m.created_at,LEAST(e.expires_at,m.expires_at),
	 (e.is_sensitive OR m.visual_sensitivity_classified_at IS NULL) `+generationOutputSourceBase+`
	 AND j.state='completed' AND m.media_type='image' AND m.mime_type IN ('image/png','image/jpeg','image/webp')
	 AND m.size_bytes>0 AND m.size_bytes<=$5 AND m.id<>$2 AND ($3::bigint=0 OR m.id<$3)
	 AND ($4='' OR strpos(lower(m.original_name),lower($4))>0)
	 ORDER BY m.id DESC,j.id LIMIT 51`, userID, excludeID, beforeID, strings.TrimSpace(search), 512<<20)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []domain.GenerationPostprocessReference{}
	var lastID int64
	count := 0
	for rows.Next() {
		var item domain.GenerationPostprocessReference
		var templateID string
		if err := rows.Scan(&item.MediaID, &item.JobPublicID, &templateID, &item.Filename, &item.SizeBytes, &item.CreatedAt, &item.ExpiresAt, &item.Sensitive); err != nil {
			return nil, 0, err
		}
		if count == 50 {
			return result, lastID, rows.Err()
		}
		count++
		lastID = item.MediaID
		if u.CanUseQuickGenerationType(templateID) {
			result = append(result, item)
		}
	}
	return result, 0, rows.Err()
}
