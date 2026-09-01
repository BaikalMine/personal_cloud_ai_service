package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

func (s *Store) InsertContentEvent(ctx context.Context, event domain.ContentEventRecord) (int64, error) {
	if event.ExpiresAt.IsZero() {
		return 0, errors.New("content event expiration is required")
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO content_events
			(user_id, generation_job_id, correlation_id, service, kind, external_id, model, generation_state, prompt_cipher, response_cipher, metadata_cipher, is_sensitive, sensitivity_classified_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12,now(),$13)
		ON CONFLICT DO NOTHING
		RETURNING id
	`, event.UserID, event.GenerationJobID, event.CorrelationID, event.Service, event.Kind, event.ExternalID, event.Model, event.GenerationState,
		event.PromptCipher, event.ResponseCipher, event.MetadataCipher, event.Sensitive, event.ExpiresAt).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) && event.GenerationJobID != nil {
		err = s.db.QueryRowContext(ctx, `SELECT id FROM content_events
			WHERE generation_job_id=$1 AND service=$2 AND kind=$3`, *event.GenerationJobID, event.Service, event.Kind).Scan(&id)
	}
	return id, err
}

func (s *Store) ContentRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT revision FROM content_change_revision WHERE id=1`).Scan(&revision)
	return revision, err
}

func (s *Store) ListContentEvents(ctx context.Context, limit int, username, service string) ([]domain.ContentEventRow, error) {
	limit = boundedLimit(limit, 1, 500)
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, COALESCE(e.user_id,0), COALESCE(u.username,'Удалённый пользователь'), e.generation_job_id,e.correlation_id,e.service, e.kind, COALESCE(e.external_id,''), e.model,
		       e.generation_state, e.prompt_cipher, e.response_cipher, e.metadata_cipher, e.is_sensitive,
		       e.generated_media_count, COALESCE(e.media_expires_at,'epoch'::timestamptz), COUNT(m.id), e.created_at, e.expires_at
		FROM content_events e
		LEFT JOIN users u ON u.id = e.user_id
		LEFT JOIN content_media m ON m.event_id = e.id AND m.expires_at > now()
		WHERE e.expires_at > now()
		  AND e.kind <> 'prompt_assistant'
		  AND ($2 = '' OR COALESCE(u.username,'Удалённый пользователь') ILIKE '%' || $2 || '%')
		  AND ($3 = '' OR e.service = $3)
		GROUP BY e.id, u.username
		ORDER BY e.created_at DESC
		LIMIT $1
	`, limit, username, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []domain.ContentEventRow
	for rows.Next() {
		var event domain.ContentEventRow
		var generationJobID sql.NullInt64
		if err := rows.Scan(&event.ID, &event.UserID, &event.Username, &generationJobID, &event.CorrelationID, &event.Service, &event.Kind,
			&event.ExternalID, &event.Model, &event.GenerationState, &event.PromptCipher, &event.ResponseCipher,
			&event.MetadataCipher, &event.Sensitive, &event.GeneratedMediaCount, &event.MediaExpiresAt, &event.MediaCount, &event.CreatedAt, &event.ExpiresAt); err != nil {
			return nil, err
		}
		if generationJobID.Valid {
			value := generationJobID.Int64
			event.GenerationJobID = &value
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) ListUnclassifiedSensitiveContent(ctx context.Context, limit int) ([]domain.ContentEventRow, error) {
	limit = boundedLimit(limit, 1, 500)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,prompt_cipher,response_cipher,metadata_cipher
		FROM content_events
		WHERE sensitivity_classified_at IS NULL AND expires_at > now()
		ORDER BY created_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ContentEventRow, 0)
	for rows.Next() {
		var item domain.ContentEventRow
		if err := rows.Scan(&item.ID, &item.PromptCipher, &item.ResponseCipher, &item.MetadataCipher); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetContentEventSensitive(ctx context.Context, id int64, sensitive bool) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE content_events
		SET is_sensitive=$2, sensitivity_classified_at=now()
		WHERE id=$1 AND sensitivity_classified_at IS NULL
	`, id, sensitive)
	return err
}

func (s *Store) ListContentMediaSummaries(ctx context.Context, eventIDs []int64) (map[int64][]domain.ContentMediaSummary, error) {
	result := make(map[int64][]domain.ContentMediaSummary)
	if len(eventIDs) == 0 {
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,event_id,media_type,(media_type='image' AND visual_sensitivity_classified_at IS NULL)
		FROM content_media
		WHERE event_id = ANY($1) AND expires_at > now()
		ORDER BY event_id,created_at,id
		LIMIT 1000
	`, pq.Array(eventIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var media domain.ContentMediaSummary
		if err := rows.Scan(&media.ID, &media.EventID, &media.MediaType, &media.VisualPending); err != nil {
			return nil, err
		}
		result[media.EventID] = append(result[media.EventID], media)
	}
	return result, rows.Err()
}

func (s *Store) LatestComfyContentEventID(ctx context.Context, userID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM content_events
		WHERE user_id=$1 AND service='comfyui' AND kind='comfyui_prompt'
		  AND created_at >= now() - interval '2 hours' AND expires_at > now()
		ORDER BY created_at DESC LIMIT 1
	`, userID).Scan(&id)
	return id, err
}

func (s *Store) InsertContentMedia(ctx context.Context, media domain.ContentMediaRecord) error {
	if media.ExpiresAt.IsZero() {
		return errors.New("content media expiration is required")
	}
	var expiresAtStored time.Time
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO content_media(event_id,media_type,mime_type,original_name,subfolder,storage_type,payload_cipher,size_bytes,content_hash,expires_at,storage_format)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'inline_v1')
		ON CONFLICT (event_id,original_name,subfolder,storage_type) DO NOTHING
		RETURNING expires_at
	`, media.EventID, media.MediaType, media.MIMEType, media.OriginalName, media.Subfolder,
		media.StorageType, media.PayloadCipher, media.SizeBytes, media.ContentHash, media.ExpiresAt).Scan(&expiresAtStored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE content_events
		SET generated_media_count=generated_media_count+1,
		    media_expires_at=GREATEST(COALESCE(media_expires_at,'epoch'::timestamptz),$2)
		WHERE id=$1
	`, media.EventID, expiresAtStored)
	return err
}

func (s *Store) SetContentGenerationState(ctx context.Context, userID int64, promptID, state string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE content_events
		SET generation_state=$3
		WHERE user_id=$1 AND service='comfyui' AND external_id=$2
	`, userID, promptID, state)
	return err
}

func (s *Store) ListPendingVisualSensitiveMedia(ctx context.Context, limit int) ([]domain.PendingSensitiveMedia, error) {
	limit = boundedLimit(limit, 1, 20)
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id,m.event_id,e.generation_job_id,e.correlation_id,m.mime_type,m.payload_cipher,
		       m.size_bytes,m.storage_format
		FROM content_media m
		JOIN content_events e ON e.id=m.event_id
		WHERE m.media_type='image' AND m.visual_sensitivity_classified_at IS NULL
		  AND m.expires_at > now() AND e.expires_at > now()
		ORDER BY m.created_at ASC,m.id ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.PendingSensitiveMedia, 0)
	for rows.Next() {
		var item domain.PendingSensitiveMedia
		if err := rows.Scan(&item.ID, &item.EventID, &item.GenerationJobID, &item.CorrelationID, &item.MIMEType,
			&item.PayloadCipher, &item.SizeBytes, &item.StorageFormat); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetContentMediaVisualSensitive(ctx context.Context, mediaID, eventID int64, sensitive bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE content_media
		SET visual_sensitivity_classified_at=now()
		WHERE id=$1 AND event_id=$2 AND visual_sensitivity_classified_at IS NULL
	`, mediaID, eventID); err != nil {
		return err
	}
	if sensitive {
		if _, err := tx.ExecContext(ctx, `UPDATE content_events SET is_sensitive=TRUE WHERE id=$1`, eventID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) InsertComfyOutputOwnerships(ctx context.Context, userID int64, outputs []domain.ComfyOutputOwnership) error {
	if len(outputs) == 0 {
		return nil
	}
	for _, output := range outputs {
		if output.ExpiresAt.IsZero() {
			return errors.New("ComfyUI output ownership expiration is required")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO comfy_output_ownership
			(event_id,user_id,prompt_id,filename,subfolder,storage_type,media_type,expires_at)
		SELECT e.id,e.user_id,$2,$3,$4,$5,$6,$7
		FROM content_events e
		WHERE e.user_id=$1 AND e.service='comfyui' AND e.external_id=$2 AND e.expires_at > now()
		ORDER BY e.created_at DESC LIMIT 1
		ON CONFLICT (event_id,filename,subfolder,storage_type) DO NOTHING
	`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, output := range outputs {
		if _, err := statement.ExecContext(ctx, userID, output.PromptID, output.Filename,
			output.Subfolder, output.StorageType, output.MediaType, output.ExpiresAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListUserGenerationMedia(ctx context.Context, userID int64, limit int) ([]domain.UserGenerationMedia, error) {
	return s.listUserGenerationMedia(ctx, userID, "", limit)
}

func (s *Store) ListUserGenerationImages(ctx context.Context, userID int64, limit int) ([]domain.UserGenerationMedia, error) {
	return s.listUserGenerationMedia(ctx, userID, "image", limit)
}

func (s *Store) listUserGenerationMedia(ctx context.Context, userID int64, mediaType string, limit int) ([]domain.UserGenerationMedia, error) {
	limit = boundedLimit(limit, 1, 100)
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id,m.media_type,m.mime_type,m.original_name,e.model,m.size_bytes,m.created_at,m.expires_at,e.is_sensitive,
		       (m.media_type='image' AND m.visual_sensitivity_classified_at IS NULL),
		       (m.pinned_at IS NOT NULL),(m.favorite_at IS NOT NULL)
		FROM content_media m
		JOIN content_events e ON e.id=m.event_id
		WHERE e.user_id=$1 AND e.service='comfyui' AND e.kind='comfyui_prompt'
		  AND e.expires_at > now() AND m.expires_at > now() AND m.profile_hidden_at IS NULL
		  AND ($3='' OR m.media_type=$3)
		ORDER BY (m.pinned_at IS NOT NULL) DESC,m.created_at DESC,m.id DESC
		LIMIT $2
	`, userID, limit, mediaType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var media []domain.UserGenerationMedia
	for rows.Next() {
		var item domain.UserGenerationMedia
		if err := rows.Scan(&item.ID, &item.MediaType, &item.MIMEType, &item.OriginalName, &item.ModelName, &item.SizeBytes,
			&item.CreatedAt, &item.ExpiresAt, &item.Sensitive, &item.VisualPending, &item.Pinned, &item.Favorite); err != nil {
			return nil, err
		}
		media = append(media, item)
	}
	return media, rows.Err()
}

func (s *Store) ContentMediaByIDForUser(ctx context.Context, id, userID int64) (domain.ContentMediaRow, error) {
	var media domain.ContentMediaRow
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id,m.media_type,m.mime_type,m.original_name,m.payload_cipher,m.size_bytes,m.storage_format
		FROM content_media m
		JOIN content_events e ON e.id=m.event_id
		WHERE m.id=$1 AND e.user_id=$2 AND e.service='comfyui' AND e.kind='comfyui_prompt'
		  AND e.expires_at > now() AND m.expires_at > now() AND m.profile_hidden_at IS NULL
	`, id, userID).Scan(&media.ID, &media.MediaType, &media.MIMEType, &media.OriginalName, &media.PayloadCipher, &media.SizeBytes, &media.StorageFormat)
	return media, err
}

func (s *Store) HideGenerationMediaForUser(ctx context.Context, id, userID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE content_media m
		SET profile_hidden_at=now()
		FROM content_events e
		WHERE m.event_id=e.id AND m.id=$1 AND e.user_id=$2
		  AND e.service='comfyui' AND e.kind='comfyui_prompt'
		  AND m.expires_at > now() AND m.profile_hidden_at IS NULL
	`, id, userID)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (s *Store) ComfyOutputOwner(ctx context.Context, filename, subfolder, storageType string) (int64, bool, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT user_id FROM comfy_output_ownership
		WHERE filename=$1 AND subfolder=$2 AND storage_type=$3 AND expires_at > now()
		ORDER BY created_at DESC,id DESC LIMIT 1
	`, filename, subfolder, storageType).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return userID, err == nil, err
}

func (s *Store) ComfyOutputEventID(ctx context.Context, userID int64, filename, subfolder, storageType string) (int64, error) {
	var eventID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT event_id FROM comfy_output_ownership
		WHERE user_id=$1 AND filename=$2 AND subfolder=$3 AND storage_type=$4 AND expires_at > now()
		ORDER BY created_at DESC,id DESC LIMIT 1
	`, userID, filename, subfolder, storageType).Scan(&eventID)
	return eventID, err
}

func (s *Store) UnarchivedComfyOutputs(ctx context.Context, limit int) ([]domain.ComfyOutputArchiveCandidate, error) {
	limit = boundedLimit(limit, 1, 50)
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.user_id,o.filename,o.subfolder,o.storage_type,o.media_type
		FROM comfy_output_ownership o
		JOIN content_events e ON e.id=o.event_id AND e.expires_at > now()
		LEFT JOIN content_media m ON m.event_id=o.event_id AND m.original_name=o.filename
		  AND m.subfolder=o.subfolder AND m.storage_type=o.storage_type AND m.expires_at > now()
		WHERE o.expires_at > now() AND m.id IS NULL
		ORDER BY o.created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ComfyOutputArchiveCandidate
	for rows.Next() {
		var item domain.ComfyOutputArchiveCandidate
		if err := rows.Scan(&item.UserID, &item.Filename, &item.Subfolder, &item.StorageType, &item.MediaType); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ContentMediaBytesForUser(ctx context.Context, userID int64) (int64, error) {
	var bytes int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(m.size_bytes),0)
		FROM content_media m JOIN content_events e ON e.id=m.event_id
		WHERE e.user_id=$1 AND m.expires_at > now() AND e.expires_at > now()
	`, userID).Scan(&bytes)
	return bytes, err
}

func (s *Store) ContentRetentionStats(ctx context.Context) (domain.ContentRetentionStats, error) {
	var stats domain.ContentRetentionStats
	var nextEventExpiry, nextMediaExpiry sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM content_events WHERE expires_at > now()),
			(SELECT COUNT(*) FROM content_media m JOIN content_events e ON e.id=m.event_id
			 WHERE m.expires_at > now() AND e.expires_at > now()),
			(SELECT COALESCE(SUM(m.size_bytes),0) FROM content_media m JOIN content_events e ON e.id=m.event_id
			 WHERE m.expires_at > now() AND e.expires_at > now()),
			(SELECT MIN(expires_at) FROM content_events WHERE expires_at > now()),
			(SELECT MIN(m.expires_at) FROM content_media m JOIN content_events e ON e.id=m.event_id
			 WHERE m.expires_at > now() AND e.expires_at > now())
	`).Scan(&stats.EventCount, &stats.MediaCount, &stats.MediaBytes, &nextEventExpiry, &nextMediaExpiry)
	if err != nil {
		return domain.ContentRetentionStats{}, err
	}
	if nextEventExpiry.Valid {
		value := nextEventExpiry.Time
		stats.NextEventExpiry = &value
	}
	if nextMediaExpiry.Valid {
		value := nextMediaExpiry.Time
		stats.NextMediaExpiry = &value
	}
	return stats, nil
}

func (s *Store) ContentMediaByID(ctx context.Context, id int64) (domain.ContentMediaRow, error) {
	var media domain.ContentMediaRow
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id,m.media_type,m.mime_type,m.original_name,m.payload_cipher,m.size_bytes,m.storage_format
		FROM content_media m JOIN content_events e ON e.id=m.event_id
		WHERE m.id=$1 AND m.expires_at > now() AND e.expires_at > now()
	`, id).Scan(&media.ID, &media.MediaType, &media.MIMEType, &media.OriginalName, &media.PayloadCipher, &media.SizeBytes, &media.StorageFormat)
	return media, err
}

func (s *Store) DeleteExpiredMedia(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM content_media WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ExpiredComfyMedia(ctx context.Context, limit int) ([]domain.ExpiredComfyMedia, error) {
	limit = boundedLimit(limit, 1, 100)
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id,m.original_name,m.subfolder,m.storage_type,m.size_bytes,m.content_hash,
		       EXISTS(
			   SELECT 1 FROM comfy_output_ownership o
			   WHERE o.event_id=m.event_id AND o.filename=m.original_name
			     AND o.subfolder=m.subfolder AND o.storage_type=m.storage_type
		       )
		FROM content_media m
		JOIN content_events e ON e.id=m.event_id
		WHERE e.service='comfyui' AND m.expires_at <= now()
		ORDER BY m.expires_at,m.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ExpiredComfyMedia, 0)
	for rows.Next() {
		var item domain.ExpiredComfyMedia
		if err := rows.Scan(&item.ID, &item.Filename, &item.Subfolder, &item.StorageType, &item.SizeBytes, &item.ContentHash, &item.HasOwnership); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteContentMediaByIDs(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM content_media WHERE id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) DeleteExpiredNonComfyMedia(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM content_media m
		USING content_events e
		WHERE m.event_id=e.id AND e.service<>'comfyui' AND m.expires_at <= now()
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) UnhashedComfyMedia(ctx context.Context, limit int) ([]domain.UnhashedComfyMedia, error) {
	limit = boundedLimit(limit, 1, 100)
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id,m.payload_cipher,m.size_bytes,m.storage_format
		FROM content_media m
		JOIN content_events e ON e.id=m.event_id
		WHERE e.service='comfyui' AND m.expires_at > now() AND m.content_hash='' AND m.storage_format='inline_v1'
		ORDER BY m.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.UnhashedComfyMedia, 0)
	for rows.Next() {
		var item domain.UnhashedComfyMedia
		if err := rows.Scan(&item.ID, &item.PayloadCipher, &item.SizeBytes, &item.StorageFormat); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetContentMediaHash(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE content_media SET content_hash=$2 WHERE id=$1 AND content_hash=''`, id, hash)
	return err
}

func (s *Store) DeleteExpiredContent(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM content_events WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ContentEventOwnedByUser(ctx context.Context, service, externalID string, userID int64) (bool, error) {
	var owned bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM content_events
			WHERE service=$1 AND external_id=$2 AND user_id=$3 AND expires_at > $4
		)
	`, service, externalID, userID, time.Now()).Scan(&owned)
	return owned, err
}

func (s *Store) ComfyPromptIDsForUser(ctx context.Context, userID int64) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT external_id FROM content_events
		WHERE user_id=$1 AND service='comfyui' AND external_id IS NOT NULL AND expires_at > now()
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

func (s *Store) ContentMediaOwnedByAnotherUser(ctx context.Context, name, subfolder, storageType string, userID int64) (bool, error) {
	var denied bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM content_media m JOIN content_events e ON e.id=m.event_id
			WHERE m.original_name=$1 AND m.subfolder=$2 AND m.storage_type=$3 AND e.user_id<>$4
			  AND m.expires_at > now() AND e.expires_at > now()
		)
	`, name, subfolder, storageType, userID).Scan(&denied)
	return denied, err
}
