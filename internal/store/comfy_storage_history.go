package store

import (
	"context"
	"database/sql"
	"time"
)

type ComfyStorageHistory struct {
	Items      []ComfyStorageOperation `json:"items"`
	Total      int64                   `json:"total"`
	FreedBytes int64                   `json:"freed_bytes"`
	Since      time.Time               `json:"since"`
	CheckedAt  time.Time               `json:"checked_at"`
	Offset     int                     `json:"offset"`
}

func (s *Store) ComfyStorageHistory(ctx context.Context, offset int) (ComfyStorageHistory, error) {
	var history ComfyStorageHistory
	if offset < 0 || offset > 1000000 {
		return history, ErrComfyStorageInput
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return history, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT now(),now()-interval '90 days'`).Scan(&history.CheckedAt, &history.Since); err != nil {
		return history, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(freed_bytes) FILTER(WHERE finished_at >= $1),0) FROM comfy_storage_operations WHERE created_at>=$1 OR finished_at>=$1 OR state IN ('preview','dispatching','unconfirmed')`, history.Since).Scan(&history.Total, &history.FreedBytes); err != nil {
		return history, err
	}
	history.Offset = offset
	history.Items = []ComfyStorageOperation{}
	rows, err := tx.QueryContext(ctx, `SELECT `+comfyStorageOperationColumns+` FROM comfy_storage_operations WHERE created_at>=$1 OR finished_at>=$1 OR state IN ('preview','dispatching','unconfirmed') ORDER BY created_at DESC,id LIMIT 50 OFFSET $2`, history.Since, offset)
	if err != nil {
		return history, err
	}
	defer rows.Close()
	for rows.Next() {
		op, err := scanComfyStorageOperation(rows)
		if err != nil {
			return history, err
		}
		history.Items = append(history.Items, op)
	}
	if err = rows.Err(); err != nil {
		return history, err
	}
	return history, tx.Commit()
}
