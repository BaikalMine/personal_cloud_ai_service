package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

var ErrComfyCleanupResult = errors.New("invalid or stale ComfyUI cleanup result")

type ComfyCleanupRecorded struct {
	Completed int64
	Deferred  int64
}

// Closing a tombstone and retaining its host acknowledgment is one transaction.
// A replay of an already closed ID cannot increment the freed-byte count again.
func (s *Store) RecordComfyOutputCleanup(ctx context.Context, results []domain.ComfyOutputCleanupResult, retryDelay time.Duration) (ComfyCleanupRecorded, error) {
	if len(results) == 0 {
		return ComfyCleanupRecorded{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ComfyCleanupRecorded{}, err
	}
	defer tx.Rollback()
	recorded, err := recordComfyOutputCleanupTx(ctx, tx, results, retryDelay, true)
	if err != nil {
		return ComfyCleanupRecorded{}, err
	}
	if err = tx.Commit(); err != nil {
		return ComfyCleanupRecorded{}, err
	}
	return recorded, nil
}

func recordComfyOutputCleanupTx(ctx context.Context, tx *sql.Tx, results []domain.ComfyOutputCleanupResult, retryDelay time.Duration, rejectClaimed bool) (ComfyCleanupRecorded, error) {
	var recorded ComfyCleanupRecorded
	if len(results) == 0 {
		return recorded, nil
	}
	if len(results) > 100 || retryDelay < time.Second || retryDelay > 24*time.Hour {
		return recorded, ErrComfyCleanupResult
	}
	ordered := append([]domain.ComfyOutputCleanupResult(nil), results...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for i, item := range ordered {
		if item.ID <= 0 || !domain.ValidComfyCleanupOutcome(item.Outcome) ||
			(domain.TerminalComfyCleanupOutcome(item.Outcome) && !validComfyCleanupIdentity(item.ComfyOutputCleanupTombstone)) || (i > 0 && ordered[i-1].ID == item.ID) {
			return recorded, ErrComfyCleanupResult
		}
	}
	for _, item := range ordered {
		var current domain.ComfyOutputCleanupTombstone
		var attempts int
		var reason string
		var queuedAt, eligibleAt time.Time
		err := tx.QueryRowContext(ctx, `SELECT id,filename,subfolder,storage_type,size_bytes,content_hash,attempt_count,cleanup_reason,created_at,eligible_at
		 FROM comfy_output_cleanup_tombstones WHERE id=$1 FOR UPDATE`, item.ID).Scan(&current.ID, &current.Filename, &current.Subfolder,
			&current.StorageType, &current.SizeBytes, &current.ContentHash, &attempts, &reason, &queuedAt, &eligibleAt)
		if errors.Is(err, sql.ErrNoRows) {
			if !rejectClaimed {
				return ComfyCleanupRecorded{}, ErrComfyCleanupResult
			}
			continue
		}
		if err != nil {
			return ComfyCleanupRecorded{}, err
		}
		if current != item.ComfyOutputCleanupTombstone {
			return ComfyCleanupRecorded{}, ErrComfyCleanupResult
		}
		if rejectClaimed {
			if err := rejectComfyStorageClaim(ctx, tx, "cleanup:"+strconv.FormatInt(item.ID, 10)); err != nil {
				return ComfyCleanupRecorded{}, err
			}
		}
		if domain.TerminalComfyCleanupOutcome(item.Outcome) {
			freedBytes := int64(0)
			if item.Outcome == "deleted" {
				freedBytes = item.SizeBytes
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO comfy_output_cleanup_receipts
			 (tombstone_id,filename,subfolder,storage_type,size_bytes,content_hash,outcome,cleanup_reason,freed_bytes,attempt_count,queued_at,eligible_at)
			 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, item.ID, item.Filename, item.Subfolder, item.StorageType, item.SizeBytes,
				strings.ToLower(item.ContentHash), item.Outcome, reason, freedBytes, attempts+1, queuedAt, eligibleAt); err != nil {
				return ComfyCleanupRecorded{}, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM comfy_output_cleanup_tombstones WHERE id=$1`, item.ID); err != nil {
				return ComfyCleanupRecorded{}, err
			}
			recorded.Completed++
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE comfy_output_cleanup_tombstones
			 SET last_outcome=$2,last_attempt_at=now(),attempt_count=attempt_count+1,
			 next_attempt_at=now()+($3::bigint*interval '1 second') WHERE id=$1`, item.ID, item.Outcome, int64(retryDelay.Seconds())); err != nil {
				return ComfyCleanupRecorded{}, err
			}
			recorded.Deferred++
		}
	}
	return recorded, nil
}

func (s *Store) ComfyOutputCleanupReceipts(ctx context.Context, limit int) ([]domain.ComfyCleanupReceipt, error) {
	limit = boundedLimit(limit, 1, 100)
	rows, err := s.db.QueryContext(ctx, `SELECT tombstone_id,filename,subfolder,storage_type,size_bytes,content_hash,
	 outcome,cleanup_reason,freed_bytes,attempt_count,queued_at,eligible_at,confirmed_at
	 FROM comfy_output_cleanup_receipts ORDER BY confirmed_at DESC,tombstone_id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.ComfyCleanupReceipt{}
	for rows.Next() {
		var item domain.ComfyCleanupReceipt
		if err := rows.Scan(&item.ID, &item.Filename, &item.Subfolder, &item.StorageType, &item.SizeBytes, &item.ContentHash,
			&item.Outcome, &item.Reason, &item.FreedBytes, &item.Attempts, &item.QueuedAt, &item.EligibleAt, &item.ConfirmedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
