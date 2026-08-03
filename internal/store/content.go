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
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO content_events
			(user_id, service, kind, external_id, model, prompt_cipher, response_cipher, metadata_cipher)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8)
		RETURNING id
	`, event.UserID, event.Service, event.Kind, event.ExternalID, event.Model,
		event.PromptCipher, event.ResponseCipher, event.MetadataCipher).Scan(&id)
	return id, err
}

func (s *Store) ListContentEvents(ctx context.Context, limit int, username, service string) ([]domain.ContentEventRow, error) {
	limit = boundedLimit(limit, 1, 500)
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.user_id, u.username, e.service, e.kind, COALESCE(e.external_id,''), e.model,
		       e.prompt_cipher, e.response_cipher, e.metadata_cipher, COUNT(m.id),
		       e.created_at, e.expires_at
		FROM content_events e
		JOIN users u ON u.id = e.user_id
		LEFT JOIN content_media m ON m.event_id = e.id AND m.expires_at > now()
		WHERE e.expires_at > now()
		  AND ($2 = '' OR u.username ILIKE '%' || $2 || '%')
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
		if err := rows.Scan(&event.ID, &event.UserID, &event.Username, &event.Service, &event.Kind,
			&event.ExternalID, &event.Model, &event.PromptCipher, &event.ResponseCipher,
			&event.MetadataCipher, &event.MediaCount, &event.CreatedAt, &event.ExpiresAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) ListContentMediaSummaries(ctx context.Context, eventIDs []int64) (map[int64][]domain.ContentMediaSummary, error) {
	result := make(map[int64][]domain.ContentMediaSummary)
	if len(eventIDs) == 0 {
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,event_id,media_type
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
		if err := rows.Scan(&media.ID, &media.EventID, &media.MediaType); err != nil {
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO content_media(event_id,media_type,mime_type,original_name,subfolder,storage_type,payload_cipher,size_bytes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (event_id,original_name,subfolder,storage_type) DO NOTHING
	`, media.EventID, media.MediaType, media.MIMEType, media.OriginalName, media.Subfolder,
		media.StorageType, media.PayloadCipher, media.SizeBytes)
	return err
}

func (s *Store) InsertComfyOutputOwnerships(ctx context.Context, userID int64, outputs []domain.ComfyOutputOwnership) error {
	if len(outputs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO comfy_output_ownership
			(event_id,user_id,prompt_id,filename,subfolder,storage_type,media_type)
		SELECT e.id,e.user_id,$2,$3,$4,$5,$6
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
			output.Subfolder, output.StorageType, output.MediaType); err != nil {
			return err
		}
	}
	return tx.Commit()
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

func (s *Store) ContentMediaBytesForUser(ctx context.Context, userID int64) (int64, error) {
	var bytes int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(m.size_bytes),0)
		FROM content_media m JOIN content_events e ON e.id=m.event_id
		WHERE e.user_id=$1 AND m.expires_at > now() AND e.expires_at > now()
	`, userID).Scan(&bytes)
	return bytes, err
}

func (s *Store) ContentMediaByID(ctx context.Context, id int64) (domain.ContentMediaRow, error) {
	var media domain.ContentMediaRow
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id,m.media_type,m.mime_type,m.original_name,m.payload_cipher
		FROM content_media m JOIN content_events e ON e.id=m.event_id
		WHERE m.id=$1 AND m.expires_at > now() AND e.expires_at > now()
	`, id).Scan(&media.ID, &media.MediaType, &media.MIMEType, &media.OriginalName, &media.PayloadCipher)
	return media, err
}

func (s *Store) DeleteExpiredMedia(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM content_media WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
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
