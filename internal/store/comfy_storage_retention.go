package store

import (
	"context"
	"database/sql"
	"errors"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/updates"
)

// Selection, preview and admission commit together. A lost database commit
// response never grants Execute; the ordinary recovery worker fences that ID.
func (s *Store) ClaimComfyStorageRetention(ctx context.Context, id, hostKey string, status updates.ComfyStorageStatus) (ComfyStorageOperation, bool, error) {
	var empty ComfyStorageOperation
	if !updates.ValidComfyStorageID(id) || !updates.ComfyStorageHex(hostKey, 32) || status.Protocol != updates.ComfyStorageProtocol || !status.GuardEnforced || !updates.ComfyStorageHex(status.ExecutorID, 32) || !updates.ComfyStorageHex(status.GuardJournalID, 16) {
		return empty, false, ErrComfyStorageInput
	}
	if status.GuardHeld || status.PendingID != "" {
		return empty, false, ErrComfyStorageBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO gpu_resources(id) VALUES($1) ON CONFLICT DO NOTHING`, domain.GPUPrimaryResource); err != nil {
		return empty, false, err
	}
	var resource string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM gpu_resources WHERE id=$1 FOR UPDATE`, domain.GPUPrimaryResource).Scan(&resource); err != nil {
		return empty, false, err
	}
	op, err := scanComfyStorageOperation(tx.QueryRowContext(ctx, `SELECT `+comfyStorageOperationColumns+` FROM comfy_storage_operations WHERE id=$1`, id))
	if err == nil {
		if op.Origin != "retention" || op.HostKey != hostKey {
			return op, false, ErrComfyStorageConflict
		}
		return op, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, false, err
	}
	var blocked bool
	// Maintenance yields to ready work, even before a worker has acquired GPU.
	if err = tx.QueryRowContext(ctx, `SELECT `+storageAdmissionBlockedSQL+`
	 OR EXISTS(SELECT 1 FROM gpu_work w JOIN users u ON u.id=w.user_id WHERE w.resource_id=$1
	 AND (w.state IN ('held','uncertain') OR (w.state='waiting' AND w.ready_until>now()
	 AND NOT w.cancellation_requested AND NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>now()))))`, resource).Scan(&blocked); err != nil {
		return empty, false, err
	}
	if blocked {
		return empty, false, ErrComfyStorageBusy
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generationInputReferenceLockID); err != nil {
		return empty, false, err
	}
	selection, err := comfyStorageRetentionSelection(ctx, tx)
	if err != nil || len(selection) == 0 {
		return empty, false, err
	}
	op, err = createComfyStorageOperationTx(ctx, tx, 0, id, hostKey, status, selection)
	if err != nil {
		return empty, false, err
	}
	op, claimed, err := dispatchComfyStorageOperationTx(ctx, tx, 0, id, hostKey, op.RequestHash, status)
	if err != nil || !claimed {
		return op, false, err
	}
	err = tx.Commit()
	return op, err == nil, err
}

func comfyStorageRetentionSelection(ctx context.Context, tx *sql.Tx) ([]ComfyStorageRegistrySelection, error) {
	var view domain.ComfyStorageInventory
	if err := readComfyStorageActivity(ctx, tx, &view); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, comfyStorageCTE+`SELECT `+comfyStorageItemColumns+` FROM inventory i
	 WHERE pending AND eligible_at<=now() AND (next_attempt_at IS NULL OR next_attempt_at<=now())
	 AND state<>'reserved' AND NOT reference_protected AND size_known AND size_bytes BETWEEN 0 AND 2147483648
	 AND content_hash ~ '^[0-9a-fA-F]{64}$'
	 AND (kind<>'input' OR (NOT $1 AND (SELECT count(*) FROM comfy_input_assets a
	 WHERE lower(a.filename)=lower(i.filename) AND lower(a.subfolder)=lower(i.subfolder))=1))
	 AND (kind<>'output' OR NOT EXISTS(SELECT 1 FROM content_media m JOIN content_events e ON e.id=m.event_id
	 WHERE e.service='comfyui' AND m.storage_type='output' AND lower(m.original_name)=lower(i.filename)
	 AND lower(m.subfolder)=lower(i.subfolder) AND m.expires_at>now()))
	 ORDER BY COALESCE(next_attempt_at,eligible_at),eligible_at,key LIMIT 50`, view.Summary.UnknownInputManifest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selected := []ComfyStorageRegistrySelection{}
	for rows.Next() {
		item, err := scanComfyStorageItem(rows)
		if err != nil {
			return nil, err
		}
		decorateComfyStorageItem(&item, view.Summary)
		selected = append(selected, ComfyStorageRegistrySelection{Key: item.Key, Version: item.Version})
	}
	return selected, rows.Err()
}
