package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/updates"
	"github.com/lib/pq"
)

var (
	ErrComfyStorageInput    = errors.New("invalid ComfyUI storage operation")
	ErrComfyStorageDenied   = errors.New("ComfyUI storage operation requires its active administrator")
	ErrComfyStorageConflict = errors.New("ComfyUI storage selection or host changed")
	ErrComfyStorageExpired  = errors.New("ComfyUI storage preview or details expired")
	ErrComfyStorageBusy     = errors.New("ComfyUI storage admission is occupied")
)

// Every caller holds gpu_resources FOR UPDATE before deciding admission.
// These claims survive connection loss and have no time-based release.
const storageAdmissionBlockedSQL = `EXISTS(SELECT 1 FROM training_storage_operations WHERE resource_id=$1 AND state IN ('dispatching','unconfirmed'))
 OR EXISTS(SELECT 1 FROM comfy_storage_operations WHERE resource_id=$1 AND state IN ('dispatching','unconfirmed'))`

type ComfyStorageRegistrySelection struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

// HostKey is an operation-local ordinal, not a registry ID or filesystem path.
// Keeping this mapping preserves the already versioned host journal protocol.
type ComfyStorageTarget struct {
	ComfyStorageRegistrySelection
	HostKey string `json:"host_key"`
}

type ComfyStorageOperation struct {
	ID            string                       `json:"id"`
	Origin        string                       `json:"origin"`
	ResourceID    string                       `json:"-"`
	ActorID       *int64                       `json:"actor_id"`
	ActorName     string                       `json:"actor_name"`
	HostKey       string                       `json:"-"`
	RequestHash   string                       `json:"request_hash"`
	Request       *updates.ComfyStorageRequest `json:"-"`
	Selection     []ComfyStorageTarget         `json:"selection,omitempty"`
	Result        *updates.ComfyStorageResult  `json:"result,omitempty"`
	State         string                       `json:"state"`
	ItemCount     int                          `json:"item_count"`
	ExpectedBytes int64                        `json:"expected_bytes"`
	FreedBytes    int64                        `json:"freed_bytes"`
	DeletedFiles  int                          `json:"deleted_files"`
	LastCode      string                       `json:"last_code"`
	CreatedAt     time.Time                    `json:"created_at"`
	ExpiresAt     time.Time                    `json:"expires_at"`
	DispatchedAt  *time.Time                   `json:"dispatched_at,omitempty"`
	FinishedAt    *time.Time                   `json:"finished_at,omitempty"`
	CheckedAt     *time.Time                   `json:"checked_at,omitempty"`
	PrunedAt      *time.Time                   `json:"pruned_at,omitempty"`
}

func (op ComfyStorageOperation) Active() bool {
	return op.State == "dispatching" || op.State == "unconfirmed"
}

func canonicalComfyRegistrySelection(input []ComfyStorageRegistrySelection) ([]ComfyStorageRegistrySelection, error) {
	if len(input) == 0 || len(input) > updates.MaxComfyStorageSelection {
		return nil, ErrComfyStorageInput
	}
	result := append([]ComfyStorageRegistrySelection(nil), input...)
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	for i, item := range result {
		if !ValidComfyStorageKey(item.Key) || strings.HasPrefix(item.Key, "output:") || !updates.ComfyStorageHex(item.Version, 32) || i > 0 && result[i-1].Key == item.Key {
			return nil, ErrComfyStorageInput
		}
	}
	return result, nil
}

func (op ComfyStorageOperation) MatchesSelection(input []ComfyStorageRegistrySelection) bool {
	items, err := canonicalComfyRegistrySelection(input)
	if err != nil || len(items) != len(op.Selection) {
		return false
	}
	for i, item := range items {
		if item != op.Selection[i].ComfyStorageRegistrySelection {
			return false
		}
	}
	return true
}

const comfyStorageOperationColumns = `id,resource_id,actor_id,actor_name,host_key,request_hash,request,selection,result,state,item_count,expected_bytes,freed_bytes,deleted_files,last_code,created_at,expires_at,dispatched_at,finished_at,checked_at,pruned_at,origin`

func scanComfyStorageOperation(row datasetScanner) (op ComfyStorageOperation, err error) {
	var request, selection, result []byte
	err = row.Scan(&op.ID, &op.ResourceID, &op.ActorID, &op.ActorName, &op.HostKey, &op.RequestHash, &request, &selection, &result, &op.State, &op.ItemCount, &op.ExpectedBytes, &op.FreedBytes, &op.DeletedFiles, &op.LastCode, &op.CreatedAt, &op.ExpiresAt, &op.DispatchedAt, &op.FinishedAt, &op.CheckedAt, &op.PrunedAt, &op.Origin)
	if err != nil {
		return
	}
	if op.PrunedAt != nil {
		return
	}
	if json.Unmarshal(request, &op.Request) != nil || op.Request == nil || json.Unmarshal(selection, &op.Selection) != nil || len(op.Selection) != op.ItemCount {
		return op, ErrComfyStorageInput
	}
	canonical, hash, err := updates.CanonicalComfyStorageRequest(*op.Request)
	if err != nil || hash != op.RequestHash || canonical.ID != op.ID || len(canonical.Items) != op.ItemCount {
		return op, ErrComfyStorageInput
	}
	op.Request = &canonical
	files := make(map[string]updates.ComfyStorageSelection, len(canonical.Items))
	for _, item := range canonical.Items {
		files[item.Key] = item
	}
	var expected int64
	for i, target := range op.Selection {
		file, ok := files[target.HostKey]
		kind := "output"
		if strings.HasPrefix(target.Key, "input:") {
			kind = "input"
		}
		if !ok || !ValidComfyStorageKey(target.Key) || strings.HasPrefix(target.Key, "output:") || target.HostKey != kind+":"+strconv.Itoa(i+1) || file.Version != target.Version || i > 0 && op.Selection[i-1].Key >= target.Key {
			return op, ErrComfyStorageInput
		}
		expected += file.File.SizeBytes
	}
	if expected != op.ExpectedBytes {
		return op, ErrComfyStorageInput
	}
	if len(result) > 0 {
		if json.Unmarshal(result, &op.Result) != nil || op.Result == nil || updates.MatchComfyStorageResult(*op.Result, canonical) != nil {
			return op, ErrComfyStorageInput
		}
	}
	return op, nil
}

func lockComfyStorageAdmin(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	name, err := lockTrainingStorageAdmin(ctx, tx, id)
	if errors.Is(err, ErrTrainingStorageDenied) {
		err = ErrComfyStorageDenied
	}
	return name, err
}

func comfyStorageAudit(ctx context.Context, tx *sql.Tx, op ComfyStorageOperation, action string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_log(actor_user_id,action,target_type,metadata) VALUES($1,$2,'comfy_storage',jsonb_build_object('operation_id',$3::text,'state',$4::text,'item_count',$5::integer,'freed_bytes',$6::bigint,'origin',$7::text))`, op.ActorID, action, op.ID, op.State, op.ItemCount, op.FreedBytes, op.Origin)
	return err
}

func (s *Store) ComfyStorageOperation(ctx context.Context, id string) (ComfyStorageOperation, error) {
	if !updates.ValidComfyStorageID(id) {
		return ComfyStorageOperation{}, ErrComfyStorageInput
	}
	return scanComfyStorageOperation(s.db.QueryRowContext(ctx, `SELECT `+comfyStorageOperationColumns+` FROM comfy_storage_operations WHERE id=$1`, id))
}

// Lock the registry rows before taking the fresh inventory snapshot. Reference
// binders take the same advisory lock, so no in-flight manifest is missed.
func lockComfyStorageSelection(ctx context.Context, tx *sql.Tx, selection []ComfyStorageRegistrySelection) ([]domain.ComfyStorageItem, error) {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generationInputReferenceLockID); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(selection))
	for _, item := range selection {
		kind, id, _ := strings.Cut(item.Key, ":")
		query := `SELECT id FROM comfy_input_assets WHERE id=$1 FOR UPDATE`
		if kind == "cleanup" {
			query = `SELECT id FROM comfy_output_cleanup_tombstones WHERE id=$1 FOR UPDATE`
		}
		var found string
		if err := tx.QueryRowContext(ctx, query, id).Scan(&found); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = ErrComfyStorageConflict
			}
			return nil, err
		}
		keys = append(keys, item.Key)
	}
	var view domain.ComfyStorageInventory
	if err := readComfyStorageActivity(ctx, tx, &view); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, comfyStorageCTE+`SELECT `+comfyStorageItemColumns+` FROM inventory WHERE key=ANY($1::text[]) ORDER BY key`, pq.Array(keys))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ComfyStorageItem, 0, len(selection))
	for rows.Next() {
		item, err := scanComfyStorageItem(rows)
		if err != nil {
			return nil, err
		}
		decorateComfyStorageItem(&item, view.Summary)
		if len(items) >= len(selection) || selection[len(items)].Key != item.Key || selection[len(items)].Version != item.Version || !item.Pending || !item.CanInspect || !updates.ComfyStorageHex(item.ContentHash, 32) || item.ReferenceProtected || item.EligibleAt == nil || item.EligibleAt.After(view.CheckedAt) {
			return nil, ErrComfyStorageConflict
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) != len(selection) {
		return nil, ErrComfyStorageConflict
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, item := range items {
		var protected bool
		if item.Kind == "input" {
			err = tx.QueryRowContext(ctx, `SELECT count(*)<>1 FROM comfy_input_assets WHERE lower(filename)=lower($1) AND lower(subfolder)=lower($2)`, item.Filename, item.Subfolder).Scan(&protected)
		} else {
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM content_media m JOIN content_events e ON e.id=m.event_id WHERE e.service='comfyui' AND m.storage_type='output' AND lower(m.original_name)=lower($1) AND lower(m.subfolder)=lower($2) AND m.expires_at>clock_timestamp())`, item.Filename, item.Subfolder).Scan(&protected)
		}
		if err != nil {
			return nil, err
		}
		if protected {
			return nil, ErrComfyStorageConflict
		}
	}
	return items, nil
}

func (s *Store) CreateComfyStorageOperation(ctx context.Context, actorID int64, id, hostKey string, status updates.ComfyStorageStatus, selection []ComfyStorageRegistrySelection) (ComfyStorageOperation, error) {
	if actorID <= 0 {
		return ComfyStorageOperation{}, ErrComfyStorageDenied
	}
	if _, err := canonicalComfyRegistrySelection(selection); err != nil || !updates.ValidComfyStorageID(id) || !updates.ComfyStorageHex(hostKey, 32) || status.Protocol != updates.ComfyStorageProtocol || !updates.ComfyStorageHex(status.ExecutorID, 32) || !updates.ComfyStorageHex(status.GuardJournalID, 16) || !status.GuardEnforced {
		return ComfyStorageOperation{}, ErrComfyStorageInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ComfyStorageOperation{}, err
	}
	defer tx.Rollback()
	op, err := createComfyStorageOperationTx(ctx, tx, actorID, id, hostKey, status, selection)
	if err != nil {
		return op, err
	}
	return op, tx.Commit()
}

func createComfyStorageOperationTx(ctx context.Context, tx *sql.Tx, actorID int64, id, hostKey string, status updates.ComfyStorageStatus, selection []ComfyStorageRegistrySelection) (ComfyStorageOperation, error) {
	var empty ComfyStorageOperation
	selected, err := canonicalComfyRegistrySelection(selection)
	if err != nil || !updates.ValidComfyStorageID(id) || !updates.ComfyStorageHex(hostKey, 32) || status.Protocol != updates.ComfyStorageProtocol || !updates.ComfyStorageHex(status.ExecutorID, 32) || !updates.ComfyStorageHex(status.GuardJournalID, 16) || !status.GuardEnforced {
		return empty, ErrComfyStorageInput
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO gpu_resources(id) VALUES($1) ON CONFLICT DO NOTHING`, domain.GPUPrimaryResource); err != nil {
		return empty, err
	}
	var resource string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM gpu_resources WHERE id=$1 FOR UPDATE`, domain.GPUPrimaryResource).Scan(&resource); err != nil {
		return empty, err
	}
	name, origin := "", "retention"
	var actor *int64
	if actorID != 0 {
		name, err = lockComfyStorageAdmin(ctx, tx, actorID)
		if err != nil {
			return empty, err
		}
		actor, origin = &actorID, "manual"
	}
	op, err := scanComfyStorageOperation(tx.QueryRowContext(ctx, `SELECT `+comfyStorageOperationColumns+` FROM comfy_storage_operations WHERE id=$1 FOR UPDATE`, id))
	if err == nil {
		if op.Origin != origin || origin == "manual" && (op.ActorID == nil || *op.ActorID != actorID) {
			return op, ErrComfyStorageDenied
		}
		if op.PrunedAt != nil {
			return op, ErrComfyStorageExpired
		}
		if op.HostKey != hostKey || op.Request.ExecutorID != status.ExecutorID || op.Request.GuardJournalID != status.GuardJournalID || !op.MatchesSelection(selected) {
			return op, ErrComfyStorageConflict
		}
		return op, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return op, err
	}
	items, err := lockComfyStorageSelection(ctx, tx, selected)
	if err != nil {
		return empty, err
	}
	request := updates.ComfyStorageRequest{Protocol: updates.ComfyStorageProtocol, ID: id, ExecutorID: status.ExecutorID, GuardJournalID: status.GuardJournalID}
	targets := make([]ComfyStorageTarget, 0, len(items))
	var expected int64
	for i, item := range items {
		key := item.Kind + ":" + strconv.Itoa(i+1)
		request.Items = append(request.Items, updates.ComfyStorageSelection{Key: key, Version: item.Version, File: updates.ComfyAssetFile{Filename: item.Filename, Subfolder: item.Subfolder, StorageType: item.StorageType, SizeBytes: item.SizeBytes, SHA256: strings.ToLower(item.ContentHash)}})
		targets = append(targets, ComfyStorageTarget{ComfyStorageRegistrySelection: selected[i], HostKey: key})
		expected += item.SizeBytes
	}
	request, hash, err := updates.CanonicalComfyStorageRequest(request)
	if err != nil {
		return empty, ErrComfyStorageInput
	}
	rawRequest, _ := json.Marshal(request)
	rawSelection, _ := json.Marshal(targets)
	if len(rawRequest) > 65536 || len(rawSelection) > 65536 {
		return empty, ErrComfyStorageInput
	}
	op, err = scanComfyStorageOperation(tx.QueryRowContext(ctx, `INSERT INTO comfy_storage_operations(id,resource_id,actor_id,actor_name,host_key,request_hash,request,selection,item_count,expected_bytes,origin)
	 VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10,$11) RETURNING `+comfyStorageOperationColumns, id, resource, actor, name, hostKey, hash, string(rawRequest), string(rawSelection), len(items), expected, origin))
	if err != nil {
		return op, err
	}
	if err = comfyStorageAudit(ctx, tx, op, "storage.comfy.preview"); err != nil {
		return op, err
	}
	return op, nil
}

func lockComfyStorageOperation(ctx context.Context, tx *sql.Tx, id string) (ComfyStorageOperation, error) {
	var resource string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM gpu_resources WHERE id=(SELECT resource_id FROM comfy_storage_operations WHERE id=$1) FOR UPDATE`, id).Scan(&resource); err != nil {
		return ComfyStorageOperation{}, err
	}
	return scanComfyStorageOperation(tx.QueryRowContext(ctx, `SELECT `+comfyStorageOperationColumns+` FROM comfy_storage_operations WHERE id=$1 FOR UPDATE`, id))
}

// The sole true return grants one Execute call. All subsequent callers may only
// read/fence the original ID, even when the original process never sent it.
func (s *Store) DispatchComfyStorageOperation(ctx context.Context, actorID int64, id, hostKey, hash string, status updates.ComfyStorageStatus) (ComfyStorageOperation, bool, error) {
	if actorID <= 0 {
		return ComfyStorageOperation{}, false, ErrComfyStorageDenied
	}
	if !updates.ValidComfyStorageID(id) || !updates.ComfyStorageHex(hostKey, 32) || !updates.ComfyStorageHex(hash, 32) {
		return ComfyStorageOperation{}, false, ErrComfyStorageInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ComfyStorageOperation{}, false, err
	}
	defer tx.Rollback()
	op, claimed, err := dispatchComfyStorageOperationTx(ctx, tx, actorID, id, hostKey, hash, status)
	if err != nil {
		return op, false, err
	}
	err = tx.Commit()
	return op, claimed && err == nil, err
}

func dispatchComfyStorageOperationTx(ctx context.Context, tx *sql.Tx, actorID int64, id, hostKey, hash string, status updates.ComfyStorageStatus) (ComfyStorageOperation, bool, error) {
	var empty ComfyStorageOperation
	if !updates.ValidComfyStorageID(id) || !updates.ComfyStorageHex(hostKey, 32) || !updates.ComfyStorageHex(hash, 32) {
		return empty, false, ErrComfyStorageInput
	}
	op, err := lockComfyStorageOperation(ctx, tx, id)
	if err != nil {
		return op, false, err
	}
	if actorID == 0 {
		if op.Origin != "retention" || op.ActorID != nil {
			return op, false, ErrComfyStorageDenied
		}
	} else {
		if _, err = lockComfyStorageAdmin(ctx, tx, actorID); err != nil {
			return op, false, err
		}
		if op.Origin != "manual" || op.ActorID == nil || *op.ActorID != actorID {
			return op, false, ErrComfyStorageDenied
		}
	}
	if op.HostKey != hostKey || op.RequestHash != hash {
		return op, false, ErrComfyStorageConflict
	}
	if op.PrunedAt != nil {
		return op, false, ErrComfyStorageExpired
	}
	if status.Protocol != updates.ComfyStorageProtocol || !status.GuardEnforced || status.ExecutorID != op.Request.ExecutorID || status.GuardJournalID != op.Request.GuardJournalID {
		return op, false, ErrComfyStorageConflict
	}
	if op.State != "preview" {
		return op, false, nil
	}
	var expired, occupied bool
	if err = tx.QueryRowContext(ctx, `SELECT $1::timestamptz<=clock_timestamp()`, op.ExpiresAt).Scan(&expired); err != nil {
		return op, false, err
	}
	if expired {
		return op, false, ErrComfyStorageExpired
	}
	if status.GuardHeld || status.PendingID != "" {
		return op, false, ErrComfyStorageBusy
	}
	if err = tx.QueryRowContext(ctx, `SELECT `+storageAdmissionBlockedSQL+`
	 OR EXISTS(SELECT 1 FROM gpu_work WHERE resource_id=$1 AND state IN ('held','uncertain'))
	 OR EXISTS(SELECT 1 FROM gpu_executor_permits WHERE resource_id=$1 AND state IN ('reserved','running'))
	 OR EXISTS(SELECT 1 FROM lora_training_jobs WHERE state IN ('uploading','preparing','caching','running','installing') AND (agent_submission_started_at IS NOT NULL OR agent_job_id<>''))
	 OR EXISTS(SELECT 1 FROM generation_jobs j WHERE (j.prompt_id IS NOT NULL OR j.submission_started_at IS NOT NULL) AND j.submission_rejected_at IS NULL AND j.resources_released_at IS NULL)`, op.ResourceID).Scan(&occupied); err != nil {
		return op, false, err
	}
	if occupied {
		return op, false, ErrComfyStorageBusy
	}
	selected := make([]ComfyStorageRegistrySelection, 0, len(op.Selection))
	for _, item := range op.Selection {
		selected = append(selected, item.ComfyStorageRegistrySelection)
	}
	if _, err = lockComfyStorageSelection(ctx, tx, selected); err != nil {
		return op, false, err
	}
	op, err = scanComfyStorageOperation(tx.QueryRowContext(ctx, `UPDATE comfy_storage_operations SET state='dispatching',dispatched_at=clock_timestamp(),last_code='dispatch_started' WHERE id=$1 RETURNING `+comfyStorageOperationColumns, id))
	if err != nil {
		return op, false, err
	}
	if err = invalidateGPUObservation(ctx, tx, op.ResourceID); err != nil {
		return op, false, err
	}
	if err = comfyStorageAudit(ctx, tx, op, "storage.comfy.confirm"); err != nil {
		return op, false, err
	}
	return op, true, nil
}

func (s *Store) MarkComfyStorageUnconfirmed(ctx context.Context, id, hostKey, code string) error {
	switch code {
	case "host_unavailable", "host_running", "host_unconfirmed", "host_changed", "invalid_response", "result_write_failed", "guard_not_closed":
	default:
		return ErrComfyStorageInput
	}
	if !updates.ValidComfyStorageID(id) || !updates.ComfyStorageHex(hostKey, 32) {
		return ErrComfyStorageInput
	}
	_, err := s.db.ExecContext(ctx, `UPDATE comfy_storage_operations SET state='unconfirmed',last_code=$3,checked_at=now() WHERE id=$1 AND host_key=$2 AND state IN ('dispatching','unconfirmed')`, id, hostKey, code)
	return err
}

func (s *Store) PendingComfyStorageOperations(ctx context.Context) ([]ComfyStorageOperation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+comfyStorageOperationColumns+` FROM comfy_storage_operations WHERE state IN ('dispatching','unconfirmed') ORDER BY dispatched_at,id LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ComfyStorageOperation{}
	for rows.Next() {
		op, err := scanComfyStorageOperation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, op)
	}
	return items, rows.Err()
}

func (s *Store) CancelComfyStoragePreview(ctx context.Context, actorID int64, id string) (ComfyStorageOperation, error) {
	if !updates.ValidComfyStorageID(id) {
		return ComfyStorageOperation{}, ErrComfyStorageInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ComfyStorageOperation{}, err
	}
	defer tx.Rollback()
	op, err := lockComfyStorageOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if _, err = lockComfyStorageAdmin(ctx, tx, actorID); err != nil {
		return op, err
	}
	if op.ActorID == nil || *op.ActorID != actorID {
		return op, ErrComfyStorageDenied
	}
	if op.State == "cancelled" {
		return op, tx.Commit()
	}
	if op.State != "preview" {
		return op, ErrComfyStorageConflict
	}
	op, err = scanComfyStorageOperation(tx.QueryRowContext(ctx, `UPDATE comfy_storage_operations SET state='cancelled',finished_at=now(),last_code='preview_cancelled' WHERE id=$1 RETURNING `+comfyStorageOperationColumns, id))
	if err != nil {
		return op, err
	}
	if err = comfyStorageAudit(ctx, tx, op, "storage.comfy.cancel"); err != nil {
		return op, err
	}
	return op, tx.Commit()
}

// Kept as closed headers after detail retention, so an old confirmation cannot
// become a new operation against a path that has since been reused.
func (s *Store) PruneComfyStorageOperations(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `WITH expired AS (
	 SELECT id FROM comfy_storage_operations WHERE state='preview' AND expires_at<now() ORDER BY expires_at,id LIMIT 500 FOR UPDATE SKIP LOCKED)
	 UPDATE comfy_storage_operations op SET state='expired',finished_at=now(),last_code='preview_expired' FROM expired WHERE op.id=expired.id`)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	result, err = s.db.ExecContext(ctx, `WITH expired AS (
	 SELECT id FROM comfy_storage_operations WHERE finished_at<now()-interval '90 days' AND pruned_at IS NULL ORDER BY finished_at,id LIMIT 500 FOR UPDATE SKIP LOCKED)
	 UPDATE comfy_storage_operations op SET request=NULL,selection=NULL,result=NULL,actor_name='',pruned_at=now() FROM expired WHERE op.id=expired.id`)
	if err != nil {
		return count, err
	}
	pruned, _ := result.RowsAffected()
	return count + pruned, nil
}
