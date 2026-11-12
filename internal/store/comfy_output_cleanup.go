package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

func (s *Store) ScheduleComfyOutputCleanup(ctx context.Context, item domain.ComfyOutputCleanupTombstone, dueAt time.Time) error {
	item.Filename = strings.TrimSpace(item.Filename)
	item.Subfolder = strings.TrimSpace(item.Subfolder)
	item.StorageType = strings.TrimSpace(item.StorageType)
	item.ContentHash = strings.ToLower(strings.TrimSpace(item.ContentHash))
	if !validComfyCleanupIdentity(item) {
		return errors.New("invalid ComfyUI output cleanup identity")
	}
	if dueAt.IsZero() {
		return errors.New("ComfyUI output cleanup time is required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO comfy_output_cleanup_tombstones(filename,subfolder,storage_type,size_bytes,content_hash,next_attempt_at,eligible_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6)
		ON CONFLICT (filename,subfolder,storage_type,size_bytes,content_hash)
		DO UPDATE SET next_attempt_at=LEAST(comfy_output_cleanup_tombstones.next_attempt_at,EXCLUDED.next_attempt_at),
		 eligible_at=LEAST(comfy_output_cleanup_tombstones.eligible_at,EXCLUDED.eligible_at)
	`, item.Filename, item.Subfolder, item.StorageType, item.SizeBytes, item.ContentHash, dueAt.UTC())
	return err
}

func (s *Store) QueueExpiredComfyOutputCleanup(ctx context.Context, items []domain.ExpiredComfyMedia) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	// The candidate list can outlive a pin/expiry change. Re-read and lock the
	// actual expired rows before creating any host deletion intent.
	rows, err := tx.QueryContext(ctx, `SELECT m.id,m.original_name,m.subfolder,m.storage_type,m.size_bytes,m.content_hash,
	 EXISTS(SELECT 1 FROM comfy_output_ownership o WHERE o.event_id=m.event_id AND o.filename=m.original_name
	 AND o.subfolder=m.subfolder AND o.storage_type=m.storage_type),m.profile_hidden_at IS NOT NULL
	 FROM content_media m JOIN content_events e ON e.id=m.event_id
	 WHERE m.id=ANY($1) AND m.expires_at<=now() AND e.service='comfyui' ORDER BY m.id FOR UPDATE OF m`, pq.Array(ids))
	if err != nil {
		return 0, err
	}
	type candidate struct {
		domain.ExpiredComfyMedia
		hidden bool
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.ID, &item.Filename, &item.Subfolder, &item.StorageType, &item.SizeBytes, &item.ContentHash, &item.HasOwnership, &item.hidden); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	ids = ids[:0]
	for _, item := range candidates {
		ids = append(ids, item.ID)
		if !item.HasOwnership || item.StorageType != "output" || len(item.ContentHash) != 64 {
			continue
		}
		reason := "retention"
		if item.hidden {
			reason = "profile_hidden"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO comfy_output_cleanup_tombstones(filename,subfolder,storage_type,size_bytes,content_hash,cleanup_reason)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (filename,subfolder,storage_type,size_bytes,content_hash)
			DO UPDATE SET next_attempt_at=LEAST(comfy_output_cleanup_tombstones.next_attempt_at,now()),
			 eligible_at=LEAST(comfy_output_cleanup_tombstones.eligible_at,now()),
			 cleanup_reason=CASE WHEN EXCLUDED.cleanup_reason='profile_hidden' THEN 'profile_hidden' ELSE comfy_output_cleanup_tombstones.cleanup_reason END
		`, item.Filename, item.Subfolder, item.StorageType, item.SizeBytes, item.ContentHash, reason); err != nil {
			return 0, err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM content_media WHERE id = ANY($1) AND expires_at <= now()`, pq.Array(ids))
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

func validComfyCleanupIdentity(item domain.ComfyOutputCleanupTombstone) bool {
	_, err := hex.DecodeString(item.ContentHash)
	return item.Filename != "" && item.StorageType == "output" && item.SizeBytes >= 0 && item.SizeBytes <= 2<<30 &&
		len(item.ContentHash) == 64 && err == nil
}

func (s *Store) DueComfyOutputCleanup(ctx context.Context, limit int) ([]domain.ComfyOutputCleanupTombstone, error) {
	limit = boundedLimit(limit, 1, 100)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,filename,subfolder,storage_type,size_bytes,content_hash
		FROM comfy_output_cleanup_tombstones
		WHERE next_attempt_at <= now()
		AND NOT `+comfyStorageClaimedSQL("'cleanup:'||comfy_output_cleanup_tombstones.id")+`
		ORDER BY next_attempt_at,id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ComfyOutputCleanupTombstone, 0)
	for rows.Next() {
		var item domain.ComfyOutputCleanupTombstone
		if err := rows.Scan(&item.ID, &item.Filename, &item.Subfolder, &item.StorageType, &item.SizeBytes, &item.ContentHash); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
