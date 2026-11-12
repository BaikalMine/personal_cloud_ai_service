package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/updates"
)

// keyExpression is a static SQL expression supplied only by store code.
func comfyStorageClaimedSQL(keyExpression string) string {
	return `EXISTS(SELECT 1 FROM comfy_storage_operations op WHERE op.state IN ('dispatching','unconfirmed')
	 AND op.selection @> jsonb_build_array(jsonb_build_object('key',` + keyExpression + `)))`
}

func rejectComfyStorageClaim(ctx context.Context, tx *sql.Tx, key string) error {
	var claimed bool
	if err := tx.QueryRowContext(ctx, `SELECT `+comfyStorageClaimedSQL("$1::text"), key).Scan(&claimed); err != nil {
		return err
	}
	if claimed {
		return ErrComfyStorageBusy
	}
	return nil
}

func comfyStorageReceiptOutcome(outcome string) string {
	switch outcome {
	case "deleted", "missing":
		return outcome
	case "changed":
		return "mismatched"
	case "rejected":
		return "rejected"
	default:
		return "unconfirmed"
	}
}

// A closed guard and fenced executor are both required. Receipt insertion,
// quota release, exact outcome accounting and admission release commit together.
func (s *Store) SettleComfyStorageOperation(ctx context.Context, id, hostKey string, result updates.ComfyStorageResult) (ComfyStorageOperation, error) {
	var empty ComfyStorageOperation
	if result.ID != id || !updates.ComfyStorageHex(hostKey, 32) || updates.ValidateComfyStorageResult(result) != nil || !result.ExecutorFenced || !result.GuardClosed {
		return empty, ErrComfyStorageInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	op, err := lockComfyStorageOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if op.HostKey != hostKey || op.RequestHash != result.RequestHash {
		return op, ErrComfyStorageConflict
	}
	if !op.Active() {
		if op.State == "preview" || op.DispatchedAt == nil {
			return op, ErrComfyStorageConflict
		}
		return op, tx.Commit()
	}
	if op.Request == nil || updates.MatchComfyStorageResult(result, *op.Request) != nil {
		return op, ErrComfyStorageConflict
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generationInputReferenceLockID); err != nil {
		return op, err
	}
	files := make(map[string]updates.ComfyStorageSelection, len(op.Request.Items))
	outcomes := make(map[string]string, len(result.Items))
	for _, item := range op.Request.Items {
		files[item.Key] = item
	}
	for _, item := range result.Items {
		outcomes[item.Selection.Key] = item.Outcome
	}
	inputs := []domain.ComfyInputCleanupResult{}
	outputs := []domain.ComfyOutputCleanupResult{}
	var freed int64
	var deleted int
	for _, target := range op.Selection {
		file := files[target.HostKey].File
		outcome := comfyStorageReceiptOutcome(outcomes[target.HostKey])
		// Expired compact host receipts prove fencing, not individual deletion.
		// Unknown items keep their registry/quota reservations for later retry.
		if outcome == "deleted" {
			freed += file.SizeBytes
			deleted++
		}
		kind, keyID, _ := strings.Cut(target.Key, ":")
		if kind == "input" {
			inputs = append(inputs, domain.ComfyInputCleanupResult{ExpiredComfyInputAsset: domain.ExpiredComfyInputAsset{ID: keyID, Filename: file.Filename, Subfolder: file.Subfolder, StorageType: file.StorageType, SizeBytes: file.SizeBytes, ContentHash: file.SHA256, State: "stored"}, ObservedSizeBytes: file.SizeBytes, ObservedHash: file.SHA256, Outcome: outcome})
		} else {
			n, _ := strconv.ParseInt(keyID, 10, 64)
			outputs = append(outputs, domain.ComfyOutputCleanupResult{ComfyOutputCleanupTombstone: domain.ComfyOutputCleanupTombstone{ID: n, Filename: file.Filename, Subfolder: file.Subfolder, StorageType: file.StorageType, SizeBytes: file.SizeBytes, ContentHash: file.SHA256}, Outcome: outcome})
		}
	}
	if _, err = recordComfyInputCleanupTx(ctx, tx, inputs, 6*time.Hour, false); err != nil {
		return op, err
	}
	if _, err = recordComfyOutputCleanupTx(ctx, tx, outputs, 6*time.Hour, false); err != nil {
		return op, err
	}
	raw, _ := json.Marshal(result)
	if len(raw) > 1048576 {
		return op, ErrComfyStorageInput
	}
	state := result.State
	if state == "expired" {
		state = "closed_unconfirmed"
	}
	op, err = scanComfyStorageOperation(tx.QueryRowContext(ctx, `UPDATE comfy_storage_operations SET state=$2,result=$3::jsonb,freed_bytes=$4,deleted_files=$5,finished_at=now(),checked_at=now(),last_code='host_settled' WHERE id=$1 RETURNING `+comfyStorageOperationColumns, id, state, string(raw), freed, deleted))
	if err != nil {
		return op, err
	}
	if err = invalidateGPUObservation(ctx, tx, op.ResourceID); err != nil {
		return op, err
	}
	if err = comfyStorageAudit(ctx, tx, op, "storage.comfy.settled"); err != nil {
		return op, err
	}
	return op, tx.Commit()
}
