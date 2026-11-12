package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

// The reserved quota identity and the exact host-observed identity are retained
// separately because ComfyUI may transform an uploaded image before storing it.
func (s *Store) RecordComfyInputCleanup(ctx context.Context, results []domain.ComfyInputCleanupResult, retryDelay time.Duration) (ComfyCleanupRecorded, error) {
	if len(results) == 0 {
		return ComfyCleanupRecorded{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ComfyCleanupRecorded{}, err
	}
	defer tx.Rollback()
	recorded, err := recordComfyInputCleanupTx(ctx, tx, results, retryDelay, true)
	if err != nil {
		return ComfyCleanupRecorded{}, err
	}
	if err = tx.Commit(); err != nil {
		return ComfyCleanupRecorded{}, err
	}
	return recorded, nil
}

func recordComfyInputCleanupTx(ctx context.Context, tx *sql.Tx, results []domain.ComfyInputCleanupResult, retryDelay time.Duration, rejectClaimed bool) (ComfyCleanupRecorded, error) {
	var recorded ComfyCleanupRecorded
	if len(results) == 0 {
		return recorded, nil
	}
	if len(results) > 100 || retryDelay < time.Second || retryDelay > 24*time.Hour {
		return recorded, ErrComfyCleanupResult
	}
	ordered := append([]domain.ComfyInputCleanupResult(nil), results...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for i, item := range ordered {
		if len(item.ID) < 32 || len(item.ID) > 128 || !domain.ValidComfyCleanupOutcome(item.Outcome) ||
			(i > 0 && ordered[i-1].ID == item.ID) || (domain.TerminalComfyCleanupOutcome(item.Outcome) && !validComfyInputCleanupIdentity(item)) {
			return recorded, ErrComfyCleanupResult
		}
	}
	for _, item := range ordered {
		var current domain.ExpiredComfyInputAsset
		var attempts int
		var queuedAt, eligibleAt time.Time
		err := tx.QueryRowContext(ctx, `SELECT id,filename,subfolder,storage_type,size_bytes,content_hash,state,cleanup_attempts,created_at,expires_at
		 FROM comfy_input_assets WHERE id=$1 FOR UPDATE`, item.ID).Scan(&current.ID, &current.Filename, &current.Subfolder, &current.StorageType, &current.SizeBytes, &current.ContentHash, &current.State, &attempts, &queuedAt, &eligibleAt)
		if errors.Is(err, sql.ErrNoRows) {
			if !rejectClaimed {
				return ComfyCleanupRecorded{}, ErrComfyCleanupResult
			}
			continue
		}
		if err != nil {
			return ComfyCleanupRecorded{}, err
		}
		if current != item.ExpiredComfyInputAsset {
			return ComfyCleanupRecorded{}, ErrComfyCleanupResult
		}
		if rejectClaimed {
			if err := rejectComfyStorageClaim(ctx, tx, "input:"+item.ID); err != nil {
				return ComfyCleanupRecorded{}, err
			}
		}
		if domain.TerminalComfyCleanupOutcome(item.Outcome) {
			freedBytes := int64(0)
			if item.Outcome == "deleted" {
				freedBytes = item.ObservedSizeBytes
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO comfy_input_cleanup_receipts
			 (input_id,filename,subfolder,storage_type,size_bytes,content_hash,state,observed_size_bytes,observed_hash,outcome,freed_bytes,attempt_count,queued_at,eligible_at)
			 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, item.ID, item.Filename, item.Subfolder, item.StorageType, item.SizeBytes, strings.ToLower(item.ContentHash), item.State, item.ObservedSizeBytes, strings.ToLower(item.ObservedHash), item.Outcome, freedBytes, attempts+1, queuedAt, eligibleAt)
			if err != nil {
				return ComfyCleanupRecorded{}, err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM comfy_input_assets WHERE id=$1`, item.ID); err != nil {
				return ComfyCleanupRecorded{}, err
			}
			recorded.Completed++
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE comfy_input_assets SET cleanup_last_outcome=$2,cleanup_last_attempt_at=now(),cleanup_attempts=cleanup_attempts+1,
			 cleanup_retry_at=now()+($3::bigint*interval '1 second'),updated_at=now() WHERE id=$1`, item.ID, item.Outcome, int64(retryDelay.Seconds()))
			if err != nil {
				return ComfyCleanupRecorded{}, err
			}
			recorded.Deferred++
		}
	}
	return recorded, nil
}

func validComfyInputCleanupIdentity(item domain.ComfyInputCleanupResult) bool {
	_, trackedErr := hex.DecodeString(item.ContentHash)
	_, observedErr := hex.DecodeString(item.ObservedHash)
	return item.Filename != "" && item.StorageType == "input" && item.SizeBytes >= 0 && item.SizeBytes <= 512<<20 &&
		item.ObservedSizeBytes >= 0 && item.ObservedSizeBytes <= 2<<30 && len(item.ContentHash) == 64 && trackedErr == nil && len(item.ObservedHash) == 64 && observedErr == nil &&
		(item.State == "reserved" || (item.State == "stored" && item.SizeBytes == item.ObservedSizeBytes && strings.EqualFold(item.ContentHash, item.ObservedHash)))
}

func (s *Store) ComfyInputCleanupReceipts(ctx context.Context, limit int) ([]domain.ComfyInputCleanupReceipt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT input_id,filename,subfolder,storage_type,size_bytes,content_hash,state,observed_size_bytes,observed_hash,outcome,freed_bytes,attempt_count,queued_at,eligible_at,confirmed_at
	 FROM comfy_input_cleanup_receipts ORDER BY confirmed_at DESC,input_id DESC LIMIT $1`, boundedLimit(limit, 1, 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.ComfyInputCleanupReceipt{}
	for rows.Next() {
		var item domain.ComfyInputCleanupReceipt
		if err := rows.Scan(&item.ID, &item.Filename, &item.Subfolder, &item.StorageType, &item.SizeBytes, &item.ContentHash, &item.State, &item.ObservedSizeBytes, &item.ObservedHash, &item.Outcome, &item.FreedBytes, &item.Attempts, &item.QueuedAt, &item.EligibleAt, &item.ConfirmedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
