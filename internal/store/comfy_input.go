package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

const comfyInputQuotaLockID int64 = 743629834

var (
	ErrComfyInputUserQuota   = errors.New("ComfyUI input quota for this user is exhausted")
	ErrComfyInputGlobalQuota = errors.New("global ComfyUI input quota is exhausted")
)

type ComfyInputQuota struct {
	UserBytes   int64
	GlobalBytes int64
	UserFiles   int
	GlobalFiles int
}

// ReserveComfyInputAsset accounts for bytes before they cross into ComfyUI.
// A database advisory lock keeps concurrent Gateway instances from admitting
// more bytes than the shared host quota.
func (s *Store) ReserveComfyInputAsset(ctx context.Context, userID int64, id, filename, subfolder string, sizeBytes int64, contentHash string, quota ComfyInputQuota) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, comfyInputQuotaLockID); err != nil {
		return err
	}
	var userBytes, globalBytes int64
	var userFiles, globalFiles int
	if err := tx.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(size_bytes) FILTER (WHERE user_id=$1),0),
			COUNT(*) FILTER (WHERE user_id=$1),
			COALESCE(SUM(size_bytes),0),
			COUNT(*)
		FROM comfy_input_assets
	`, userID).Scan(&userBytes, &userFiles, &globalBytes, &globalFiles); err != nil {
		return err
	}
	if (quota.UserBytes > 0 && (sizeBytes > quota.UserBytes || userBytes > quota.UserBytes-sizeBytes)) || (quota.UserFiles > 0 && userFiles >= quota.UserFiles) {
		return ErrComfyInputUserQuota
	}
	if (quota.GlobalBytes > 0 && (sizeBytes > quota.GlobalBytes || globalBytes > quota.GlobalBytes-sizeBytes)) || (quota.GlobalFiles > 0 && globalFiles >= quota.GlobalFiles) {
		return ErrComfyInputGlobalQuota
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO comfy_input_assets (id,user_id,filename,subfolder,size_bytes,content_hash,state,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,'reserved',now() + interval '15 minutes')
	`, id, userID, filename, subfolder, sizeBytes, contentHash); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinalizeComfyInputAsset(ctx context.Context, id, filename, subfolder string, sizeBytes int64, contentHash string, retention time.Duration) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE comfy_input_assets
		SET filename=$2,subfolder=$3,size_bytes=$4,content_hash=$5,state='stored',updated_at=now(),
		    expires_at=now() + ($6::bigint * interval '1 second'),cleanup_retry_at=now(),cleanup_attempts=0
		WHERE id=$1 AND state='reserved'
	`, id, filename, subfolder, sizeBytes, contentHash, int64(retention.Seconds()))
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *Store) ReleaseComfyInputReservation(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM comfy_input_assets WHERE id=$1 AND state='reserved'`, id)
	return err
}

func (s *Store) ExpiredComfyInputAssets(ctx context.Context, limit int) ([]domain.ExpiredComfyInputAsset, error) {
	limit = boundedLimit(limit, 1, 100)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,filename,subfolder,storage_type,size_bytes,content_hash,state
		FROM comfy_input_assets
		WHERE expires_at <= now() AND cleanup_retry_at <= now()
		ORDER BY cleanup_retry_at,expires_at,id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ExpiredComfyInputAsset, 0)
	for rows.Next() {
		var item domain.ExpiredComfyInputAsset
		if err := rows.Scan(&item.ID, &item.Filename, &item.Subfolder, &item.StorageType, &item.SizeBytes, &item.ContentHash, &item.State); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeferComfyInputCleanup(ctx context.Context, ids []string, delay time.Duration) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE comfy_input_assets
		SET cleanup_retry_at=now() + ($2::bigint * interval '1 second'),cleanup_attempts=cleanup_attempts+1,updated_at=now()
		WHERE id = ANY($1)
	`, pq.Array(ids), int64(delay.Seconds()))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) DeleteComfyInputAssetsByIDs(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM comfy_input_assets WHERE id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
