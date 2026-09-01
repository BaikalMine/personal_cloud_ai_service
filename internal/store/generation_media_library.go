package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

const (
	maxGenerationMediaCollections = 40
	maxGenerationMediaTags        = 8
	maxMediaCollectionAssignments = 12
)

type generationMediaLocation struct {
	promptID string
	index    int
}

func (s *Store) ListGenerationMediaForPrompts(ctx context.Context, userID int64, promptIDs []string) (map[string][]domain.GenerationVariantMedia, error) {
	result := make(map[string][]domain.GenerationVariantMedia)
	promptIDs = uniqueStrings(promptIDs)
	if len(promptIDs) == 0 {
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.external_id,m.id,m.media_type,m.mime_type,m.original_name,m.size_bytes,m.created_at,m.expires_at,
		       e.is_sensitive,(m.media_type='image' AND m.visual_sensitivity_classified_at IS NULL),
		       (m.pinned_at IS NOT NULL),(m.favorite_at IS NOT NULL),e.generation_job_id,COALESCE(j.public_id,'')
		FROM content_media m
		JOIN content_events e ON e.id=m.event_id
		LEFT JOIN generation_jobs j ON j.id=e.generation_job_id
		WHERE e.user_id=$1 AND e.service='comfyui' AND e.kind='comfyui_prompt'
		  AND e.external_id=ANY($2) AND e.expires_at > now() AND m.expires_at > now()
		  AND m.profile_hidden_at IS NULL
		ORDER BY m.created_at DESC,m.id DESC
	`, userID, pq.Array(promptIDs))
	if err != nil {
		return nil, err
	}
	locations := make(map[int64]generationMediaLocation)
	mediaIDs := make([]int64, 0)
	for rows.Next() {
		var item domain.GenerationVariantMedia
		var generationJobID sql.NullInt64
		if err := rows.Scan(&item.PromptID, &item.ID, &item.MediaType, &item.MIMEType, &item.Filename, &item.SizeBytes,
			&item.CreatedAt, &item.ExpiresAt, &item.Sensitive, &item.VisualPending, &item.Pinned, &item.Favorite,
			&generationJobID, &item.GenerationJobPublicID); err != nil {
			rows.Close()
			return nil, err
		}
		if generationJobID.Valid {
			value := generationJobID.Int64
			item.GenerationJobID = &value
		}
		result[item.PromptID] = append(result[item.PromptID], item)
		locations[item.ID] = generationMediaLocation{promptID: item.PromptID, index: len(result[item.PromptID]) - 1}
		mediaIDs = append(mediaIDs, item.ID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(mediaIDs) == 0 {
		return result, nil
	}
	if err := s.loadGenerationMediaTags(ctx, userID, mediaIDs, locations, result); err != nil {
		return nil, err
	}
	if err := s.loadGenerationMediaCollections(ctx, userID, mediaIDs, locations, result); err != nil {
		return nil, err
	}
	if err := s.loadGenerationMediaReferenceUses(ctx, userID, mediaIDs, locations, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) loadGenerationMediaTags(ctx context.Context, userID int64, mediaIDs []int64, locations map[int64]generationMediaLocation, result map[string][]domain.GenerationVariantMedia) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT media_id,tag FROM generation_media_tags
		WHERE user_id=$1 AND media_id=ANY($2)
		ORDER BY media_id,tag_key
	`, userID, pq.Array(mediaIDs))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var mediaID int64
		var tag string
		if err := rows.Scan(&mediaID, &tag); err != nil {
			return err
		}
		location, ok := locations[mediaID]
		if !ok {
			continue
		}
		item := result[location.promptID][location.index]
		item.Tags = append(item.Tags, tag)
		result[location.promptID][location.index] = item
	}
	return rows.Err()
}

func (s *Store) loadGenerationMediaCollections(ctx context.Context, userID int64, mediaIDs []int64, locations map[int64]generationMediaLocation, result map[string][]domain.GenerationVariantMedia) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT item.media_id,collection.id,collection.name,collection.created_at,collection.updated_at
		FROM generation_media_collection_items item
		JOIN generation_media_collections collection ON collection.id=item.collection_id
		WHERE collection.user_id=$1 AND item.media_id=ANY($2)
		ORDER BY item.media_id,collection.name_key,collection.id
	`, userID, pq.Array(mediaIDs))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var mediaID int64
		var collection domain.GenerationMediaCollection
		if err := rows.Scan(&mediaID, &collection.ID, &collection.Name, &collection.CreatedAt, &collection.UpdatedAt); err != nil {
			return err
		}
		location, ok := locations[mediaID]
		if !ok {
			continue
		}
		item := result[location.promptID][location.index]
		item.Collections = append(item.Collections, collection)
		result[location.promptID][location.index] = item
	}
	return rows.Err()
}

func (s *Store) loadGenerationMediaReferenceUses(ctx context.Context, userID int64, mediaIDs []int64, locations map[int64]generationMediaLocation, result map[string][]domain.GenerationVariantMedia) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT reference.source_media_id,job.id,job.public_id,job.template_id,job.workflow_id,
		       reference.reference_number,reference.reference_role,reference.created_at
		FROM generation_media_references reference
		JOIN generation_jobs job ON job.id=reference.target_job_id
		WHERE job.user_id=$1 AND reference.source_media_id=ANY($2)
		ORDER BY reference.source_media_id,reference.created_at DESC,reference.id DESC
	`, userID, pq.Array(mediaIDs))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var mediaID int64
		var use domain.GenerationMediaReferenceUse
		if err := rows.Scan(&mediaID, &use.JobID, &use.JobPublicID, &use.TemplateID, &use.WorkflowID, &use.Number, &use.Role, &use.CreatedAt); err != nil {
			return err
		}
		location, ok := locations[mediaID]
		if !ok {
			continue
		}
		item := result[location.promptID][location.index]
		item.ReferenceUses = append(item.ReferenceUses, use)
		result[location.promptID][location.index] = item
	}
	return rows.Err()
}

func (s *Store) ListGenerationMediaCollections(ctx context.Context, userID int64) ([]domain.GenerationMediaCollection, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT collection.id,collection.name,COUNT(media.id),collection.created_at,collection.updated_at
		FROM generation_media_collections collection
		LEFT JOIN generation_media_collection_items item ON item.collection_id=collection.id
		LEFT JOIN content_media media ON media.id=item.media_id AND media.expires_at > now() AND media.profile_hidden_at IS NULL
		WHERE collection.user_id=$1
		GROUP BY collection.id
		ORDER BY collection.name_key,collection.id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.GenerationMediaCollection, 0)
	for rows.Next() {
		var item domain.GenerationMediaCollection
		if err := rows.Scan(&item.ID, &item.Name, &item.ItemCount, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateGenerationMediaCollection(ctx context.Context, userID int64, name string) (domain.GenerationMediaCollection, error) {
	displayName, nameKey, err := normalizeGenerationLibraryText(name, 80)
	if err != nil {
		return domain.GenerationMediaCollection{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GenerationMediaCollection{}, err
	}
	defer tx.Rollback()
	var lockedUserID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&lockedUserID); err != nil {
		return domain.GenerationMediaCollection{}, err
	}
	var item domain.GenerationMediaCollection
	err = tx.QueryRowContext(ctx, `
		INSERT INTO generation_media_collections(user_id,name,name_key)
		SELECT $1,$2,$3
		WHERE EXISTS (SELECT 1 FROM generation_media_collections WHERE user_id=$1 AND name_key=$3)
		   OR (SELECT COUNT(*) FROM generation_media_collections WHERE user_id=$1) < $4
		ON CONFLICT(user_id,name_key) DO UPDATE SET name=EXCLUDED.name,updated_at=now()
		RETURNING id,name,created_at,updated_at
	`, userID, displayName, nameKey, maxGenerationMediaCollections).Scan(&item.ID, &item.Name, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationMediaCollection{}, fmt.Errorf("достигнут лимит коллекций: %d", maxGenerationMediaCollections)
	}
	if err != nil {
		return domain.GenerationMediaCollection{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.GenerationMediaCollection{}, err
	}
	return item, nil
}

func (s *Store) DeleteGenerationMediaCollection(ctx context.Context, userID, collectionID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM generation_media_collections WHERE id=$1 AND user_id=$2`, collectionID, userID)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (s *Store) UpdateGenerationMediaMetadata(ctx context.Context, userID, mediaID int64, tags []string, collectionIDs []int64) error {
	normalizedTags, err := normalizeGenerationMediaTags(tags)
	if err != nil {
		return err
	}
	collectionIDs, err = normalizeGenerationCollectionIDs(collectionIDs)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owned bool
	if err := tx.QueryRowContext(ctx, `
		SELECT TRUE FROM content_media media JOIN content_events event ON event.id=media.event_id
		WHERE media.id=$1 AND event.user_id=$2 AND event.service='comfyui' AND event.kind='comfyui_prompt'
		  AND media.expires_at > now() AND event.expires_at > now() AND media.profile_hidden_at IS NULL
		FOR UPDATE OF media
	`, mediaID, userID).Scan(&owned); err != nil {
		return err
	}
	if len(collectionIDs) > 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM generation_media_collections WHERE user_id=$1 AND id=ANY($2)`, userID, pq.Array(collectionIDs)).Scan(&count); err != nil {
			return err
		}
		if count != len(collectionIDs) {
			return errors.New("одна из выбранных коллекций недоступна")
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM generation_media_tags WHERE media_id=$1 AND user_id=$2`, mediaID, userID); err != nil {
		return err
	}
	for _, tag := range normalizedTags {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO generation_media_tags(media_id,user_id,tag,tag_key) VALUES($1,$2,$3,$4)
		`, mediaID, userID, tag.display, tag.key); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM generation_media_collection_items item USING generation_media_collections collection
		WHERE item.collection_id=collection.id AND item.media_id=$1 AND collection.user_id=$2
	`, mediaID, userID); err != nil {
		return err
	}
	for _, collectionID := range collectionIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO generation_media_collection_items(collection_id,media_id) VALUES($1,$2)
		`, collectionID, mediaID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetGenerationMediaFavorite(ctx context.Context, userID, mediaID int64, favorite bool) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE content_media media SET favorite_at=CASE WHEN $3 THEN COALESCE(favorite_at,now()) ELSE NULL END
		FROM content_events event
		WHERE media.event_id=event.id AND media.id=$1 AND event.user_id=$2
		  AND event.service='comfyui' AND event.kind='comfyui_prompt'
		  AND media.expires_at > now() AND event.expires_at > now() AND media.profile_hidden_at IS NULL
	`, mediaID, userID, favorite)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (s *Store) SetGenerationMediaPinned(ctx context.Context, userID, mediaID int64, pinned bool, regularUntil, pinnedUntil time.Time) (time.Time, bool, error) {
	if regularUntil.IsZero() || pinnedUntil.IsZero() || pinnedUntil.Before(regularUntil) {
		return time.Time{}, false, errors.New("invalid generation media retention boundary")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, false, err
	}
	defer tx.Rollback()
	var eventID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT media.event_id FROM content_media media JOIN content_events event ON event.id=media.event_id
		WHERE media.id=$1 AND event.user_id=$2 AND event.service='comfyui' AND event.kind='comfyui_prompt'
		  AND media.expires_at > now() AND event.expires_at > now() AND media.profile_hidden_at IS NULL
		FOR UPDATE OF media,event
	`, mediaID, userID).Scan(&eventID); err != nil {
		return time.Time{}, false, err
	}
	var expiresAt time.Time
	if pinned {
		if err := tx.QueryRowContext(ctx, `
			UPDATE content_media SET pinned_at=COALESCE(pinned_at,now()),expires_at=GREATEST(expires_at,$2)
			WHERE id=$1 RETURNING expires_at
		`, mediaID, pinnedUntil).Scan(&expiresAt); err != nil {
			return time.Time{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE content_events SET expires_at=GREATEST(expires_at,$2),media_expires_at=GREATEST(COALESCE(media_expires_at,'epoch'::timestamptz),$2)
			WHERE id=$1
		`, eventID, pinnedUntil); err != nil {
			return time.Time{}, false, err
		}
	} else {
		if err := tx.QueryRowContext(ctx, `
			UPDATE content_media SET pinned_at=NULL,expires_at=LEAST(expires_at,$2)
			WHERE id=$1 RETURNING expires_at
		`, mediaID, regularUntil).Scan(&expiresAt); err != nil {
			return time.Time{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE content_events event SET media_expires_at=summary.expires_at
			FROM (SELECT MAX(expires_at) AS expires_at FROM content_media WHERE event_id=$1) summary
			WHERE event.id=$1
		`, eventID); err != nil {
			return time.Time{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, false, err
	}
	return expiresAt, true, nil
}

func (s *Store) HideGenerationMediaForUserBulk(ctx context.Context, userID int64, mediaIDs []int64) (int64, error) {
	mediaIDs = uniqueInt64s(mediaIDs)
	if len(mediaIDs) == 0 || len(mediaIDs) > 100 {
		return 0, errors.New("выберите от 1 до 100 результатов")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE content_media media SET profile_hidden_at=now()
		FROM content_events event
		WHERE media.event_id=event.id AND media.id=ANY($1) AND event.user_id=$2
		  AND event.service='comfyui' AND event.kind='comfyui_prompt'
		  AND media.expires_at > now() AND media.profile_hidden_at IS NULL
	`, pq.Array(mediaIDs), userID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) GenerationMediaByIDsForUser(ctx context.Context, userID int64, mediaIDs []int64) ([]domain.ContentMediaRow, error) {
	mediaIDs = uniqueInt64s(mediaIDs)
	if len(mediaIDs) == 0 || len(mediaIDs) > 100 {
		return nil, errors.New("выберите от 1 до 100 результатов")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT media.id,media.media_type,media.mime_type,media.original_name,media.payload_cipher,media.size_bytes,media.storage_format
		FROM content_media media JOIN content_events event ON event.id=media.event_id
		WHERE media.id=ANY($1) AND event.user_id=$2 AND event.service='comfyui' AND event.kind='comfyui_prompt'
		  AND media.expires_at > now() AND event.expires_at > now() AND media.profile_hidden_at IS NULL
		ORDER BY media.created_at,media.id
	`, pq.Array(mediaIDs), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ContentMediaRow, 0, len(mediaIDs))
	for rows.Next() {
		var item domain.ContentMediaRow
		if err := rows.Scan(&item.ID, &item.MediaType, &item.MIMEType, &item.OriginalName, &item.PayloadCipher, &item.SizeBytes, &item.StorageFormat); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ReplaceGenerationMediaReferencesForJob(ctx context.Context, userID, jobID int64, references []domain.GenerationMediaReferenceRecord) error {
	if jobID <= 0 || len(references) > 4 {
		return errors.New("invalid generation media references")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owned bool
	if err := tx.QueryRowContext(ctx, `SELECT TRUE FROM generation_jobs WHERE id=$1 AND user_id=$2 FOR UPDATE`, jobID, userID).Scan(&owned); err != nil {
		return err
	}
	seen := make(map[int]struct{}, len(references))
	numbers := make([]int64, 0, len(references))
	for _, reference := range references {
		if reference.SourceMediaID <= 0 || reference.Number < 1 || reference.Number > 4 {
			return errors.New("invalid generation media reference")
		}
		if _, ok := seen[reference.Number]; ok {
			return errors.New("duplicate generation media reference number")
		}
		seen[reference.Number] = struct{}{}
		numbers = append(numbers, int64(reference.Number))
		role, _, err := normalizeGenerationLibraryText(reference.Role, 40)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(reference.SourceMediaName)
		if utf8.RuneCountInString(name) > 255 {
			name = string([]rune(name)[:255])
		}
		var sourceOwned bool
		if err := tx.QueryRowContext(ctx, `
			SELECT TRUE FROM content_media media JOIN content_events event ON event.id=media.event_id
			WHERE media.id=$1 AND event.user_id=$2 AND event.service='comfyui' AND event.kind='comfyui_prompt'
			  AND media.media_type='image' AND media.expires_at > now() AND event.expires_at > now()
			  AND media.profile_hidden_at IS NULL
		`, reference.SourceMediaID, userID).Scan(&sourceOwned); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO generation_media_references(source_media_id,source_media_name,target_job_id,reference_number,reference_role)
			VALUES($1,$2,$3,$4,$5)
			ON CONFLICT(target_job_id,reference_number) DO UPDATE SET
			  source_media_id=EXCLUDED.source_media_id,source_media_name=EXCLUDED.source_media_name,reference_role=EXCLUDED.reference_role
			WHERE generation_media_references.source_media_id IS DISTINCT FROM EXCLUDED.source_media_id
			   OR generation_media_references.source_media_name IS DISTINCT FROM EXCLUDED.source_media_name
			   OR generation_media_references.reference_role IS DISTINCT FROM EXCLUDED.reference_role
		`, reference.SourceMediaID, name, jobID, reference.Number, role); err != nil {
			return err
		}
	}
	if len(numbers) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM generation_media_references WHERE target_job_id=$1`, jobID); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `
		DELETE FROM generation_media_references WHERE target_job_id=$1 AND NOT (reference_number=ANY($2))
	`, jobID, pq.Array(numbers)); err != nil {
		return err
	}
	return tx.Commit()
}

type normalizedGenerationLibraryText struct {
	display string
	key     string
}

func normalizeGenerationLibraryText(value string, maxRunes int) (string, string, error) {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || utf8.RuneCountInString(value) > maxRunes {
		return "", "", fmt.Errorf("значение должно содержать от 1 до %d символов", maxRunes)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", "", errors.New("значение содержит недопустимые символы")
		}
	}
	return value, strings.ToLower(value), nil
}

func normalizeGenerationMediaTags(tags []string) ([]normalizedGenerationLibraryText, error) {
	if len(tags) > maxGenerationMediaTags {
		return nil, fmt.Errorf("можно добавить не более %d тегов", maxGenerationMediaTags)
	}
	result := make([]normalizedGenerationLibraryText, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, raw := range tags {
		raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
		if raw == "" {
			continue
		}
		display, key, err := normalizeGenerationLibraryText(raw, 32)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, normalizedGenerationLibraryText{display: display, key: key})
	}
	return result, nil
}

func normalizeGenerationCollectionIDs(ids []int64) ([]int64, error) {
	ids = uniqueInt64s(ids)
	if len(ids) > maxMediaCollectionAssignments {
		return nil, fmt.Errorf("результат можно добавить не более чем в %d коллекций", maxMediaCollectionAssignments)
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, errors.New("некорректная коллекция")
		}
	}
	return ids, nil
}

func uniqueInt64s(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
