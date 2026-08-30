package store

import (
	"context"
	"database/sql"
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
	if item.Filename == "" || item.StorageType != "output" || item.SizeBytes < 0 || item.SizeBytes > 2<<30 || len(item.ContentHash) != 64 {
		return errors.New("invalid ComfyUI output cleanup identity")
	}
	if dueAt.IsZero() {
		return errors.New("ComfyUI output cleanup time is required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO comfy_output_cleanup_tombstones(filename,subfolder,storage_type,size_bytes,content_hash,next_attempt_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (filename,subfolder,storage_type,size_bytes,content_hash)
		DO UPDATE SET next_attempt_at=LEAST(comfy_output_cleanup_tombstones.next_attempt_at,EXCLUDED.next_attempt_at)
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
		if !item.HasOwnership || item.StorageType != "output" || len(item.ContentHash) != 64 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO comfy_output_cleanup_tombstones(filename,subfolder,storage_type,size_bytes,content_hash)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (filename,subfolder,storage_type,size_bytes,content_hash)
			DO UPDATE SET next_attempt_at=LEAST(comfy_output_cleanup_tombstones.next_attempt_at,now())
		`, item.Filename, item.Subfolder, item.StorageType, item.SizeBytes, item.ContentHash); err != nil {
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

func (s *Store) DueComfyOutputCleanup(ctx context.Context, limit int) ([]domain.ComfyOutputCleanupTombstone, error) {
	limit = boundedLimit(limit, 1, 100)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,filename,subfolder,storage_type,size_bytes,content_hash
		FROM comfy_output_cleanup_tombstones
		WHERE next_attempt_at <= now()
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

func (s *Store) DeleteComfyOutputCleanupByIDs(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM comfy_output_cleanup_tombstones WHERE id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) DeferComfyOutputCleanup(ctx context.Context, ids []int64, delay time.Duration) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE comfy_output_cleanup_tombstones
		SET next_attempt_at=now() + ($2::bigint * interval '1 second'),attempt_count=attempt_count+1
		WHERE id = ANY($1)
	`, pq.Array(ids), int64(delay.Seconds()))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
