package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

var (
	ErrTrainingStorageInput    = errors.New("invalid training storage operation")
	ErrTrainingStorageDenied   = errors.New("training storage operation requires its active administrator")
	ErrTrainingStorageConflict = errors.New("training storage operation changed")
	ErrTrainingStorageExpired  = errors.New("training storage preview or details expired")
	ErrTrainingStorageBusy     = errors.New("training storage admission is occupied")
)

type TrainingStorageOperation struct {
	ID            string                              `json:"id"`
	ResourceID    string                              `json:"-"`
	ActorID       *int64                              `json:"actor_id"`
	ActorName     string                              `json:"actor_name"`
	HostKey       string                              `json:"-"`
	RequestHash   string                              `json:"request_hash"`
	Request       *loratraining.StorageCleanupRequest `json:"-"`
	Preview       *loratraining.StorageInventory      `json:"preview,omitempty"`
	Result        *loratraining.StorageCleanupResult  `json:"result,omitempty"`
	State         string                              `json:"state"`
	ItemCount     int                                 `json:"item_count"`
	ExpectedBytes int64                               `json:"expected_bytes"`
	FreedBytes    int64                               `json:"freed_bytes"`
	DeletedFiles  int64                               `json:"deleted_files"`
	LastCode      string                              `json:"last_code"`
	CreatedAt     time.Time                           `json:"created_at"`
	ExpiresAt     time.Time                           `json:"expires_at"`
	DispatchedAt  *time.Time                          `json:"dispatched_at,omitempty"`
	FinishedAt    *time.Time                          `json:"finished_at,omitempty"`
	CheckedAt     *time.Time                          `json:"checked_at,omitempty"`
	PrunedAt      *time.Time                          `json:"pruned_at,omitempty"`
}

func (op TrainingStorageOperation) Active() bool {
	return op.State == "dispatching" || op.State == "unconfirmed"
}

func (op TrainingStorageOperation) MatchesSelection(ids []string) bool {
	if op.Request == nil || len(ids) != len(op.Request.Items) || loratraining.ValidateStorageIDs(ids) != nil {
		return false
	}
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	for i, item := range op.Request.Items {
		if item.ID != ids[i] {
			return false
		}
	}
	return true
}

const trainingStorageColumns = `id,resource_id,actor_id,actor_name,host_key,request_hash,request,preview,result,state,item_count,expected_bytes,freed_bytes,deleted_files,last_code,created_at,expires_at,dispatched_at,finished_at,checked_at,pruned_at`

func scanTrainingStorage(row datasetScanner) (op TrainingStorageOperation, err error) {
	var request, preview, result []byte
	err = row.Scan(&op.ID, &op.ResourceID, &op.ActorID, &op.ActorName, &op.HostKey, &op.RequestHash, &request, &preview, &result, &op.State, &op.ItemCount, &op.ExpectedBytes, &op.FreedBytes, &op.DeletedFiles, &op.LastCode, &op.CreatedAt, &op.ExpiresAt, &op.DispatchedAt, &op.FinishedAt, &op.CheckedAt, &op.PrunedAt)
	if err != nil {
		return
	}
	if len(request) > 0 {
		if err = json.Unmarshal(request, &op.Request); err != nil {
			return
		}
		var hash string
		if op.Request == nil {
			return op, ErrTrainingStorageInput
		}
		_, hash, err = loratraining.CanonicalStorageCleanupRequest(*op.Request)
		if err != nil || hash != op.RequestHash || op.Request.ID != op.ID {
			return op, ErrTrainingStorageInput
		}
	}
	if len(preview) > 0 {
		if err = json.Unmarshal(preview, &op.Preview); err != nil {
			return
		}
		if op.Preview == nil || loratraining.ValidateStorageInventory(*op.Preview) != nil {
			return op, ErrTrainingStorageInput
		}
	}
	if len(result) > 0 {
		if err = json.Unmarshal(result, &op.Result); err != nil {
			return
		}
		if op.Result == nil || op.Result.RequestHash != op.RequestHash || loratraining.ValidateStorageCleanupResult(*op.Result) != nil {
			return op, ErrTrainingStorageInput
		}
	}
	return
}

func lockTrainingStorageAdmin(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id=$1 AND role='admin' AND NOT disabled AND (account_expires_at IS NULL OR account_expires_at>clock_timestamp()) FOR SHARE`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrTrainingStorageDenied
	}
	return name, err
}

func trainingStorageAudit(ctx context.Context, tx *sql.Tx, op TrainingStorageOperation, action string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_log(actor_user_id,action,target_type,metadata)
	 VALUES($1,$2,'training_storage',jsonb_build_object('operation_id',$3::text,'state',$4::text,'item_count',$5::integer,'freed_bytes',$6::bigint))`, op.ActorID, action, op.ID, op.State, op.ItemCount, op.FreedBytes)
	return err
}

func (s *Store) TrainingStorageOperation(ctx context.Context, id string) (TrainingStorageOperation, error) {
	if !loratraining.ValidStorageOperationID(id) {
		return TrainingStorageOperation{}, ErrTrainingStorageInput
	}
	return scanTrainingStorage(s.db.QueryRowContext(ctx, `SELECT `+trainingStorageColumns+` FROM training_storage_operations WHERE id=$1`, id))
}

func (s *Store) CreateTrainingStorageOperation(ctx context.Context, actorID int64, id, hostKey string, preview loratraining.StorageInventory) (TrainingStorageOperation, error) {
	if !loratraining.ValidStorageOperationID(id) || !loratraining.ValidStorageVersion(hostKey) || loratraining.ValidateStorageInventory(preview) != nil || !preview.Complete || preview.NextCursor != "" || preview.TotalItems != len(preview.Items) || len(preview.Items) == 0 || len(preview.Items) > loratraining.MaxStorageSelection {
		return TrainingStorageOperation{}, ErrTrainingStorageInput
	}
	request := loratraining.StorageCleanupRequest{ID: id}
	var expected int64
	for _, item := range preview.Items {
		if !item.CanCleanup {
			return TrainingStorageOperation{}, ErrTrainingStorageConflict
		}
		request.Items = append(request.Items, loratraining.StorageSelection{ID: item.ID, Version: item.Version})
		expected += item.SizeBytes
	}
	request, hash, err := loratraining.CanonicalStorageCleanupRequest(request)
	if err != nil {
		return TrainingStorageOperation{}, ErrTrainingStorageInput
	}
	rawRequest, _ := json.Marshal(request)
	rawPreview, _ := json.Marshal(preview)
	if len(rawRequest) > 65536 || len(rawPreview) > 262144 {
		return TrainingStorageOperation{}, ErrTrainingStorageInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TrainingStorageOperation{}, err
	}
	defer tx.Rollback()
	name, err := lockTrainingStorageAdmin(ctx, tx, actorID)
	if err != nil {
		return TrainingStorageOperation{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO gpu_resources(id) VALUES($1) ON CONFLICT DO NOTHING`, domain.GPUPrimaryResource); err != nil {
		return TrainingStorageOperation{}, err
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO training_storage_operations(id,resource_id,actor_id,actor_name,host_key,request_hash,request,preview,item_count,expected_bytes)
	 VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10) ON CONFLICT(id) DO NOTHING`, id, domain.GPUPrimaryResource, actorID, name, hostKey, hash, string(rawRequest), string(rawPreview), len(preview.Items), expected)
	if err != nil {
		return TrainingStorageOperation{}, err
	}
	op, err := scanTrainingStorage(tx.QueryRowContext(ctx, `SELECT `+trainingStorageColumns+` FROM training_storage_operations WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return op, err
	}
	if op.ActorID == nil || *op.ActorID != actorID {
		return op, ErrTrainingStorageDenied
	}
	if op.HostKey != hostKey || op.RequestHash != hash {
		return op, ErrTrainingStorageConflict
	}
	if op.PrunedAt != nil {
		return op, ErrTrainingStorageExpired
	}
	if n, _ := inserted.RowsAffected(); n == 1 {
		if err = trainingStorageAudit(ctx, tx, op, "storage.training.preview"); err != nil {
			return op, err
		}
	}
	return op, tx.Commit()
}

func (s *Store) TrainingStorageAdmissionBlocked(ctx context.Context, resource string) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT `+storageAdmissionBlockedSQL, resource).Scan(&active)
	return active, err
}

func trainingStorageAdmissionBlockedTx(ctx context.Context, tx *sql.Tx, resource string) (bool, error) {
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT `+storageAdmissionBlockedSQL, resource).Scan(&active)
	return active, err
}

// The resource row serializes this durable claim with GPU work and executor
// permits. A connection loss does not release the claim or authorize a retry.
func (s *Store) DispatchTrainingStorageOperation(ctx context.Context, actorID int64, id, hostKey, hash string) (TrainingStorageOperation, bool, error) {
	if !loratraining.ValidStorageOperationID(id) || !loratraining.ValidStorageVersion(hostKey) || !loratraining.ValidStorageVersion(hash) {
		return TrainingStorageOperation{}, false, ErrTrainingStorageInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TrainingStorageOperation{}, false, err
	}
	defer tx.Rollback()
	resource, err := scanGPUResource(tx.QueryRowContext(ctx, `SELECT `+gpuResourceColumns+` FROM gpu_resources WHERE id=(SELECT resource_id FROM training_storage_operations WHERE id=$1) FOR UPDATE`, id))
	if err != nil {
		return TrainingStorageOperation{}, false, err
	}
	if _, err = lockTrainingStorageAdmin(ctx, tx, actorID); err != nil {
		return TrainingStorageOperation{}, false, err
	}
	op, err := scanTrainingStorage(tx.QueryRowContext(ctx, `SELECT `+trainingStorageColumns+` FROM training_storage_operations WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return op, false, err
	}
	if op.ActorID == nil || *op.ActorID != actorID {
		return op, false, ErrTrainingStorageDenied
	}
	if op.HostKey != hostKey || op.RequestHash != hash {
		return op, false, ErrTrainingStorageConflict
	}
	if op.PrunedAt != nil {
		return op, false, ErrTrainingStorageExpired
	}
	if op.State != "preview" {
		return op, false, tx.Commit()
	}
	var expired, occupied bool
	if err = tx.QueryRowContext(ctx, `SELECT $1::timestamptz<=clock_timestamp()`, op.ExpiresAt).Scan(&expired); err != nil {
		return op, false, err
	}
	if expired {
		return op, false, ErrTrainingStorageExpired
	}
	if err = tx.QueryRowContext(ctx, `SELECT
	 `+storageAdmissionBlockedSQL+`
	 OR EXISTS(SELECT 1 FROM gpu_work WHERE resource_id=$1 AND state IN ('held','uncertain'))
	 OR EXISTS(SELECT 1 FROM gpu_executor_permits WHERE resource_id=$1 AND state IN ('reserved','running'))
	 OR EXISTS(SELECT 1 FROM lora_training_jobs WHERE state IN ('uploading','preparing','caching','running','installing') AND (agent_submission_started_at IS NOT NULL OR agent_job_id<>''))
	 OR EXISTS(SELECT 1 FROM generation_jobs WHERE resources_released_at IS NULL AND dispatch_closed_at IS NULL AND (prompt_id IS NOT NULL OR submission_started_at IS NOT NULL) AND submission_rejected_at IS NULL AND state IN ('preparing','uploading','waiting_for_resources','queued','running','postprocessing','archiving'))`, resource.ID).Scan(&occupied); err != nil {
		return op, false, err
	}
	if occupied {
		return op, false, ErrTrainingStorageBusy
	}
	op, err = scanTrainingStorage(tx.QueryRowContext(ctx, `UPDATE training_storage_operations SET state='dispatching',dispatched_at=clock_timestamp(),last_code='dispatch_started' WHERE id=$1 RETURNING `+trainingStorageColumns, id))
	if err != nil {
		return op, false, err
	}
	if err = invalidateGPUObservation(ctx, tx, resource.ID); err != nil {
		return op, false, err
	}
	if err = trainingStorageAudit(ctx, tx, op, "storage.training.confirm"); err != nil {
		return op, false, err
	}
	err = tx.Commit()
	return op, err == nil, err
}

func (s *Store) MarkTrainingStorageUnconfirmed(ctx context.Context, id, hostKey, code string) error {
	switch code {
	case "host_unavailable", "host_running", "host_unconfirmed", "host_changed", "invalid_response", "result_write_failed":
	default:
		return ErrTrainingStorageInput
	}
	_, err := s.db.ExecContext(ctx, `UPDATE training_storage_operations SET state='unconfirmed',last_code=$3,checked_at=now() WHERE id=$1 AND host_key=$2 AND state IN ('dispatching','unconfirmed')`, id, hostKey, code)
	return err
}

// Only terminal, exactly bound host evidence releases the durable admission
// gate. Byte accounting and gate release commit in the same transaction.
func (s *Store) SettleTrainingStorageOperation(ctx context.Context, id, hostKey string, result loratraining.StorageCleanupResult) (TrainingStorageOperation, error) {
	if result.ID != id || loratraining.ValidateStorageCleanupResult(result) != nil || result.State == "running" || result.State == "unconfirmed" {
		return TrainingStorageOperation{}, ErrTrainingStorageInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TrainingStorageOperation{}, err
	}
	defer tx.Rollback()
	resource, err := scanGPUResource(tx.QueryRowContext(ctx, `SELECT `+gpuResourceColumns+` FROM gpu_resources WHERE id=(SELECT resource_id FROM training_storage_operations WHERE id=$1) FOR UPDATE`, id))
	if err != nil {
		return TrainingStorageOperation{}, err
	}
	op, err := scanTrainingStorage(tx.QueryRowContext(ctx, `SELECT `+trainingStorageColumns+` FROM training_storage_operations WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return op, err
	}
	if op.HostKey != hostKey || op.RequestHash != result.RequestHash {
		return op, ErrTrainingStorageConflict
	}
	if !op.Active() {
		if op.State == "preview" {
			return op, ErrTrainingStorageConflict
		}
		return op, tx.Commit()
	}
	if op.Request == nil || op.Preview == nil || len(result.Items) != len(op.Preview.Items) {
		return op, ErrTrainingStorageInput
	}
	preview := make(map[string]loratraining.StorageItem, len(op.Preview.Items))
	for _, item := range op.Preview.Items {
		preview[item.ID] = item
	}
	var freed, files int64
	for i := range result.Items {
		item := &result.Items[i]
		before, ok := preview[item.ID]
		if !ok || before.Version != item.Version || item.FreedBytes > before.SizeBytes || item.DeletedFiles > before.Files {
			return op, ErrTrainingStorageInput
		}
		item.Label = before.Label
		freed += item.FreedBytes
		files += item.DeletedFiles
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 1048576 {
		return op, ErrTrainingStorageInput
	}
	op, err = scanTrainingStorage(tx.QueryRowContext(ctx, `UPDATE training_storage_operations SET state=$2,result=$3::jsonb,freed_bytes=$4,deleted_files=$5,finished_at=now(),checked_at=now(),last_code='host_settled' WHERE id=$1 RETURNING `+trainingStorageColumns, id, result.State, string(raw), freed, files))
	if err != nil {
		return op, err
	}
	if err = invalidateGPUObservation(ctx, tx, resource.ID); err != nil {
		return op, err
	}
	if err = trainingStorageAudit(ctx, tx, op, "storage.training.settled"); err != nil {
		return op, err
	}
	return op, tx.Commit()
}

func (s *Store) CancelTrainingStoragePreview(ctx context.Context, actorID int64, id string) (TrainingStorageOperation, error) {
	if !loratraining.ValidStorageOperationID(id) {
		return TrainingStorageOperation{}, ErrTrainingStorageInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TrainingStorageOperation{}, err
	}
	defer tx.Rollback()
	if _, err = lockTrainingStorageAdmin(ctx, tx, actorID); err != nil {
		return TrainingStorageOperation{}, err
	}
	op, err := scanTrainingStorage(tx.QueryRowContext(ctx, `SELECT `+trainingStorageColumns+` FROM training_storage_operations WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return op, err
	}
	if op.ActorID == nil || *op.ActorID != actorID {
		return op, ErrTrainingStorageDenied
	}
	if op.State == "cancelled" {
		return op, tx.Commit()
	}
	if op.State != "preview" {
		return op, ErrTrainingStorageConflict
	}
	op, err = scanTrainingStorage(tx.QueryRowContext(ctx, `UPDATE training_storage_operations SET state='cancelled',finished_at=now(),last_code='preview_cancelled' WHERE id=$1 RETURNING `+trainingStorageColumns, id))
	if err != nil {
		return op, err
	}
	if err = trainingStorageAudit(ctx, tx, op, "storage.training.cancel"); err != nil {
		return op, err
	}
	return op, tx.Commit()
}

func (s *Store) PendingTrainingStorageOperations(ctx context.Context) ([]TrainingStorageOperation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+trainingStorageColumns+` FROM training_storage_operations WHERE state IN ('dispatching','unconfirmed') ORDER BY dispatched_at,id LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []TrainingStorageOperation{}
	for rows.Next() {
		op, err := scanTrainingStorage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, op)
	}
	return items, rows.Err()
}

type TrainingStorageHistory struct {
	Items      []TrainingStorageOperation `json:"items"`
	Total      int64                      `json:"total"`
	FreedBytes int64                      `json:"freed_bytes"`
	Since      time.Time                  `json:"since"`
	CheckedAt  time.Time                  `json:"checked_at"`
	Offset     int                        `json:"offset"`
}

func (s *Store) TrainingStorageHistory(ctx context.Context, offset int) (TrainingStorageHistory, error) {
	var history TrainingStorageHistory
	if offset < 0 || offset > 1000000 {
		return history, ErrTrainingStorageInput
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return history, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT now(),now()-interval '90 days'`).Scan(&history.CheckedAt, &history.Since); err != nil {
		return history, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(freed_bytes) FILTER(WHERE finished_at >= $1),0) FROM training_storage_operations WHERE created_at>=$1 OR finished_at>=$1 OR state IN ('preview','dispatching','unconfirmed')`, history.Since).Scan(&history.Total, &history.FreedBytes); err != nil {
		return history, err
	}
	history.Offset = offset
	history.Items = []TrainingStorageOperation{}
	rows, err := tx.QueryContext(ctx, `SELECT `+trainingStorageColumns+` FROM training_storage_operations WHERE created_at>=$1 OR finished_at>=$1 OR state IN ('preview','dispatching','unconfirmed') ORDER BY created_at DESC,id LIMIT 50 OFFSET $2`, history.Since, offset)
	if err != nil {
		return history, err
	}
	defer rows.Close()
	for rows.Next() {
		op, err := scanTrainingStorage(rows)
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

// Closed headers retain IDs after details expire, so delayed create/confirm
// retries can never reuse a previous operation against newly rebuilt files.
func (s *Store) PruneTrainingStorageOperations(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `WITH expired AS (
	 SELECT id FROM training_storage_operations WHERE state='preview' AND expires_at<now() ORDER BY expires_at,id LIMIT 500 FOR UPDATE SKIP LOCKED)
	 UPDATE training_storage_operations op SET state='expired',finished_at=now(),last_code='preview_expired' FROM expired WHERE op.id=expired.id`)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	result, err = s.db.ExecContext(ctx, `WITH expired AS (
	 SELECT id FROM training_storage_operations WHERE finished_at<now()-interval '90 days' AND pruned_at IS NULL ORDER BY finished_at,id LIMIT 500 FOR UPDATE SKIP LOCKED)
	 UPDATE training_storage_operations op SET request=NULL,preview=NULL,result=NULL,actor_name='',pruned_at=now() FROM expired WHERE op.id=expired.id`)
	if err != nil {
		return count, err
	}
	pruned, _ := result.RowsAffected()
	return count + pruned, nil
}
